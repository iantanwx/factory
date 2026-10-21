package agenthost_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
)

func TestParseRunIDRefusesMalformed(t *testing.T) {
	for _, s := range []string{"", "a/b", "a/b/c/d", "a//c", "a b/c/d", "orca:local/term x/i"} {
		if _, err := agenthost.ParseRunID(s); !errors.Is(err, agenthost.ErrBadSpec) {
			t.Errorf("ParseRunID(%q) = %v; want ErrBadSpec", s, err)
		}
	}
}

func TestInsideRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		filepath.Join(root, "a"):             true,
		filepath.Join(root, "new", "deeper"): true,
		root:                                 false,
		filepath.Join(root, "..", "x"):       false,
		filepath.Join(root, "out"):           false,
		filepath.Join(root, "out", "x"):      false,
		root + "-sibling":                    false,
		"relative/path":                      false,
	}
	for p, want := range cases {
		if got := agenthost.InsideRoot(root, p); got != want {
			t.Errorf("InsideRoot(%s) = %v; want %v", p, got, want)
		}
	}
}

func TestHarnessArgv(t *testing.T) {
	spec := agenthost.StartSpec{
		Kind: agenthost.KindStage, Checkout: "/r/c", Harness: agenthost.Claude,
		Model: "claude-opus-4-1", Effort: "max", PromptFile: "/p/prompt.txt",
	}
	got, err := agenthost.HarnessArgv(spec, []string{"--x"})
	want := []string{"claude", "--model", "claude-opus-4-1", "--effort", "max", "--x", agenthost.PromptInstruction("/p/prompt.txt")}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("HarnessArgv = %q, %v", got, err)
	}
	bad := []func(s *agenthost.StartSpec){
		func(s *agenthost.StartSpec) { s.Model = "" },
		func(s *agenthost.StartSpec) { s.Model = "m; rm -rf /" },
		func(s *agenthost.StartSpec) { s.Effort = "High!" },
		func(s *agenthost.StartSpec) { s.Harness = "gemini" },
		func(s *agenthost.StartSpec) { s.Kind = "chat" },
		func(s *agenthost.StartSpec) { s.PromptFile = "prompt.txt" },
		func(s *agenthost.StartSpec) { s.Checkout = "" },
	}
	for i, mod := range bad {
		s := spec
		mod(&s)
		if _, err := agenthost.HarnessArgv(s, nil); !errors.Is(err, agenthost.ErrBadSpec) {
			t.Errorf("case %d: HarnessArgv = %v; want ErrBadSpec", i, err)
		}
	}
}
