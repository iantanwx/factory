package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// AppKeySecret is the secret-store name, under config.HostScope, of the
// key of a host's first App, from before Apps had owners.
const AppKeySecret = "GITHUB_APP_KEY"

// AppKeySecretFor is the secret-store name of the key of owner's App.
func AppKeySecretFor(owner string) string {
	return "GITHUB_APP_KEY_" + strings.ToUpper(strings.ReplaceAll(owner, "-", "_"))
}

// SaveApp stores app's key and records the App under its owner in host
// config. It reads host config again first: the running server may have
// written it while the operator was in the browser.
func SaveApp(ctx context.Context, l Layout, app CreatedApp) error {
	name := AppKeySecretFor(app.Owner)
	if err := storeHostSecret(ctx, l, name, app.PEM); err != nil {
		return err
	}
	cfg, err := config.LoadHost(l.HostConfig())
	if err != nil {
		return err
	}
	cfg.Credentials.SetApp(config.GitHubAppCredentials{Owner: app.Owner, AppID: app.ID, PrivateKeySecret: name})
	return config.SaveHost(l.HostConfig(), cfg)
}

// HostSetup is the part of `loop install` outside the instance directory:
// linger, the harness settings that `loop doctor` checks, and the loop
// GitHub App. Each step leaves alone what is already set, so a second
// install changes nothing.
type HostSetup struct {
	// Home is the operator's home directory.
	Home string
	// User is the operator's login name, for loginctl.
	User string
	// GOOS selects the linger step, which only Linux has.
	GOOS string
	// Run runs loginctl.
	Run Runner
	// App creates the loop App when host config names none. Nil skips it.
	App func(ctx context.Context) (CreatedApp, error)
	// Orca is the orca CLI's absolute path, because the service runs with
	// a bare PATH. Empty when this host has no orca.
	Orca string
}

// defaultAgentHost fills the agent host fields a new host config leaves
// empty: orca, its binary, and its checkouts under ~/orca/workspaces.
func (s *HostSetup) defaultAgentHost(cfg *config.HostConfig) {
	if cfg.AgentHost.Kind == "" {
		cfg.AgentHost.Kind = "orca"
	}
	if cfg.AgentHost.Binary == "" {
		cfg.AgentHost.Binary = s.Orca
	}
	if cfg.AgentHost.CheckoutRoot == "" {
		cfg.AgentHost.CheckoutRoot = filepath.Join(s.Home, "orca", "workspaces")
	}
}

// run does every step and returns one line for each change it made, and
// whether host config gained the App. A step that fails stops install,
// except linger, which a host without logind cannot set: that becomes a
// line naming the command to run.
func (s *HostSetup) run(ctx context.Context, l Layout) (notes []string, appAdded bool, err error) {
	if s.GOOS == "linux" && s.User != "" {
		if err := s.Run(ctx, "loginctl", "enable-linger", s.User); err != nil {
			notes = append(notes, fmt.Sprintf("linger is not set, so the service stops at logout; run loginctl enable-linger %s: %v", s.User, err))
		}
	}
	if note, err := s.linkCLI(l); err != nil {
		return notes, false, err
	} else if note != "" {
		notes = append(notes, note)
	}
	cfg, err := config.LoadHost(l.HostConfig())
	if err != nil {
		return notes, false, err
	}
	changed, err := s.harnesses(cfg)
	if err != nil {
		return notes, false, err
	}
	notes = append(notes, changed...)
	if s.App == nil || len(cfg.Credentials.Apps()) > 0 {
		return notes, false, nil
	}
	app, err := s.App(ctx)
	if err != nil {
		return notes, false, err
	}
	if err := SaveApp(ctx, l, app); err != nil {
		return notes, false, err
	}
	notes = append(notes, fmt.Sprintf("created the GitHub App %s (id %d); install it on each project repository at %s", app.Slug, app.ID, app.InstallURL))
	return notes, true, nil
}

// linkCLI puts loop on the operator's PATH: ~/.local/bin/loop links to
// the instance's current binary, so each upgrade moves it too. A file
// there that is not a symlink stays.
func (s *HostSetup) linkCLI(l Layout) (string, error) {
	link := filepath.Join(s.Home, ".local", "bin", "loop")
	target := filepath.Join(l.Dir, "current", BinaryName)
	have, err := os.Readlink(link)
	switch {
	case err == nil && have == target:
		return "", nil
	case err == nil:
		if err := os.Remove(link); err != nil {
			return "", err
		}
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("%s exists and is not a symlink, so loop is not linked there; run %s instead", link, target), nil
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return "", err
	}
	if err := os.Symlink(target, link); err != nil {
		return "", err
	}
	return "linked " + link + " to " + target, nil
}

// storeHostSecret writes one platform credential into the secret store
// directly. The server may be running: SQLite takes the second writer.
func storeHostSecret(ctx context.Context, l Layout, name string, value []byte) error {
	keys, err := secrets.NewFileKeyProvider(l.KeyFile())
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, l.DB())
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return secrets.New(db, keys).Set(ctx, config.HostScope, name, strings.NewReader(string(value)))
}

// ReadSecret reads one value from the secret store directly, as
// storeHostSecret writes one. `loop register` reads the global release
// token with it, to copy the token into a project's Actions secrets.
func ReadSecret(ctx context.Context, l Layout, scope, name string) ([]byte, error) {
	keys, err := secrets.NewFileKeyProvider(l.KeyFile())
	if err != nil {
		return nil, err
	}
	db, err := store.Open(ctx, l.DB())
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	return secrets.New(db, keys).Get(ctx, scope, name)
}

// harnesses turns off each harness's update prompt and trusts the checkout
// root, as `loop doctor` checks them.
func (s *HostSetup) harnesses(cfg *config.HostConfig) ([]string, error) {
	root := cfg.AgentHost.CheckoutRoot
	var notes []string
	for _, h := range (&Doctor{Home: s.Home}).harnessList(cfg) {
		var changed []string
		var err error
		switch h.name {
		case "claude":
			changed, err = s.claude(h, root)
		case "codex":
			changed, err = s.codex(h, root)
		}
		if err != nil {
			return notes, fmt.Errorf("release: set the %s settings: %w", h.name, err)
		}
		notes = append(notes, changed...)
	}
	return notes, nil
}

func (s *HostSetup) claude(h harness, root string) ([]string, error) {
	dir, state := h.configDir, filepath.Join(s.Home, ".claude.json")
	if dir == "" {
		dir = filepath.Join(s.Home, ".claude")
	} else {
		state = filepath.Join(dir, ".claude.json")
	}
	var notes []string
	settingsPath := filepath.Join(dir, "settings.json")
	settings, err := readJSONObject(settingsPath)
	if err != nil {
		return nil, err
	}
	env, _ := settings["env"].(map[string]any)
	if v, _ := env["DISABLE_AUTOUPDATER"].(string); v == "" || v == "0" || v == "false" {
		if env == nil {
			env = map[string]any{}
		}
		env["DISABLE_AUTOUPDATER"] = "1"
		settings["env"] = env
		if err := writeJSONObject(settingsPath, settings); err != nil {
			return nil, err
		}
		notes = append(notes, "set env.DISABLE_AUTOUPDATER in "+settingsPath)
	}
	if root == "" {
		return notes, nil
	}
	global, err := readJSONObject(state)
	if err != nil {
		return nil, err
	}
	projects, _ := global["projects"].(map[string]any)
	for _, p := range ancestors(root) {
		if entry, _ := projects[p].(map[string]any); entry["hasTrustDialogAccepted"] == true {
			return notes, nil
		}
	}
	if projects == nil {
		projects = map[string]any{}
	}
	entry, _ := projects[root].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	entry["hasTrustDialogAccepted"] = true
	projects[root] = entry
	global["projects"] = projects
	if err := writeJSONObject(state, global); err != nil {
		return nil, err
	}
	return append(notes, "trusted "+root+" in "+state), nil
}

func (s *HostSetup) codex(h harness, root string) ([]string, error) {
	dir := h.configDir
	if dir == "" {
		dir = filepath.Join(s.Home, ".codex")
	}
	path := filepath.Join(dir, "config.toml")
	top, tables, err := readTOMLKeys(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	text := string(data)
	var notes []string
	if top["check_for_update_on_startup"] != "false" {
		text = setTOMLTopKey(text, "check_for_update_on_startup", "false")
		notes = append(notes, "set check_for_update_on_startup = false in "+path)
	}
	trusted := root == ""
	for _, p := range ancestors(root) {
		if !trusted && tables[`projects."`+p+`"`]["trust_level"] == `"trusted"` {
			trusted = true
		}
	}
	if !trusted {
		text = setTOMLTableKey(text, `projects."`+root+`"`, "trust_level", `"trusted"`)
		notes = append(notes, "trusted "+root+" in "+path)
	}
	if len(notes) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return notes, writeFileAtomic(path, []byte(text), 0o600)
}

// setTOMLTopKey sets key = value among the top-level keys: it replaces the
// key's line before the first table, or puts the line first.
func setTOMLTopKey(text, key, value string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			break
		}
		if k, _, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == key {
			lines[i] = key + " = " + value
			return strings.Join(lines, "\n")
		}
	}
	return key + " = " + value + "\n" + text
}

// setTOMLTableKey sets key = value in [table]: it replaces the key's line
// in the table, adds the line under the table's header, or adds the table
// at the end.
func setTOMLTableKey(text, table, key, value string) string {
	lines := strings.Split(text, "\n")
	in, header := false, -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			in = strings.TrimSpace(strings.Trim(t, "[]")) == table
			if in {
				header = i
			}
			continue
		}
		if k, _, ok := strings.Cut(t, "="); in && ok && strings.TrimSpace(k) == key {
			lines[i] = key + " = " + value
			return strings.Join(lines, "\n")
		}
	}
	if header >= 0 {
		lines = append(lines[:header+1], append([]string{key + " = " + value}, lines[header+1:]...)...)
		return strings.Join(lines, "\n")
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if text != "" {
		text += "\n"
	}
	return text + "[" + table + "]\n" + key + " = " + value + "\n"
}

// readJSONObject reads a JSON object, or an empty one when the file is
// missing, keeping every key it does not know.
func readJSONObject(path string) (map[string]any, error) {
	obj := map[string]any{}
	err := readJSON(path, &obj)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	return obj, nil
}

// writeJSONObject writes obj atomically, keeping the file's mode when it
// exists.
func writeJSONObject(path string, obj map[string]any) error {
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	perm := fs.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		perm = st.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), perm)
}
