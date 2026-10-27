package release

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/github"
)

// DoctorLine is one line of `loop doctor`. A failed line says what the
// operator changes. It is the GitHub check line, so the server's lines
// join the host's lines unchanged.
type DoctorLine = github.CheckLine

// Doctor runs the host checks of `loop doctor`. The GitHub checks
// need the platform credentials, so they run on the server, and Remote
// returns their lines.
type Doctor struct {
	Layout    Layout
	Installer *Installer
	// Home is the operator's home directory, where each harness keeps its
	// settings.
	Home string
	// Repos lists the repository paths the agent host knows.
	Repos func(ctx context.Context, cfg *config.HostConfig) ([]string, error)
	// Remote runs the server-side checks. Nil, or an error, fails one line.
	Remote func(ctx context.Context) ([]DoctorLine, error)
}

// Run returns every line, failed or not.
func (d *Doctor) Run(ctx context.Context) []DoctorLine {
	var lines []DoctorLine
	add := func(check string, ok bool, format string, args ...any) {
		lines = append(lines, DoctorLine{Check: check, OK: ok, Detail: fmt.Sprintf(format, args...)})
	}
	if d.Installer != nil {
		rep, err := d.Installer.Check(ctx)
		switch {
		case err != nil:
			add("install", false, "the install check could not run: %v", err)
		case len(rep.Drift) > 0:
			add("install", false, "run loop install: %s", strings.Join(rep.Drift, "; "))
		default:
			add("install", true, "the service, binaries, host config, key file and credential match loop install")
		}
	}
	cfg, err := config.LoadHost(d.Layout.HostConfig())
	if err != nil {
		add("host_config", false, "the host config cannot be read: %v", err)
		return lines
	}
	lines = append(lines, d.harnesses(cfg)...)
	lines = append(lines, d.repos(ctx, cfg)...)
	if len(cfg.Projects) > 0 {
		lines = append(lines, d.remote(ctx)...)
		lines = append(lines, judgeBinaries())
	}
	return lines
}

// judgeBinaries checks that each judge of the fixer's panel is on the
// path. It runs no judge, so it calls no model and no Jev (ADR 0004 D12).
func judgeBinaries() DoctorLine {
	line := DoctorLine{Check: "judge_binaries"}
	var missing []string
	for _, bin := range escalation.JudgeBins() {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	line.OK = len(missing) == 0
	line.Detail = "each judge of the fixer's panel is on the path: " + strings.Join(escalation.JudgeBins(), ", ")
	if !line.OK {
		line.Detail = "put each judge of the fixer's panel on the path: " + strings.Join(missing, ", ")
	}
	return line
}

// Failed lists the failed lines.
func Failed(lines []DoctorLine) []DoctorLine {
	var out []DoctorLine
	for _, l := range lines {
		if !l.OK {
			out = append(out, l)
		}
	}
	return out
}

func (d *Doctor) remote(ctx context.Context) []DoctorLine {
	if d.Remote == nil {
		return []DoctorLine{{Check: "github", Detail: "this build cannot ask the server for the GitHub checks"}}
	}
	lines, err := d.Remote(ctx)
	if err != nil {
		return []DoctorLine{{Check: "github", Detail: "the server's GitHub checks could not run: " + err.Error()}}
	}
	return lines
}

// harness is one harness the doctor checks.
type harness struct {
	name      string
	configDir string
}

func (d *Doctor) harnessList(cfg *config.HostConfig) []harness {
	var out []harness
	for _, h := range cfg.Harnesses {
		out = append(out, harness{name: h.Name, configDir: h.ConfigDir})
	}
	if len(out) > 0 {
		return out
	}
	// With no harness listed, check Claude Code, which the coordinator
	// runs, and Codex when this host has its settings.
	out = append(out, harness{name: "claude"})
	if _, err := os.Stat(filepath.Join(d.Home, ".codex")); err == nil {
		out = append(out, harness{name: "codex"})
	}
	return out
}

// harnesses checks that no harness stops an unattended start with an
// update prompt or a trust dialog. The checks read each harness's own
// settings files, whose shape the vendor may change: a line that cannot
// decide fails and names the file it read.
func (d *Doctor) harnesses(cfg *config.HostConfig) []DoctorLine {
	root := cfg.AgentHost.CheckoutRoot
	var lines []DoctorLine
	for _, h := range d.harnessList(cfg) {
		switch h.name {
		case "claude":
			lines = append(lines, d.claude(h, root)...)
		case "codex":
			lines = append(lines, d.codex(h, root)...)
		default:
			lines = append(lines, DoctorLine{Check: h.name + "_settings", Detail: "loop doctor knows no settings of harness " + h.name})
		}
	}
	return lines
}

func (d *Doctor) claude(h harness, root string) []DoctorLine {
	dir := h.configDir
	state := filepath.Join(d.Home, ".claude.json")
	if dir == "" {
		dir = filepath.Join(d.Home, ".claude")
	} else {
		state = filepath.Join(dir, ".claude.json")
	}
	var global struct {
		AutoUpdates *bool `json:"autoUpdates"`
		Projects    map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	gerr := readJSON(state, &global)
	serr := readJSON(filepath.Join(dir, "settings.json"), &settings)
	update := DoctorLine{Check: "claude_update_prompt"}
	switch v := settings.Env["DISABLE_AUTOUPDATER"]; {
	case serr == nil && v != "" && v != "0" && v != "false":
		update.OK, update.Detail = true, "settings.json sets DISABLE_AUTOUPDATER"
	case gerr == nil && global.AutoUpdates != nil && !*global.AutoUpdates:
		update.OK, update.Detail = true, state+" sets autoUpdates false"
	default:
		update.Detail = fmt.Sprintf("set env.DISABLE_AUTOUPDATER to \"1\" in %s, so an update never interrupts an unattended start",
			filepath.Join(dir, "settings.json"))
	}
	trust := DoctorLine{Check: "claude_trust_dialog"}
	switch {
	case root == "":
		trust.Detail = "host config sets no agent_host.checkout_root, so no trusted directory can be checked"
	case gerr != nil:
		trust.Detail = fmt.Sprintf("%s cannot be read: %v", state, gerr)
	default:
		for _, p := range ancestors(root) {
			if global.Projects[p].HasTrustDialogAccepted {
				trust.OK, trust.Detail = true, "Claude Code trusts "+p
				break
			}
		}
		if !trust.OK {
			trust.Detail = fmt.Sprintf("accept the trust dialog once for %s, or for a directory above it; %s records it", root, state)
		}
	}
	return []DoctorLine{update, trust}
}

func (d *Doctor) codex(h harness, root string) []DoctorLine {
	dir := h.configDir
	if dir == "" {
		dir = filepath.Join(d.Home, ".codex")
	}
	path := filepath.Join(dir, "config.toml")
	top, tables, err := readTOMLKeys(path)
	update := DoctorLine{Check: "codex_update_prompt"}
	trust := DoctorLine{Check: "codex_trust_dialog"}
	if err != nil {
		update.Detail = fmt.Sprintf("%s cannot be read: %v", path, err)
		trust.Detail = update.Detail
		return []DoctorLine{update, trust}
	}
	if top["check_for_update_on_startup"] == "false" {
		update.OK, update.Detail = true, path+" sets check_for_update_on_startup = false"
	} else {
		update.Detail = "set check_for_update_on_startup = false at the top of " + path
	}
	if root == "" {
		trust.Detail = "host config sets no agent_host.checkout_root, so no trusted directory can be checked"
	} else {
		for _, p := range ancestors(root) {
			if tables[`projects."`+p+`"`]["trust_level"] == `"trusted"` {
				trust.OK, trust.Detail = true, "Codex trusts "+p
				break
			}
		}
		if !trust.OK {
			trust.Detail = fmt.Sprintf("add [projects.\"%s\"] with trust_level = \"trusted\" to %s", root, path)
		}
	}
	return []DoctorLine{update, trust}
}

// repos checks that the agent host knows each project's repository.
func (d *Doctor) repos(ctx context.Context, cfg *config.HostConfig) []DoctorLine {
	if len(cfg.Projects) == 0 {
		return nil
	}
	line := DoctorLine{Check: "agent_host_repos"}
	if d.Repos == nil || cfg.AgentHost.Kind == "" {
		line.Detail = "host config names no agent host"
		return []DoctorLine{line}
	}
	known, err := d.Repos(ctx, cfg)
	if err != nil {
		line.Detail = "the agent host's repositories could not be read: " + err.Error()
		return []DoctorLine{line}
	}
	var missing []string
	for _, p := range cfg.Projects {
		if !slices.Contains(known, filepath.Clean(p.Path)) {
			missing = append(missing, p.ID+" ("+p.Path+")")
		}
	}
	line.OK = len(missing) == 0
	line.Detail = "every project's repository is known to the agent host"
	if !line.OK {
		line.Detail = "add each repository to the agent host: " + strings.Join(missing, ", ")
	}
	return []DoctorLine{line}
}

// OrcaRepos reads `orca repo list --json`.
func OrcaRepos(run func(ctx context.Context, bin string, args ...string) ([]byte, error)) func(context.Context, *config.HostConfig) ([]string, error) {
	return func(ctx context.Context, cfg *config.HostConfig) ([]string, error) {
		if cfg.AgentHost.Kind != "orca" {
			return nil, fmt.Errorf("agent host %q has no repository list", cfg.AgentHost.Kind)
		}
		bin := cfg.AgentHost.Binary
		if bin == "" {
			bin = "orca"
		}
		out, err := run(ctx, bin, "repo", "list", "--json")
		if err != nil {
			return nil, err
		}
		var env struct {
			OK     bool `json:"ok"`
			Result struct {
				Repos []struct {
					Path string `json:"path"`
				} `json:"repos"`
			} `json:"result"`
		}
		if err := json.Unmarshal(out, &env); err != nil || !env.OK {
			return nil, errors.New("orca repo list printed no ok answer")
		}
		var paths []string
		for _, r := range env.Result.Repos {
			paths = append(paths, filepath.Clean(r.Path))
		}
		return paths, nil
	}
}

func ancestors(p string) []string {
	p = filepath.Clean(p)
	out := []string{p}
	for {
		parent := filepath.Dir(p)
		if parent == p {
			return out
		}
		out, p = append(out, parent), parent
	}
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// readTOMLKeys reads the "key = value" lines of a TOML file: the top-level
// keys, and the keys of each [table]. Values stay as written. This is
// enough for the two settings the doctor reads, and it is not a TOML
// parser.
func readTOMLKeys(path string) (map[string]string, map[string]map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, map[string]map[string]string{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	top := map[string]string{}
	tables := map[string]map[string]string{}
	cur := top
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSpace(strings.Trim(line, "[]"))
			if tables[name] == nil {
				tables[name] = map[string]string{}
			}
			cur = tables[name]
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		cur[strings.TrimSpace(k)] = v
	}
	return top, tables, sc.Err()
}
