package sched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// briefFiles returns the brief.json of a started run as a map, and its
// brief.md.
func briefFiles(t *testing.T, st *Started) (map[string]any, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(st.Checkout, ".loop", "brief.json"))
	if err != nil {
		t.Fatal(err)
	}
	var b map[string]any
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	md, err := os.ReadFile(filepath.Join(st.Checkout, ".loop", "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	return b, string(md)
}

// withCodexLogin gives host config a Codex login beside Claude's.
func (w *world) withCodexLogin() {
	w.t.Helper()
	w.writeHost(nil, nil, func(hc *config.HostConfig) {
		hc.Harnesses = []config.HarnessLogin{{Name: "claude"}, {Name: "codex"}}
	})
	w.rebuild()
}

// C4 (the brief half) and C23: a doc-reviewer brief with doc_readers true
// holds the three readers, a Readers line that names them, and harness
// claude. With doc_readers false it holds an empty doc_readers and a line
// that says why the readers are off. An implementer brief holds no
// doc_readers field.
func TestC4DocReviewerBriefHoldsTheReadersOrWhyTheyAreOff(t *testing.T) {
	cases := []struct {
		name, setting string
		readers       []any
		line          string
	}{
		{"doc_readers true", "true", []any{"unslop", "asd-ste100", "content-lens"}, "- Readers: unslop, asd-ste100, content-lens\n"},
		{"doc_readers false", "false", []any{}, "- Readers: off (stages.doc-reviewer.doc_readers is false)\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.project.set(snapshot(t, "stages:\n  doc-reviewer:\n    doc_readers: "+c.setting+"\n  implementer: {}\nmerge:\n  mechanism: direct\n", ""))
			w.epic(machine.PRDReview, "")
			b, md := briefFiles(t, w.dispatch(machine.StageDocReviewer))
			got, ok := b["doc_readers"].([]any)
			if !ok || !slices.Equal(got, c.readers) {
				t.Fatalf("doc_readers = %#v, want %#v", b["doc_readers"], c.readers)
			}
			if !strings.Contains(md, c.line) {
				t.Fatalf("brief.md holds no line %q:\n%s", c.line, md)
			}
			if b["harness"] != "claude" {
				t.Fatalf("harness = %v", b["harness"])
			}
			w.ticket(machine.Todo)
			b, md = briefFiles(t, w.dispatch(machine.StageImplementer))
			if _, ok := b["doc_readers"]; ok || strings.Contains(md, "Readers:") {
				t.Fatalf("the implementer brief holds doc_readers %v, or a Readers line:\n%s", b["doc_readers"], md)
			}
		})
	}
}

// C23: with the readers in force and a Codex login on the host, the
// doc-reviewer takes the Claude Code profile and no other, and the plan
// lists that one. With doc_readers false the Codex profile follows as
// today.
func TestC23DocReviewerWithReadersTakesClaudeOnly(t *testing.T) {
	hc := &config.HostConfig{Harnesses: []config.HarnessLogin{{Name: "claude"}, {Name: "codex"}}}
	harnesses := func(ps []Profile) []agenthost.Harness {
		var out []agenthost.Harness
		for _, p := range ps {
			out = append(out, p.Harness)
		}
		return out
	}
	for setting, want := range map[string][]agenthost.Harness{
		"true":  {agenthost.Claude},
		"false": {agenthost.Claude, agenthost.Codex},
	} {
		pc, err := config.ParseProjectConfig([]byte("stages:\n  doc-reviewer:\n    doc_readers: " + setting + "\nmerge:\n  mechanism: direct\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := harnesses(profiles(config.StageDocReviewer, pc, hc)); !slices.Equal(got, want) {
			t.Errorf("doc_readers %s: doc-reviewer profiles = %v, want %v", setting, got, want)
		}
		if got := harnesses(profiles(config.StageImplementer, pc, hc)); !slices.Equal(got, []agenthost.Harness{agenthost.Claude, agenthost.Codex}) {
			t.Errorf("doc_readers %s: implementer profiles = %v", setting, got)
		}
	}

	w := newWorld(t)
	w.withCodexLogin()
	w.project.set(snapshot(t, "stages:\n  doc-reviewer:\n    doc_readers: true\nmerge:\n  mechanism: direct\n", ""))
	rows, err := w.s.StageList(context.Background(), prj)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Stage == config.StageDocReviewer {
			if got := harnesses(r.Profiles); !slices.Equal(got, []agenthost.Harness{agenthost.Claude}) {
				t.Fatalf("plan lists the doc-reviewer profiles %v", got)
			}
		}
	}
}

// C23: with the readers in force, a Claude Code harness that is paused or
// over QuotaLimit, and a Codex login present, the chooser starts no
// doc-reviewer run, and the refusal names why. With doc_readers false the
// run starts on Codex.
func TestC23DocReviewerWithReadersWaitsForClaude(t *testing.T) {
	spend := map[string]func(w *world){
		"paused": func(w *world) {
			w.s.pauseHarness(context.Background(), agenthost.Claude, "test", w.clock.Now().Add(time.Hour))
		},
		"over QuotaLimit": func(w *world) {
			w.host.SetQuota(agenthost.Quota{Harness: agenthost.Claude, OK: true, Windows: []agenthost.QuotaWindow{{Name: "weekly", UsedPercent: QuotaLimit + 1}}})
		},
	}
	for how, spendClaude := range spend {
		for setting, starts := range map[string]bool{"true": false, "false": true} {
			t.Run(how+"/doc_readers "+setting, func(t *testing.T) {
				w := newWorld(t)
				w.withCodexLogin()
				w.project.set(snapshot(t, "stages:\n  doc-reviewer:\n    doc_readers: "+setting+"\nmerge:\n  mechanism: direct\n", ""))
				w.epic(machine.PRDReview, "")
				spendClaude(w)
				st, ref := w.s.Dispatch(context.Background(), prj, machine.StageDocReviewer)
				if starts {
					if ref != nil || st == nil || st.Profile.Harness != agenthost.Codex {
						t.Fatalf("doc_readers false with claude %s: started %+v, refusal %+v", how, st, ref)
					}
					return
				}
				if st != nil || ref == nil || ref.Code != cli.CodeCapFull || !strings.Contains(ref.Hint, "claude") || strings.Contains(ref.Hint, "codex") {
					t.Fatalf("readers in force with claude %s: started %+v, refusal %+v", how, st, ref)
				}
				if n := len(w.host.Launches()); n != 0 {
					t.Fatalf("%d sessions started", n)
				}
			})
		}
	}
}
