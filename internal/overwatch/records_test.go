package overwatch

import (
	"context"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/machine"
)

// keyPart is a fake part whose one finding holds key.
func keyPart(name, key string) Part {
	return Part{Name: name, Read: func(context.Context, World) (Reading, error) {
		return Reading{Findings: []Finding{{Key: key, Line: "the finding " + key}}}, nil
	}}
}

// sweepCode sweeps one part that finds key, and returns the part's code
// and lines.
func sweepCode(t *testing.T, w World, key string) (Code, []string) {
	t.Helper()
	r := Sweep(context.Background(), w, []Part{keyPart("stalls", key)})
	if len(r.Parts) != 1 {
		t.Fatalf("parts = %+v", r.Parts)
	}
	return r.Parts[0].Code, r.Parts[0].Lines
}

// C31: each open record kind names a key that one of its tokens equals,
// and the line names the record.
func TestC31AFindingThatAnOpenRecordNamesIsNotWork(t *testing.T) {
	key := Key("stalls", "factorio-x1y@in_progress")
	cases := []struct {
		name   string
		setup  func(t *testing.T, w *fakeWorld)
		record string
	}{
		{"a reported bug in another project", func(t *testing.T, w *fakeWorld) {
			w.add("alpha", machine.Item{ID: "alpha-b1", Kind: machine.KindBug, Status: machine.Reported}, "seen at "+key+".")
		}, "alpha-b1"},
		{"a pending decision, by its title", func(t *testing.T, w *fakeWorld) {
			w.add("factorio", machine.Item{ID: "factorio-d1", Kind: machine.KindDecision, Status: machine.DecisionPending,
				Title: "wait on " + key}, "no key here")
		}, "factorio-d1"},
		{"a decision in needs_human, by its body", func(t *testing.T, w *fakeWorld) {
			w.add("factorio", machine.Item{ID: "factorio-d2", Kind: machine.KindDecision, Status: machine.NeedsHuman}, "("+key+")")
		}, "factorio-d2"},
		{"a shaped bug whose ticket is open", func(t *testing.T, w *fakeWorld) {
			w.add("factorio", machine.Item{ID: "factorio-t1", Kind: machine.KindTicket, Status: "in_progress"}, "")
			w.add("factorio", machine.Item{ID: "factorio-b2", Kind: machine.KindBug, Status: machine.Shaped,
				Facts: map[string]string{machine.FactTicket: "factorio-t1"}}, key)
		}, "factorio-b2"},
		{"an unanswered question", func(t *testing.T, w *fakeWorld) {
			w.question(t, "Options for "+key+": fix it, or wait.", false)
		}, "question 1"},
		{"an open fault", func(t *testing.T, w *fakeWorld) { w.fault(t, "stalled: "+key, "open") }, "F-1"},
		{"an acknowledged fault", func(t *testing.T, w *fakeWorld) { w.fault(t, key+":", "acknowledged") }, "F-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newFakeWorld(t)
			c.setup(t, w)
			code, lines := sweepCode(t, w, key)
			if code != Quiet {
				t.Fatalf("code = %d, want 0; lines %q", code, lines)
			}
			if len(lines) != 1 || !strings.Contains(lines[0], key) || !strings.Contains(lines[0], c.record) {
				t.Fatalf("lines = %q, want one line that names %s", lines, c.record)
			}
		})
	}
}

// C31: these records do not name a key.
func TestC31AClosedRecordOrAPrefixDoesNotName(t *testing.T) {
	key := Key("main", "factorio", "ci-yml@1a2b3c4d5e6f")
	cases := []struct {
		name  string
		setup func(t *testing.T, w *fakeWorld)
	}{
		{"a duplicate bug", func(t *testing.T, w *fakeWorld) {
			w.add("factorio", machine.Item{ID: "factorio-b1", Kind: machine.KindBug, Status: machine.Duplicate}, key)
		}},
		{"a canceled bug", func(t *testing.T, w *fakeWorld) {
			w.add("factorio", machine.Item{ID: "factorio-b1", Kind: machine.KindBug, Status: machine.Canceled}, key)
		}},
		{"a shaped bug whose ticket is done", func(t *testing.T, w *fakeWorld) {
			w.add("factorio", machine.Item{ID: "factorio-t1", Kind: machine.KindTicket, Status: machine.Done}, "")
			w.add("factorio", machine.Item{ID: "factorio-b2", Kind: machine.KindBug, Status: machine.Shaped,
				Facts: map[string]string{machine.FactTicket: "factorio-t1"}}, key)
		}},
		{"a settled decision", func(t *testing.T, w *fakeWorld) {
			w.add("factorio", machine.Item{ID: "factorio-d1", Kind: machine.KindDecision, Status: machine.Done, Title: key}, key)
		}},
		{"an answered question", func(t *testing.T, w *fakeWorld) { w.question(t, key, true) }},
		{"a fixed fault", func(t *testing.T, w *fakeWorld) { w.fault(t, key, "fixed") }},
		{"a token that holds the key as a prefix", func(t *testing.T, w *fakeWorld) {
			w.add("factorio", machine.Item{ID: "factorio-b1", Kind: machine.KindBug, Status: machine.Reported}, key+"x and "+key+"/1")
		}},
		{"a ticket's body", func(t *testing.T, w *fakeWorld) {
			w.add("factorio", machine.Item{ID: "factorio-t1", Kind: machine.KindTicket, Status: "in_progress"}, key)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newFakeWorld(t)
			c.setup(t, w)
			if code, lines := sweepCode(t, w, key); code != Work || len(lines) != 1 || lines[0] != "the finding "+key {
				t.Fatalf("code %d, lines %q; want 1 and the finding's own line", code, lines)
			}
		})
	}
}

// C31: a fault's text names keys that start overwatch:, and never a fault
// id, so a fault that cites another files nothing.
func TestC31AFaultTextNamesNoFaultID(t *testing.T) {
	w := newFakeWorld(t)
	w.fault(t, "this repeats F-2 and stalls at "+Key("stalls", "x"), "open")
	r, err := ReadRecords(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if rec, ok := r.Names("F-2"); ok {
		t.Fatalf("a fault's text filed F-2 in %s", rec)
	}
	if rec, ok := r.Names(Key("stalls", "x")); !ok || rec != "F-1" {
		t.Fatalf("Names = %q %v, want F-1", rec, ok)
	}
}

// C31: a bug body that holds F-12 names F-12 and not F-1.
func TestC31TokensKeepF1ApartFromF12(t *testing.T) {
	w := newFakeWorld(t)
	w.add("factorio", machine.Item{ID: "factorio-b1", Kind: machine.KindBug, Status: machine.Reported}, "Files F-12.")
	r, err := ReadRecords(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Names("F-1"); ok {
		t.Fatal("F-12 named F-1")
	}
	if rec, ok := r.Names("F-12"); !ok || rec != "factorio-b1" {
		t.Fatalf("Names(F-12) = %q %v", rec, ok)
	}
}

// C31: when the records read fails, every finding is work, records holds
// the reason, and the report has code 2.
func TestC31AFailedRecordsReadMakesEveryFindingWork(t *testing.T) {
	w := newFakeWorld(t)
	key := Key("stalls", "factorio-x1y@in_progress")
	w.fault(t, key, "open")
	w.itemsErr = errFake
	r := Sweep(context.Background(), w, []Part{keyPart("stalls", key), {Name: "inbox", Read: func(context.Context, World) (Reading, error) {
		return Reading{Notes: []string{"a live session"}}, nil
	}}})
	if r.Code != Unread || r.Records == "" || !strings.Contains(r.Records, errFake.Error()) {
		t.Fatalf("report code %d records %q; want 2 and the reason", r.Code, r.Records)
	}
	if r.Parts[0].Code != Work || r.Parts[0].Lines[0] != "the finding "+key {
		t.Fatalf("stalls = %+v; want its finding as work", r.Parts[0])
	}
	if r.Parts[1].Code != Quiet {
		t.Fatalf("inbox = %+v; a part with notes only stays quiet", r.Parts[1])
	}
}

// C31, property: for generated keys and bodies, a body names a key exactly
// when the key stands in it between characters that a key cannot hold,
// followed by any run of "." and ":".
func TestC31TokensProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(31, 31))
	alphabet := "abcXYZ019._/@:-"
	seps := []string{" ", "\n", "(", ")", ",", "`", "\"", "'", ";", "!", "?", "[", "]"}
	tails := []string{"", ".", ":", "..", ".:"}
	for range 2000 {
		var b strings.Builder
		for n := 1 + rng.IntN(12); n > 0; n-- {
			b.WriteByte(alphabet[rng.IntN(len(alphabet))])
		}
		key := Key("flaky", b.String())
		before := seps[rng.IntN(len(seps))]
		after := seps[rng.IntN(len(seps))]
		tail := tails[rng.IntN(len(tails))]
		body := "prefix" + before + key + tail + after + "suffix"
		if !slices.Contains(Tokens(body), key) {
			t.Fatalf("Tokens(%q) = %q lacks %q", body, Tokens(body), key)
		}
		// A key with one more key character after it is another token.
		longer := before + key + "x" + after
		if slices.Contains(Tokens(longer), key) {
			t.Fatalf("Tokens(%q) holds the prefix %q", longer, key)
		}
		for _, tok := range Tokens(body) {
			if tok == "" || strings.HasSuffix(tok, ".") || strings.HasSuffix(tok, ":") {
				t.Fatalf("Tokens(%q) gave %q", body, tok)
			}
		}
	}
}

// Key's grammar: letters, digits and ._/@:-, never ending in . or :.
func TestKeyAndSlug(t *testing.T) {
	if got := Key("main", "factorio", "ci-yml@1a2b3c4d5e6f"); got != "overwatch:main:factorio:ci-yml@1a2b3c4d5e6f" {
		t.Fatalf("Key = %q", got)
	}
	if got := Slug("Build (ubuntu, Go 1.26)"); got != "build-ubuntu-go-1-26" {
		t.Fatalf("Slug = %q", got)
	}
	if got := Slug("ci.yml"); got != "ci-yml" {
		t.Fatalf("Slug = %q", got)
	}
	if got := Key("stalls", "a b.", "c:"); got != "overwatch:stalls:a-b.:c" {
		t.Fatalf("Key = %q", got)
	}
}

// C7 and spec Work 3: a sweep of no part reads no record, so it holds code
// 0 even when the records read would fail, and the job cannot ring with no
// part.
func TestASweepOfNoPartReadsNoRecord(t *testing.T) {
	w := newFakeWorld(t)
	w.itemsErr = errFake
	r := Sweep(context.Background(), w, nil)
	if r.Code != Quiet || r.Records != "" || r.Parts == nil || len(r.Parts) != 0 {
		t.Fatalf("a sweep of no part = %+v", r)
	}
	if text, ok := RingText(r); ok {
		t.Fatalf("a sweep of no part rings %q", text)
	}
}
