package sched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/config"
)

// nightlyQA is an agent job of host config: a run that claims no item,
// once a night at 02:55.
func nightlyQA(c *config.HostConfig) {
	c.AgentJobs = map[string]config.AgentJob{"nightly-qa": {Cap: 1, Schedule: "55 2 * * *", Runbook: "docs/runbooks/nightly-qa.md"}}
}

// An agent job starts a run with no item on its own cron, in each project
// that lists it under stages. Its brief holds no item, and its prompt
// names the job's runbook in the repository. The job's cap holds a second
// run off while the first one lives, and a run that finishes done frees
// the slot for the next night.
func TestAgentJobStartsAnItemlessRunOnItsCron(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, nightlyQA)
	w.project.set(snapshot(t, "stages:\n  implementer: {}\n  nightly-qa: {}\nmerge:\n  mechanism: direct\n", ""))
	w.rebuild()

	w.clock.Set(at(t, "2026-10-21T01:00:00Z"))
	w.tick() // the first sight sets the deadline after now
	w.clock.Set(at(t, "2026-10-21T02:50:00Z"))
	w.tick()
	if n := len(w.host.Launches()); n != 0 {
		t.Fatalf("%d runs started before 02:55", n)
	}
	w.clock.Set(at(t, "2026-10-21T02:55:30Z"))
	w.tick()
	l := w.host.Launches()
	if len(l) != 1 {
		t.Fatalf("%d runs started at 02:55, want 1", len(l))
	}
	run := w.str(`SELECT run_id FROM runs WHERE stage = 'nightly-qa'`)
	if item := w.str(`SELECT item FROM runs WHERE run_id = ?`, run); item != "" {
		t.Fatalf("the agent job's run claimed %q", item)
	}
	checkout := w.str(`SELECT path FROM checkouts WHERE run_id = ?`, run)
	raw, err := os.ReadFile(filepath.Join(checkout, brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil || b.Item != nil || b.Stage != "nightly-qa" || b.Runbook != "docs/runbooks/nightly-qa.md" {
		t.Fatalf("brief = %s", raw)
	}
	prompt, err := os.ReadFile(l[0].Spec.PromptFile)
	if err != nil || !strings.Contains(string(prompt), "Then follow the runbook at docs/runbooks/nightly-qa.md.") {
		t.Fatalf("prompt = %q, %v", prompt, err)
	}

	if st, ref := w.s.Dispatch(context.Background(), prj, "nightly-qa"); st != nil || ref != nil {
		t.Fatalf("a second run past the cap of 1 = %+v, %+v", st, ref)
	}
	if res, ref := w.s.Finish(context.Background(), run, "done"); ref != nil || res.Class != "done" {
		t.Fatalf("finish = %+v, %+v", res, ref)
	}
	w.clock.Set(at(t, "2026-10-22T02:55:30Z"))
	w.tick()
	if n := len(w.host.Launches()); n != 2 {
		t.Fatalf("%d runs started by the second night, want 2", n)
	}
}

// A project that does not list the agent job under stages gets no run of
// it.
func TestAgentJobRunsOnlyWhereTheProjectListsIt(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, nightlyQA)
	w.rebuild()
	w.clock.Set(at(t, "2026-10-21T01:00:00Z"))
	w.tick()
	w.clock.Set(at(t, "2026-10-21T02:55:30Z"))
	w.tick()
	if n := len(w.host.Launches()); n != 0 {
		t.Fatalf("%d runs started in a project that does not list the job", n)
	}
}
