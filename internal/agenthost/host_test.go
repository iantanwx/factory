package agenthost_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

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

// C-bug-1: the first message opens with the session's title on one line:
// newlines and control characters are replaced, and its length is bounded.
// The prompt file instruction follows on the same line, so a host that
// types the message into a terminal submits it once. No prompt text joins.
func TestFirstMessageOpensWithOneTitleLine(t *testing.T) {
	spec := agenthost.StartSpec{
		Kind: agenthost.KindStage, Checkout: "/r/c", Harness: agenthost.Claude, Model: "opus", PromptFile: "/p/prompt.txt",
		Title: "loop alpha implementer tk-1: Fix\nthe\tthing\x1b[31m\r" + strings.Repeat(" long", 200),
	}
	argv, err := agenthost.HarnessArgv(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	msg := argv[len(argv)-1]
	if !strings.HasPrefix(msg, "loop alpha implementer tk-1: Fix the thing") {
		t.Errorf("first message %q does not open with the title", msg)
	}
	if strings.ContainsFunc(msg, unicode.IsControl) {
		t.Errorf("first message %q is not one line", msg)
	}
	title, ok := strings.CutSuffix(msg, agenthost.PromptInstruction("/p/prompt.txt"))
	if !ok {
		t.Fatalf("first message %q does not end with the prompt file instruction", msg)
	}
	if n := utf8.RuneCountInString(title); n > 200 {
		t.Errorf("the title holds %d runes", n)
	}
}

// C-bug-1 (F-21): past the age bound only a complete inventory, of any
// epoch, that lists neither the exact identity nor a trace settles a run.
// The exact-identity row matters: orca reads a disconnected live terminal
// as unknown while its inventory still lists it, and Traces skips that row.
func TestUntracedNeedsACompleteInventoryWithNoExactRowAndNoTrace(t *testing.T) {
	s := agenthost.Session{ID: agenthost.RunID{Host: "h", Terminal: "t1", Incarnation: "i1"}, Pty: "p1", Epoch: "e1"}
	other := agenthost.Row{ID: agenthost.RunID{Host: "h", Terminal: "t9", Incarnation: "i9"}, Pty: "p9"}
	cases := []struct {
		name string
		inv  agenthost.Inventory
		want bool
	}{
		{"complete, other epoch, exact row", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{other, {ID: s.ID, Pty: "p1"}}}, false},
		{"complete, same epoch, exact row", agenthost.Inventory{Complete: true, Epoch: "e1", Rows: []agenthost.Row{{ID: s.ID}}}, false},
		{"complete, incarnation trace", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{{ID: agenthost.RunID{Host: "h", Terminal: "t2", Incarnation: "i1"}}}}, false},
		{"complete, PTY trace", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{{ID: agenthost.RunID{Host: "h", Terminal: "t2", Incarnation: "i2"}, Pty: "p1"}}}, false},
		{"incomplete, no row", agenthost.Inventory{Complete: false, Epoch: "e2", Rows: []agenthost.Row{other}}, false},
		{"complete, other epoch, no row", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{other}}, true},
		{"complete, same epoch, empty", agenthost.Inventory{Complete: true, Epoch: "e1"}, true},
	}
	for _, c := range cases {
		if got := c.inv.Untraced(s); got != c.want {
			t.Errorf("%s: Untraced = %v; want %v", c.name, got, c.want)
		}
	}
}

// C-bug-1: only a complete inventory with exactly one row of the same
// incarnation and the same checkout under another handle rebinds the
// session, and the session then carries that row's identity and the
// inventory's epoch.
func TestReboundNeedsOneRowOfTheSameIncarnationAndCheckout(t *testing.T) {
	s := agenthost.Session{ID: agenthost.RunID{Host: "h", Terminal: "t1", Incarnation: "i1"}, Checkout: "/c/coord", Pty: "p1", Epoch: "e1"}
	moved := agenthost.Row{ID: agenthost.RunID{Host: "h", Terminal: "t2", Incarnation: "i1"}, Checkout: "/c/coord", Pty: "p1"}
	twin := agenthost.Row{ID: agenthost.RunID{Host: "h", Terminal: "t3", Incarnation: "i1"}, Checkout: "/c/coord", Pty: "p3"}
	elsewhere := agenthost.Row{ID: agenthost.RunID{Host: "h", Terminal: "t2", Incarnation: "i1"}, Checkout: "/c/other", Pty: "p1"}
	reborn := agenthost.Row{ID: agenthost.RunID{Host: "h", Terminal: "t1", Incarnation: "i2"}, Checkout: "/c/coord", Pty: "p1"}
	other := agenthost.Row{ID: agenthost.RunID{Host: "h", Terminal: "t9", Incarnation: "i9"}, Checkout: "/c/x", Pty: "p9"}
	cases := []struct {
		name string
		inv  agenthost.Inventory
		want bool
	}{
		{"complete, one moved row", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{other, moved}}, true},
		{"incomplete, one moved row", agenthost.Inventory{Complete: false, Epoch: "e2", Rows: []agenthost.Row{moved}}, false},
		{"complete, no epoch", agenthost.Inventory{Complete: true, Rows: []agenthost.Row{moved}}, false},
		{"complete, two moved rows", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{moved, twin}}, false},
		{"complete, no row of the incarnation", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{other}}, false},
		{"complete, the incarnation in another checkout", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{elsewhere}}, false},
		{"complete, same handle under another incarnation", agenthost.Inventory{Complete: true, Epoch: "e1", Rows: []agenthost.Row{reborn}}, false},
		{"complete, exact row and a moved row", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{{ID: s.ID, Checkout: "/c/coord"}, moved}}, false},
	}
	for _, c := range cases {
		got, ok := c.inv.Rebound(s)
		if ok != c.want {
			t.Errorf("%s: Rebound ok = %v; want %v", c.name, ok, c.want)
			continue
		}
		if !ok && got != s {
			t.Errorf("%s: a refused rebind changed the session to %+v", c.name, got)
		}
	}
	got, _ := agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{other, moved}}.Rebound(s)
	want := s
	want.ID = agenthost.RunID{Host: "h", Terminal: "t2", Incarnation: "i1"}
	want.Epoch = "e2"
	if got != want {
		t.Errorf("Rebound = %+v; want %+v", got, want)
	}
}

// C-bug-1 (F-30): when a complete inventory lists no row of the session's
// incarnation, only the one live row in the session's checkout that names
// the session's harness as its agent rebinds the session. A shell, a second
// agent row or an agent of another harness rebinds nothing.
func TestReboundToAgentNeedsTheOneAgentOfTheHarnessInTheCheckout(t *testing.T) {
	s := agenthost.Session{ID: agenthost.RunID{Host: "h", Terminal: "t1", Incarnation: "i1"}, Checkout: "/c/coord", Harness: agenthost.Claude, Pty: "p1", Epoch: "e1"}
	agent := agenthost.Row{ID: agenthost.RunID{Host: "h", Terminal: "t2", Incarnation: "i2"}, Checkout: "/c/coord", Pty: "p2", Agent: "claude", Connected: true}
	twin := agenthost.Row{ID: agenthost.RunID{Host: "h", Terminal: "t3", Incarnation: "i3"}, Checkout: "/c/coord", Pty: "p3", Agent: "claude", Connected: true}
	shell := agenthost.Row{ID: agenthost.RunID{Host: "h", Terminal: "t4", Incarnation: "i4"}, Checkout: "/c/coord", Pty: "p4", Connected: true}
	codex := agenthost.Row{ID: agenthost.RunID{Host: "h", Terminal: "t5", Incarnation: "i5"}, Checkout: "/c/coord", Pty: "p5", Agent: "codex", Connected: true}
	elsewhere := agenthost.Row{ID: agenthost.RunID{Host: "h", Terminal: "t6", Incarnation: "i6"}, Checkout: "/c/other", Pty: "p6", Agent: "claude", Connected: true}
	moved := agenthost.Row{ID: agenthost.RunID{Host: "h", Terminal: "t7", Incarnation: "i1"}, Checkout: "/c/other", Pty: "p1", Agent: "claude", Connected: true}
	gone := agent
	gone.Connected = false
	cases := []struct {
		name string
		inv  agenthost.Inventory
		want bool
	}{
		{"complete, one agent row and a shell", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{shell, agent, elsewhere}}, true},
		{"complete, a shell only", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{shell}}, false},
		{"complete, two agent rows", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{agent, twin}}, false},
		{"complete, an agent of another harness", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{codex}}, false},
		{"complete, the agent in another checkout", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{elsewhere}}, false},
		{"complete, the agent row disconnected", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{gone}}, false},
		{"incomplete, one agent row", agenthost.Inventory{Complete: false, Epoch: "e2", Rows: []agenthost.Row{agent}}, false},
		{"complete, no epoch", agenthost.Inventory{Complete: true, Rows: []agenthost.Row{agent}}, false},
		{"complete, a row of the incarnation elsewhere", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{agent, moved}}, false},
		{"complete, the exact row", agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{{ID: s.ID, Checkout: "/c/coord", Agent: "claude", Connected: true}, agent}}, false},
	}
	for _, c := range cases {
		got, ok := c.inv.ReboundToAgent(s)
		if ok != c.want {
			t.Errorf("%s: ReboundToAgent ok = %v; want %v", c.name, ok, c.want)
			continue
		}
		if !ok && got != s {
			t.Errorf("%s: a refused rebind changed the session to %+v", c.name, got)
		}
	}
	got, _ := agenthost.Inventory{Complete: true, Epoch: "e2", Rows: []agenthost.Row{shell, agent}}.ReboundToAgent(s)
	want := s
	want.ID = agenthost.RunID{Host: "h", Terminal: "t2", Incarnation: "i2"}
	want.Pty, want.Epoch = "p2", "e2"
	if got != want {
		t.Errorf("ReboundToAgent = %+v; want %+v", got, want)
	}
}
