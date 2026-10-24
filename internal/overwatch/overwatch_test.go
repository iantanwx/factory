package overwatch

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"
)

// shortDeadline makes PartDeadline short for one test.
func shortDeadline(t *testing.T, d time.Duration) {
	t.Helper()
	old := partDeadline
	partDeadline = d
	t.Cleanup(func() { partDeadline = old })
}

// readingPart returns a fixed reading.
func readingPart(name string, r Reading) Part {
	return Part{Name: name, Command: "loop " + name, Read: func(context.Context, World) (Reading, error) { return r, nil }}
}

// The three ways a part fails.
var failures = []struct {
	kind string
	read func(ctx context.Context, w World) (Reading, error)
	says string
}{
	{"error", func(context.Context, World) (Reading, error) {
		return Reading{Findings: []Finding{{Key: "overwatch:x:y", Line: "hidden"}}}, errors.New("the store is locked")
	}, "the store is locked"},
	{"panic", func(context.Context, World) (Reading, error) { panic("index out of range") }, "index out of range"},
	{"deadline", func(ctx context.Context, w World) (Reading, error) {
		select {} // never returns, and never reads ctx
	}, "deadline"},
}

// C5, property: over generated sets of parts named from PartNames, one
// failing part of each kind costs only itself. Every other part's report
// equals its report from the same set with the failed part quiet.
func TestC5AFailedPartCostsOnlyItself(t *testing.T) {
	shortDeadline(t, 50*time.Millisecond)
	rng := rand.New(rand.NewPCG(5, 5))
	w := newFakeWorld(t)
	for round := range 12 {
		for _, f := range failures {
			names := slicesShuffle(rng, PartNames)[:2+rng.IntN(len(PartNames)-2)]
			failed := names[rng.IntN(len(names))]
			var failing, quiet []Part
			for i, n := range names {
				if n == failed {
					failing = append(failing, Part{Name: n, Read: f.read})
					quiet = append(quiet, readingPart(n, Reading{}))
					continue
				}
				p := readingPart(n, genReading(rng, n, i))
				failing, quiet = append(failing, p), append(quiet, p)
			}
			got := Sweep(context.Background(), w, failing)
			want := Sweep(context.Background(), w, quiet)
			if len(got.Parts) != len(names) {
				t.Fatalf("round %d %s: %d parts, want %d", round, f.kind, len(got.Parts), len(names))
			}
			for i, pr := range got.Parts {
				if pr.Name != want.Parts[i].Name {
					t.Fatalf("round %d %s: part %d is %s, want %s", round, f.kind, i, pr.Name, want.Parts[i].Name)
				}
				if pr.Name != failed {
					if !reflect.DeepEqual(pr, want.Parts[i]) {
						t.Fatalf("round %d %s: %s = %+v, want %+v", round, f.kind, pr.Name, pr, want.Parts[i])
					}
					continue
				}
				if pr.Code != Unread || len(pr.Lines) != 1 || !strings.HasPrefix(pr.Lines[0], "UNREAD") ||
					!strings.Contains(pr.Lines[0], f.says) {
					t.Fatalf("round %d %s: the failed part = %+v", round, f.kind, pr)
				}
			}
			if got.Code != Unread {
				t.Fatalf("round %d %s: report code %d, want 2", round, f.kind, got.Code)
			}
			assertOrder(t, got)
		}
	}
}

// A sweep with a slow part ends at that part's deadline: no part waits on
// another, and the records read runs beside them.
func TestSweepEndsAtTheDeadlineOfItsSlowestPart(t *testing.T) {
	shortDeadline(t, 100*time.Millisecond)
	w := newFakeWorld(t)
	var parts []Part
	for _, n := range PartNames {
		parts = append(parts, Part{Name: n, Read: failures[2].read})
	}
	start := time.Now()
	r := Sweep(context.Background(), w, parts)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("a sweep of %d slow parts took %s", len(parts), took)
	}
	for _, p := range r.Parts {
		if p.Code != Unread {
			t.Fatalf("%s = %+v", p.Name, p)
		}
	}
}

// The report holds one PartReport for each part given, in PartNames order,
// with its command, and its code is the highest of its parts.
func TestSweepOrdersPartsAndTakesTheHighestCode(t *testing.T) {
	w := newFakeWorld(t)
	r := Sweep(context.Background(), w, []Part{
		readingPart("server", Reading{Notes: []string{"every check passed"}}),
		readingPart("stalls", Reading{Findings: []Finding{{Key: "overwatch:stalls:a@b", Line: "a stalls (overwatch:stalls:a@b)"}},
			Notes: []string{"b waits on its run"}}),
		readingPart("bugs", Reading{}),
	})
	if r.Code != Work || !r.At.Equal(w.now) || r.Records != "" {
		t.Fatalf("report = %+v", r)
	}
	assertOrder(t, r)
	if r.Parts[0].Name != "stalls" || r.Parts[0].Command != "loop stalls" ||
		!reflect.DeepEqual(r.Parts[0].Lines, []string{"a stalls (overwatch:stalls:a@b)", "b waits on its run"}) {
		t.Fatalf("stalls = %+v", r.Parts[0])
	}
	if r.Parts[1].Name != "bugs" || r.Parts[1].Code != Quiet || r.Parts[1].Lines == nil {
		t.Fatalf("bugs = %+v; an empty part holds an empty list of lines", r.Parts[1])
	}
	if empty := Sweep(context.Background(), w, nil); empty.Code != Quiet || empty.Parts == nil || len(empty.Parts) != 0 {
		t.Fatalf("an empty sweep = %+v", empty)
	}
}

// Unfiled findings print first, then filed findings with their record,
// then notes.
func TestSweepPrintsUnfiledThenFiledThenNotes(t *testing.T) {
	w := newFakeWorld(t)
	w.fault(t, "filed: overwatch:stalls:filed", "open")
	r := Sweep(context.Background(), w, []Part{readingPart("stalls", Reading{
		Findings: []Finding{{Key: "overwatch:stalls:filed", Line: "one"}, {Key: "overwatch:stalls:open", Line: "two"}},
		Notes:    []string{"three"},
	})})
	lines := r.Parts[0].Lines
	if r.Parts[0].Code != Work || len(lines) != 3 || lines[0] != "two" || !strings.HasPrefix(lines[1], "one") ||
		!strings.Contains(lines[1], "F-1") || lines[2] != "three" {
		t.Fatalf("lines = %q", lines)
	}
}

func TestPartNamesHoldTheFifteenInTheSpecOrder(t *testing.T) {
	want := []string{"stalls", "questions", "decisions", "bugs", "backlog", "claims", "pulls", "worktrees", "main", "ci",
		"flaky", "audit", "timer-health", "inbox", "server"}
	if !reflect.DeepEqual(PartNames, want) {
		t.Fatalf("PartNames = %q", PartNames)
	}
	if len(Parts()) != 0 {
		t.Fatalf("Parts() = %d parts; no part has landed", len(Parts()))
	}
	if PartDeadline != 60*time.Second {
		t.Fatalf("PartDeadline = %s", PartDeadline)
	}
}

func assertOrder(t *testing.T, r Report) {
	t.Helper()
	last := -1
	for _, p := range r.Parts {
		i := indexOf(PartNames, p.Name)
		if i <= last {
			t.Fatalf("parts out of PartNames order: %+v", r.Parts)
		}
		last = i
	}
}

func indexOf(names []string, n string) int {
	for i, x := range names {
		if x == n {
			return i
		}
	}
	return -1
}

func slicesShuffle(rng *rand.Rand, in []string) []string {
	out := append([]string(nil), in...)
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// genReading makes a reading of zero to two findings and zero to two notes.
func genReading(rng *rand.Rand, name string, i int) Reading {
	var r Reading
	for k := range rng.IntN(3) {
		key := Key(name, fmt.Sprintf("s%d-%d", i, k))
		r.Findings = append(r.Findings, Finding{Key: key, Line: "finding " + key})
	}
	for k := range rng.IntN(3) {
		r.Notes = append(r.Notes, fmt.Sprintf("note %d of %s", k, name))
	}
	return r
}
