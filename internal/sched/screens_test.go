package sched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/machine"
)

// screensEpic lands a design record on the base, and makes an epic in
// execution whose record merged, with an acceptance of S1.1, S2.1 and
// S1.2, in that order. S1.1 holds the default pixel mark, S1.2 none and
// S2.1 a mark of 0.125%.
func (w *world) screensEpic() string {
	w.t.Helper()
	accepted, err := json.Marshal([]machine.Acceptance{
		{State: "S1.1", Words: "The empty list may show a hint.", At: "2026-11-01T10:00:00Z"},
		{State: "S2.1", Words: "The dialog may sit higher.", At: "2026-11-01T11:00:00Z"},
		{State: "S1.2", Words: "The rows may be denser.", At: "2026-11-01T12:00:00Z"},
	})
	if err != nil {
		w.t.Fatal(err)
	}
	epic := w.item("epic", machine.Execution, nil)
	path := "docs/design/" + epic + "/record.md"
	w.landRecord(path, "# Widgets design\n\n**Epic:** "+epic+"\n**PRD:** `docs/prd/widgets.md`\n"+
		"**Prototype:** `loop/"+epic+"-prototype` at `"+strings.Repeat("a", 40)+"`\n\n"+
		"### S1. Widget list\n\n**Stories:** US-01\n\n"+
		"- **S1.1 Empty.** Picture `s1-1.png`, target `/widgets?empty`, pixel.\n"+
		"- **S1.2 Full.** Picture `s1-2.png`, target `/widgets`.\n\n"+
		"### S2. Widget dialog\n\n**Stories:** US-01\n\n"+
		"- **S2.1 Open.** Picture `s2-1.png`, target `/widgets?dialog`, pixel 0.125%.\n")
	if _, err := w.bx.Update(context.Background(), epic, beadsx.UpdateSpec{IfStatus: machine.Execution, SetMetadata: map[string]string{
		"loop." + machine.FactDesignPath: path, "loop." + machine.FactDesignMerged: "d1",
		"loop." + machine.FactAcceptedDifferences: string(accepted),
	}}); err != nil {
		w.t.Fatal(err)
	}
	return epic
}

// landRecord pushes the record at path to the base, and the project clone
// fetches it, so every new checkout holds it.
func (w *world) landRecord(path, text string) {
	w.t.Helper()
	clone := filepath.Join(w.t.TempDir(), "design")
	gitIn(w.t, w.dir, "clone", "-q", w.origin, clone)
	if err := os.MkdirAll(filepath.Join(clone, filepath.Dir(path)), 0o755); err != nil {
		w.t.Fatal(err)
	}
	commitIn(w.t, clone, path, text, "docs: the design record")
	gitIn(w.t, clone, "push", "-q", "origin", "main")
	gitIn(w.t, w.repo, "fetch", "-q", "origin")
}

// briefOf reads the brief and the rendered brief of a started run.
func briefOf(t *testing.T, st *Started) (brief.Brief, string) {
	t.Helper()
	b, _, _ := held(t, st)
	md, err := os.ReadFile(filepath.Join(st.Checkout, brief.BriefMD))
	if err != nil {
		t.Fatal(err)
	}
	return b, string(md)
}

// C18: the brief of an implementer or a code-reviewer run on a ticket
// with screens S1.1,S1.2 holds the record path, the capture branch, both
// states with the picture, target and mark of the record at the base, and
// each acceptance of S1.1 or S1.2, and no acceptance of S2.1. A second
// ticket that cites S1.2 gets the same acceptance of S1.2. A ticket with
// no screens gets no screens object.
func TestC18BriefNamesEachCitedStateAndEachAcceptanceOfIt(t *testing.T) {
	w := newWorld(t)
	epic := w.screensEpic()
	dir := "docs/design/" + epic + "/"
	s11 := brief.ScreenState{ID: "S1.1", Picture: dir + "s1-1.png", Target: "/widgets?empty", Pixel: "0.5%"}
	s12 := brief.ScreenState{ID: "S1.2", Picture: dir + "s1-2.png", Target: "/widgets"}
	a11 := brief.Acceptance{State: "S1.1", Words: "The empty list may show a hint.", At: "2026-11-01T10:00:00Z"}
	a12 := brief.Acceptance{State: "S1.2", Words: "The rows may be denser.", At: "2026-11-01T12:00:00Z"}

	for _, c := range []struct {
		name, status, stage, screens string
		want                         *brief.Screens
	}{
		{"an implementer run on a ticket that cites S1.1 and S1.2", machine.Todo, machine.StageImplementer, "S1.1,S1.2",
			&brief.Screens{Record: dir + "record.md", States: []brief.ScreenState{s11, s12}, Accepted: []brief.Acceptance{a11, a12}}},
		{"a code-reviewer run on a ticket that cites S1.1 and S1.2", machine.InReview, machine.StageCodeReviewer, "S1.1,S1.2",
			&brief.Screens{Record: dir + "record.md", States: []brief.ScreenState{s11, s12}, Accepted: []brief.Acceptance{a11, a12}}},
		{"a second ticket that cites S1.2", machine.Todo, machine.StageImplementer, "S1.2",
			&brief.Screens{Record: dir + "record.md", States: []brief.ScreenState{s12}, Accepted: []brief.Acceptance{a12}}},
		{"a ticket that cites no state", machine.Todo, machine.StageImplementer, "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			facts := map[string]string{machine.FactEpic: epic}
			if c.screens != "" {
				facts[machine.FactScreens] = c.screens
			}
			id := w.item("task", c.status, facts)
			st := w.dispatch(c.stage)
			if st.Item != id {
				t.Fatalf("dispatch started %s, want %s", st.Item, id)
			}
			b, md := briefOf(t, st)
			want := c.want
			if want != nil {
				want.Branch = "loop/" + id + "-captures"
			}
			if !reflect.DeepEqual(b.Screens, want) {
				got, _ := json.Marshal(b.Screens)
				exp, _ := json.Marshal(want)
				t.Fatalf("brief screens\n%s\nwant\n%s", got, exp)
			}
			if want == nil {
				if strings.Contains(md, "## Screens") {
					t.Fatalf("brief.md of a ticket that cites no state holds a screens section:\n%s", md)
				}
				return
			}
			for _, s := range append([]string{want.Record, want.Branch}, wantedText(want)...) {
				if !strings.Contains(md, s) {
					t.Errorf("brief.md lacks %q:\n%s", s, md)
				}
			}
			if strings.Contains(md, "The dialog may sit higher.") {
				t.Errorf("brief.md names the acceptance of S2.1, which the ticket does not cite:\n%s", md)
			}
		})
	}
}

// wantedText is each state id, picture, target and acceptance's words of s.
func wantedText(s *brief.Screens) []string {
	var out []string
	for _, st := range s.States {
		out = append(out, st.ID, st.Picture, st.Target)
	}
	for _, a := range s.Accepted {
		out = append(out, a.Words)
	}
	return out
}
