package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func byCheck(lines []DoctorLine) map[string]DoctorLine {
	m := map[string]DoctorLine{}
	for _, l := range lines {
		m[l.Check] = l
	}
	return m
}

func TestDoctorChecksTheHost(t *testing.T) {
	w := newInstallWorld(t)
	ctx := context.Background()
	if _, err := w.in.Install(ctx); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	root := filepath.Join(home, "checkouts")
	cfg, err := config.LoadHost(w.in.Layout.HostConfig())
	if err != nil {
		t.Fatal(err)
	}
	cfg.AgentHost = config.AgentHostConfig{Kind: "orca", CheckoutRoot: root}
	cfg.Projects = []config.Project{{ID: "alpha", Repo: "o/alpha", Path: "/src/alpha"}, {ID: "beta", Repo: "o/beta", Path: "/src/beta"}}
	if err := config.SaveHost(w.in.Layout.HostConfig(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	d := &Doctor{Layout: w.in.Layout, Installer: w.in, Home: home,
		Repos: func(context.Context, *config.HostConfig) ([]string, error) { return []string{"/src/alpha"}, nil },
		Remote: func(context.Context) ([]DoctorLine, error) {
			return []DoctorLine{{Check: "alpha/project_config", Detail: "merge it"}}, nil
		},
	}

	// Nothing is set up: each harness line fails and says what to change.
	got := byCheck(d.Run(ctx))
	for _, check := range []string{"claude_update_prompt", "claude_trust_dialog", "codex_update_prompt", "codex_trust_dialog",
		"agent_host_repos", "alpha/project_config"} {
		if got[check].OK || got[check].Detail == "" {
			t.Errorf("%s = %+v", check, got[check])
		}
	}
	if !got["install"].OK {
		t.Errorf("install = %+v", got["install"])
	}
	if !strings.Contains(got["agent_host_repos"].Detail, "beta") || strings.Contains(got["agent_host_repos"].Detail, "alpha") {
		t.Errorf("agent_host_repos = %q", got["agent_host_repos"].Detail)
	}

	// The operator sets each one up.
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"env":{"DISABLE_AUTOUPDATER":"1"}}`)
	writeFile(t, filepath.Join(home, ".claude.json"), `{"projects":{"`+home+`":{"hasTrustDialogAccepted":true}}}`)
	writeFile(t, filepath.Join(home, ".codex", "config.toml"),
		"model = \"x\"\ncheck_for_update_on_startup = false # no prompt\n\n[projects.\""+root+"\"]\ntrust_level = \"trusted\"\n")
	d.Repos = func(context.Context, *config.HostConfig) ([]string, error) {
		return []string{"/src/alpha", "/src/beta"}, nil
	}
	d.Remote = func(context.Context) ([]DoctorLine, error) {
		return []DoctorLine{{Check: "alpha/project_config", OK: true}}, nil
	}
	if failed := Failed(d.Run(ctx)); len(failed) != 0 {
		t.Fatalf("failed = %+v", failed)
	}

	// A server that cannot answer fails one line, never a pass.
	d.Remote = func(context.Context) ([]DoctorLine, error) { return nil, errors.New("the server is not reachable") }
	if failed := Failed(d.Run(ctx)); len(failed) != 1 || failed[0].Check != "github" {
		t.Fatalf("failed = %+v", failed)
	}
}

func TestOrcaReposReadsThePaths(t *testing.T) {
	run := func(_ context.Context, bin string, args ...string) ([]byte, error) {
		if bin != "/opt/orca" || strings.Join(args, " ") != "repo list --json" {
			t.Fatalf("ran %s %q", bin, args)
		}
		return []byte(`{"id":"x","ok":true,"result":{"repos":[{"id":"1","path":"/src/alpha/","displayName":"alpha"}]}}`), nil
	}
	paths, err := OrcaRepos(run)(context.Background(), &config.HostConfig{AgentHost: config.AgentHostConfig{Kind: "orca", Binary: "/opt/orca"}})
	if err != nil || len(paths) != 1 || paths[0] != "/src/alpha" {
		t.Fatalf("paths = %q %v", paths, err)
	}
}
