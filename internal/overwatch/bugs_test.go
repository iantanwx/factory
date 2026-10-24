package overwatch

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// bugsWorld is the fake world with the project configs the bugs part reads.
type bugsWorld struct {
	*fakeWorld
	configs map[string]*config.ProjectConfig // project -> config; absent answers errFake
}

// newBugsWorld holds the loop's own project, "home", whose repository is
// the loop's and whose reproducer runs, next to the fake world's factorio
// and alpha.
func newBugsWorld(t *testing.T) *bugsWorld {
	t.Helper()
	w := &bugsWorld{fakeWorld: newFakeWorld(t), configs: map[string]*config.ProjectConfig{"home": reproducer(true)}}
	w.projects = append(w.projects, config.Project{ID: "home", Repo: loopRepo})
	return w
}

func (w *bugsWorld) ProjectConfig(_ context.Context, project string) (*config.ProjectSnapshot, error) {
	c, ok := w.configs[project]
	if !ok {
		return nil, errFake
	}
	return &config.ProjectSnapshot{Config: c}, nil
}

// reproducer is a project config whose reproducer stage runs when on.
func reproducer(on bool) *config.ProjectConfig {
	return &config.ProjectConfig{Stages: map[string]config.StageConfig{
		config.StageImplementer: {}, config.StageReproducer: {Enabled: &on},
	}}
}

// sweepBugs sweeps the bugs part alone, and returns its report.
func sweepBugs(t *testing.T, w World) PartReport {
	t.Helper()
	r := Sweep(context.Background(), w, []Part{bugsPart()})
	if len(r.Parts) != 1 || r.Parts[0].Name != "bugs" {
		t.Fatalf("parts = %+v", r.Parts)
	}
	return r.Parts[0]
}

// linesWith returns the lines that hold every word.
func linesWith(lines []string, words ...string) []string {
	var out []string
	for _, l := range lines {
		if !slices.ContainsFunc(words, func(w string) bool { return !slices.Contains(Tokens(l), w) }) {
			out = append(out, l)
		}
	}
	return out
}

// setFault moves fault id to status.
func setFault(t *testing.T, st *store.Store, id, status string) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE faults SET status = ? WHERE n = ?`, status, strings.TrimPrefix(id, "F-"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// C20: an open or acknowledged fault that no bug of the loop's project
// names is work, keyed by its id, and the line names the project to file
// it in.
func TestC20AnUnfiledLoopFaultIsWork(t *testing.T) {
	for _, status := range []string{"open", "acknowledged"} {
		t.Run(status, func(t *testing.T) {
			w := newBugsWorld(t)
			id := w.fault(t, "the reviewer ran at the wrong head", status)
			pr := sweepBugs(t, w)
			if pr.Code != Work {
				t.Fatalf("code = %d, want 1; lines %q", pr.Code, pr.Lines)
			}
			if got := linesWith(pr.Lines, "F-1", "home"); len(got) != 1 || id != "F-1" {
				t.Fatalf("lines = %q, want one line that names F-1 and the project home", pr.Lines)
			}
		})
	}
}

// C20: a bug of the loop's project, in any state, files the fault its body
// names, and so the fault is not work.
func TestC20AFaultThatABugNamesIsNotWork(t *testing.T) {
	for _, status := range []string{machine.Reported, machine.Reproducing, machine.Shaped, machine.Duplicate} {
		t.Run(status, func(t *testing.T) {
			w := newBugsWorld(t)
			w.fault(t, "the reviewer ran at the wrong head", "open")
			w.add("home", machine.Item{ID: "home-b1", Kind: machine.KindBug, Status: status}, "Loop fault F-1: the reviewer ran at the wrong head.")
			pr := sweepBugs(t, w)
			if pr.Code != Quiet {
				t.Fatalf("code = %d, want 0; lines %q", pr.Code, pr.Lines)
			}
			if got := linesWith(pr.Lines, "F-1", "home-b1"); len(got) != 1 {
				t.Fatalf("lines = %q, want one line that names F-1 and home-b1", pr.Lines)
			}
		})
	}
}

// C20: the fault id is a token, so a body that holds F-12 files F-12 and
// not F-1.
func TestC20F12FilesF12AndNotF1(t *testing.T) {
	w := newBugsWorld(t)
	for range 12 {
		w.fault(t, "a fault", "open")
	}
	w.add("home", machine.Item{ID: "home-b1", Kind: machine.KindBug, Status: machine.Reported}, "Files F-12.")
	pr := sweepBugs(t, w)
	if pr.Code != Work {
		t.Fatalf("code = %d, want 1; lines %q", pr.Code, pr.Lines)
	}
	if got := linesWith(pr.Lines, "F-12", "home-b1"); len(got) != 1 {
		t.Fatalf("lines = %q, want F-12 filed in home-b1", pr.Lines)
	}
	if got := linesWith(pr.Lines, "F-1", "home-b1"); len(got) != 0 {
		t.Fatalf("lines = %q, F-1 is filed by F-12", pr.Lines)
	}
	if got := linesWith(pr.Lines, "F-1", "home"); len(got) != 1 {
		t.Fatalf("lines = %q, want F-1 as work in home", pr.Lines)
	}
}

// C20: another fault's text that cites F-12 files nothing, and a bug in a
// project that is not the loop's files nothing either.
func TestC20AFaultThatCitesAnotherDoesNotFileIt(t *testing.T) {
	w := newBugsWorld(t)
	first := w.fault(t, "the reviewer ran at the wrong head", "open")
	w.fault(t, "same cause as "+first+".", "open")
	w.add("alpha", machine.Item{ID: "alpha-b1", Kind: machine.KindBug, Status: machine.Duplicate}, "Same as "+first+".")
	pr := sweepBugs(t, w)
	if pr.Code != Work {
		t.Fatalf("code = %d, want 1; lines %q", pr.Code, pr.Lines)
	}
	if got := linesWith(pr.Lines, first, "home"); len(got) != 1 {
		t.Fatalf("lines = %q, want %s as work in home", pr.Lines, first)
	}
}

// C20: a fault that is fixed, a duplicate or not a fault is never work.
func TestC20AResolvedFaultIsNeverWork(t *testing.T) {
	w := newBugsWorld(t)
	for _, status := range []string{"fixed", "duplicate", "not-a-fault"} {
		w.fault(t, "resolved", status)
	}
	pr := sweepBugs(t, w)
	if pr.Code != Quiet || len(pr.Lines) != 0 {
		t.Fatalf("code = %d, lines %q, want 0 and no line", pr.Code, pr.Lines)
	}
}

// C21: the loop's project is the one whose repository is the loop's, not
// the one whose id looks like it. A closed bug of factorio files nothing.
func TestC21ItsRepositoryNamesTheLoopsProject(t *testing.T) {
	w := newBugsWorld(t)
	w.fault(t, "a fault", "open")
	w.add("factorio", machine.Item{ID: "factorio-b1", Kind: machine.KindBug, Status: machine.Duplicate}, "F-1")
	pr := sweepBugs(t, w)
	if pr.Code != Work {
		t.Fatalf("code = %d, want 1; lines %q", pr.Code, pr.Lines)
	}
	if got := linesWith(pr.Lines, "F-1", "home"); len(got) != 1 {
		t.Fatalf("lines = %q, want F-1 as work in home", pr.Lines)
	}
}

// C21: with no project that holds the loop's repository, each open fault
// is work, one line says so, and `loop fault ack` ends the work.
func TestC21NoLoopProjectMakesEachOpenFaultWorkUntilItsAck(t *testing.T) {
	w := newBugsWorld(t)
	w.projects = w.projects[:2]
	id := w.fault(t, "a fault", "open")
	pr := sweepBugs(t, w)
	if pr.Code != Work || len(linesWith(pr.Lines, id)) != 1 {
		t.Fatalf("code = %d, lines %q, want 1 and one line for %s", pr.Code, pr.Lines, id)
	}
	if got := linesWith(pr.Lines, loopRepo); len(got) != 1 || !strings.Contains(got[0], "no registered project") {
		t.Fatalf("lines = %q, want one line that no registered project holds %s", pr.Lines, loopRepo)
	}

	setFault(t, w.st, id, "acknowledged")
	pr = sweepBugs(t, w)
	if pr.Code != Quiet || len(linesWith(pr.Lines, id)) != 0 {
		t.Fatalf("after the ack: code = %d, lines %q, want 0 and no line for %s", pr.Code, pr.Lines, id)
	}
}

// C22: a bug in reported in a project whose config disables the reproducer
// is work. With the reproducer on, stalls owns it.
func TestC22AReportedBugThatNothingTakesIsWork(t *testing.T) {
	key := Key("bugs", "alpha-b1")
	cases := []struct {
		name   string
		config *config.ProjectConfig
		status string
		want   Code
	}{
		{"the reproducer is off", reproducer(false), machine.Reported, Work},
		{"the project lists no reproducer", &config.ProjectConfig{Stages: map[string]config.StageConfig{config.StageImplementer: {}}}, machine.Reported, Work},
		{"the reproducer runs", reproducer(true), machine.Reported, Quiet},
		{"the bug left reported", reproducer(false), machine.Shaped, Quiet},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newBugsWorld(t)
			w.configs["alpha"] = c.config
			w.add("alpha", machine.Item{ID: "alpha-b1", Kind: machine.KindBug, Status: c.status}, "it broke")
			pr := sweepBugs(t, w)
			if pr.Code != c.want {
				t.Fatalf("code = %d, want %d; lines %q", pr.Code, c.want, pr.Lines)
			}
			found := len(linesWith(pr.Lines, key, "alpha")) == 1
			if found != (c.want == Work) {
				t.Fatalf("lines = %q, want a line that quotes %s: %v", pr.Lines, key, c.want == Work)
			}
		})
	}
}

// C22: a question that names the key ends the work.
func TestC22AQuestionThatNamesTheReportEndsIt(t *testing.T) {
	w := newBugsWorld(t)
	w.configs["alpha"] = reproducer(false)
	w.add("alpha", machine.Item{ID: "alpha-b1", Kind: machine.KindBug, Status: machine.Reported}, "it broke")
	w.question(t, "Enable the reproducer for "+Key("bugs", "alpha-b1")+"?", false)
	if pr := sweepBugs(t, w); pr.Code != Quiet {
		t.Fatalf("code = %d, want 0; lines %q", pr.Code, pr.Lines)
	}
}

// Parts ships the bugs part.
func TestPartsHoldsBugs(t *testing.T) {
	if !slices.ContainsFunc(Parts(), func(p Part) bool { return p.Name == "bugs" }) {
		t.Fatal("Parts() holds no bugs part")
	}
}
