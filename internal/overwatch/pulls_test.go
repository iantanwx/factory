package overwatch

import (
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// C23: pulls maps each open pull request's head branch with SpecEpicOf,
// else ItemOf. Two open pull requests with one item and one document are
// work, and the line names both numbers. Each case below that is not work
// reads code 0.
func TestC23TwoOpenPullsForOneItemAndOneDocumentAreWork(t *testing.T) {
	cases := []struct {
		name string
		prs  []gt.PR
		code Code
		key  string
		want []string
	}{
		{"one branch to two bases", []gt.PR{{Head: "loop/alpha-x1", Base: "main"}, {Head: "loop/alpha-x1", Base: "release"}},
			Work, "overwatch:pulls:alpha:alpha-x1:work", []string{"#1", "#2"}},
		{"a spec branch to two bases", []gt.PR{{Head: "loop/alpha-e1-spec", Base: "main"}, {Head: "loop/alpha-e1-spec", Base: "next"}},
			Work, "overwatch:pulls:alpha:alpha-e1:spec", []string{"#1", "#2"}},
		{"one pull request for each item", []gt.PR{{Head: "loop/alpha-x1"}, {Head: "loop/alpha-x2"}}, Quiet, "", nil},
		{"branches that map to no item", []gt.PR{{Head: "feature-x", Base: "main"}, {Head: "feature-x", Base: "release"},
			{Head: "loop/", Base: "main"}, {Head: "loop/a/b", Base: "main"}, {Head: "loop/a/b", Base: "next"}}, Quiet, "", nil},
		{"a fork's pull request", []gt.PR{{Head: "loop/alpha-x1"}, {Head: "loop/alpha-x1", Base: "release", Fork: true}}, Quiet, "", nil},
		{"an epic's PRD and spec at once", []gt.PR{{Head: "loop/factorio-a7r"}, {Head: "loop/factorio-a7r-spec"}}, Quiet, "", nil},
		{"a7r and a7, which no exact function joins", []gt.PR{{Head: "loop/factorio-a7r"}, {Head: "loop/factorio-a7"}}, Quiet, "", nil},
		{"one of the two closed", []gt.PR{{Head: "loop/alpha-x1"}, {Head: "loop/alpha-x1", Base: "release", State: "closed"}}, Quiet, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newGHWorld(t, alpha)
			for _, pr := range c.prs {
				w.prs().Add(&pr)
			}
			pr := sweepOne(t, w, partNamed(t, "pulls"))
			expect(t, pr, c.code, c.want...)
			if c.key != "" && unfiled(pr, c.key) != 1 {
				t.Fatalf("no line quotes %s: %v", c.key, pr.Lines)
			}
			if len(pr.Lines) == 0 {
				t.Fatal("a quiet project prints no line")
			}
		})
	}
}

// C23: a fault that names the key ends the work. The line then names the
// fault, with code 0.
func TestC23AFaultThatNamesThePullsKeyEndsTheWork(t *testing.T) {
	w := newGHWorld(t, alpha)
	w.prs().Add(&gt.PR{Head: "loop/alpha-x1", Base: "main"})
	w.prs().Add(&gt.PR{Head: "loop/alpha-x1", Base: "release"})
	id := w.fault(t, "two pull requests: overwatch:pulls:alpha:alpha-x1:work.", "open")
	pr := sweepOne(t, w, partNamed(t, "pulls"))
	expect(t, pr, Quiet, "overwatch:pulls:alpha:alpha-x1:work", "filed in "+id)
}
