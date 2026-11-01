package pack_test

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/pack"
)

// simplifySources are the three upstream files the simplify runbook forks
// (docs/plans/factorio-1r94-simplify-stage-spec.md, C1).
var simplifySources = []string{"ce-simplify-code", "no-comments", "comment-sicko"}

var (
	forkUpstream = regexp.MustCompile(`^\s*upstream:\s+https://github\.com/(?:EveryInc/compound-engineering-plugin|cursor/plugins)/\S+$`)
	forkCommit   = regexp.MustCompile(`^\s*commit:\s+[0-9a-f]{40}$`)
)

// forkFaults names each source that the runbook's header comment does not
// record with an upstream URL and a commit in one block of lines.
func forkFaults(text string) []string {
	head, _, ok := strings.Cut(text, "-->")
	_, comment, open := strings.Cut(head, "<!--")
	if !ok || !open {
		return []string{"the runbook holds no header comment"}
	}
	var faults []string
	for _, src := range simplifySources {
		recorded := false
		for _, block := range strings.Split(comment, "\n\n") {
			var url, commit bool
			for _, line := range strings.Split(block, "\n") {
				url = url || forkUpstream.MatchString(line) && strings.Contains(line, "/"+src)
				commit = commit || forkCommit.MatchString(line)
			}
			recorded = recorded || url && commit
		}
		if !recorded {
			faults = append(faults, src+" has no upstream URL and commit")
		}
	}
	if !strings.Contains(comment, "Changed on purpose") {
		faults = append(faults, "the header comment names nothing this fork changed on purpose")
	}
	return faults
}

// C1: the header comment of the simplify runbook records each source's
// upstream URL and commit, and a copy that drops one commit fails.
func TestC1TheSimplifyRunbookRecordsEachSourcesFork(t *testing.T) {
	text := read(t, pack.RunbookSource("simplify"))
	if got := forkFaults(text); len(got) != 0 {
		t.Fatalf("fork faults = %v, want none", got)
	}
	commit := regexp.MustCompile(`(?m)^\s*commit:\s+[0-9a-f]{40}\n`)
	for i := range commit.FindAllStringIndex(text, -1) {
		n := -1
		dropped := commit.ReplaceAllStringFunc(text, func(m string) string {
			if n++; n == i {
				return ""
			}
			return m
		})
		if got := forkFaults(dropped); len(got) != 1 || !strings.HasSuffix(got[0], "has no upstream URL and commit") {
			t.Errorf("drop commit %d: fork faults = %v, want one source without its commit", i, got)
		}
	}
	if n := len(commit.FindAllString(text, -1)); n != len(simplifySources) {
		t.Fatalf("the header comment holds %d commits, want %d", n, len(simplifySources))
	}
}

// C2: the release pack ships the simplify runbook, the manifest names it,
// and a run receives it as loop-simplify.
func TestC2ThePackShipsTheSimplifyRunbook(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	rb, ok := m.Runbooks["simplify"]
	if !ok {
		t.Fatal("the manifest names no simplify runbook")
	}
	wantSkills := []string{"loop-sdlc-turn", "loop-sdlc-glossary", "loop-commit", "loop-least-code", "loop-reader-load", "loop-test-behavior-not-implementation"}
	if rb.Attended || rb.Policy != "code-reviewer" || !slices.Equal(rb.Skills, wantSkills) {
		t.Fatalf("simplify runbook entry = %+v, want unattended, policy code-reviewer, skills %v", rb, wantSkills)
	}
	if got := brief.Runbook(agenthost.Claude, "simplify"); got != ".claude/skills/loop-simplify/SKILL.md" {
		t.Fatalf("the brief names runbook %s", got)
	}
	tree, err := pack.Layout(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	got, err := fs.ReadFile(tree, pack.Injected("simplify")+"/SKILL.md")
	if err != nil {
		t.Fatalf("the laid-out pack holds no loop-simplify/SKILL.md: %v", err)
	}
	if string(got) != read(t, pack.RunbookSource("simplify")) {
		t.Fatal("loop-simplify/SKILL.md is not runbooks/simplify.md")
	}
}
