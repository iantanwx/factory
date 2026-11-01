package pack_test

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/sched"
)

// forks names each forked file of the qa stage and the upstream URL
// prefixes its header comment must record.
var forks = map[string][]string{
	"skills/loop-verify-create/SKILL.md":   {"https://github.com/cursor/plugins/tree/main/pstack/skills/"},
	"skills/loop-verify-maintain/SKILL.md": {"https://github.com/cursor/plugins/tree/main/pstack/skills/"},
	"runbooks/qa.md": {
		"https://github.com/EveryInc/compound-engineering-plugin/",
		"https://github.com/slothware-labs/alpha/",
	},
}

var forkCommit = regexp.MustCompile(`^commit: +[0-9a-f]{40}$`)

// forkFaults returns one fault for each upstream prefix that the header
// comment of text does not record as an "upstream:" line whose next line is
// "commit:" and 40 lowercase hexadecimal characters.
func forkFaults(text string, upstreams []string) []string {
	comment, _, _ := strings.Cut(text, "-->")
	_, comment, ok := strings.Cut(comment, "<!--")
	if !ok {
		return []string{"no header comment"}
	}
	lines := strings.Split(comment, "\n")
	var faults []string
	for _, up := range upstreams {
		found := false
		for i, l := range lines {
			url, ok := strings.CutPrefix(strings.TrimSpace(l), "upstream:")
			if ok && strings.HasPrefix(strings.TrimSpace(url), up) &&
				i+1 < len(lines) && forkCommit.MatchString(strings.TrimSpace(lines[i+1])) {
				found = true
			}
		}
		if !found {
			faults = append(faults, "no upstream "+up+" with a commit")
		}
	}
	return faults
}

// C1: each forked file of the qa stage records its upstream URL and the
// commit it forks. A copy that drops one commit line fails.
func TestC1EachForkRecordsItsSource(t *testing.T) {
	for name, upstreams := range forks {
		text := read(t, name)
		if faults := forkFaults(text, upstreams); faults != nil {
			t.Errorf("%s: %v", name, faults)
		}
		lines := strings.Split(text, "\n")
		dropped := 0
		for i, l := range lines {
			if !strings.HasPrefix(strings.TrimSpace(l), "commit:") {
				continue
			}
			dropped++
			cut := strings.Join(slices.Delete(slices.Clone(lines), i, i+1), "\n")
			if forkFaults(cut, upstreams) == nil {
				t.Errorf("%s with line %d dropped passes the fork check", name, i+1)
			}
		}
		if dropped != len(upstreams) {
			t.Errorf("%s holds %d commit lines, want %d", name, dropped, len(upstreams))
		}
	}
}

// C2: the release pack ships the qa runbook as loop-qa and the two verify
// skills, and the implementer receives both skills.
func TestC2PackShipsTheQARunbookAndTheVerifySkills(t *testing.T) {
	tree, err := pack.Layout(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{pack.Injected(config.StageQA), "loop-verify-create", "loop-verify-maintain"} {
		text, err := fs.ReadFile(tree, dir+"/SKILL.md")
		if err != nil {
			t.Fatalf("the pack layout holds no %s: %v", dir, err)
		}
		if !strings.HasPrefix(string(text), "---\nname: "+dir+"\n") {
			t.Errorf("%s/SKILL.md does not name itself %s", dir, dir)
		}
	}
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	impl := m.Runbooks[config.StageImplementer].Skills
	for _, s := range []string{"loop-verify-create", "loop-verify-maintain"} {
		if !slices.Contains(impl, s) {
			t.Errorf("the implementer's skills %v do not hold %s", impl, s)
		}
	}
	if got := sched.DefaultEfforts[config.StageQA]; got != "high" {
		t.Errorf("DefaultEfforts[qa] = %q, want high", got)
	}
}

// C2: with entry 1 alone, a project config that does not list qa gets a qa
// firing that is not enabled, so the stage starts no run.
func TestC2QAStageStartsNoRunUnlessListed(t *testing.T) {
	pc, err := config.ParseProjectConfig([]byte("stages:\n  implementer: {}\n  code-reviewer: {}\nmerge:\n  mechanism: direct\n"))
	if err != nil {
		t.Fatal(err)
	}
	snap := &config.ProjectSnapshot{Config: pc, Manifest: &config.Manifest{}}
	hc := config.DefaultHost(t.TempDir())
	firings := sched.Firings(hc, []config.Project{{ID: "p"}}, func(string) *config.ProjectSnapshot { return snap }, sched.Jobs{})
	i := slices.IndexFunc(firings, func(f sched.Firing) bool { return f.Stage == config.StageQA })
	if i < 0 {
		t.Fatalf("firings %+v hold no qa firing", firings)
	}
	if want := (sched.Firing{Project: "p", Stage: "qa", Kind: sched.FiringRun, Enabled: false,
		Schedule: "2,7,12,17,22,27,32,37,42,47,52,57 * * * *"}); firings[i] != want {
		t.Fatalf("qa firing = %+v, want %+v", firings[i], want)
	}
}
