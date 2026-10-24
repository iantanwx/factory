package sched

import (
	"regexp"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/machine"
)

// C-bug-1: the slug is short lowercase [a-z0-9-], and the run id keeps two
// checkouts of one item and stage apart.
func TestCheckoutNameSlugIsShortAndUnique(t *testing.T) {
	ad := admission.Admitted{Project: "factorio", Stage: machine.StageImplementer, Item: "factorio-kva", RunID: "r-1"}
	title := "Name the work in each session's Orca title, first message line and stage worktree\n\x1b[31m!"
	name := checkoutName(ad, title)
	prefix := "loop-factorio-implementer-factorio-kva-"
	slug, ok := strings.CutPrefix(name, prefix)
	if !ok {
		t.Fatalf("checkout name %q, want prefix %q", name, prefix)
	}
	slug, ok = strings.CutSuffix(slug, "-r-1")
	if !ok {
		t.Fatalf("checkout name %q does not end with the run id", name)
	}
	if !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(slug) || len(slug) > maxSlug {
		t.Errorf("slug %q is not short lowercase [a-z0-9-]", slug)
	}
	if !strings.HasPrefix(slug, "name-the-work") {
		t.Errorf("slug %q does not follow the title", slug)
	}
	ad.RunID = "r-2"
	if other := checkoutName(ad, title); other == name {
		t.Errorf("two runs of one item share the checkout name %q", name)
	}
	ad.Item = ""
	if got := checkoutName(ad, ""); got != "loop-factorio-implementer-r-2" {
		t.Errorf("a run with no item = %q", got)
	}
}

// C-bug-1: a checkout name fits the host's limit for every valid project
// id. The slug shortens first, then the item id goes; the run id stays, so
// the name stays unique.
func TestCheckoutNameFitsTheHostLimit(t *testing.T) {
	orcaName := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	title := "Name the work in each session's Orca title, first message line and stage worktree"
	for n := 1; n <= 63; n++ {
		project := strings.Repeat("p", n)
		ad := admission.Admitted{Project: project, Stage: machine.StageCodeReviewer, Item: project + "-kva", RunID: "r-f19f4d4df225"}
		name := checkoutName(ad, title)
		if !orcaName.MatchString(name) {
			t.Fatalf("project of %d bytes: checkout name %q (%d bytes) does not fit the host", n, name, len(name))
		}
		if !strings.HasSuffix(name, "-"+ad.RunID) {
			t.Errorf("project of %d bytes: checkout name %q does not end with the run id", n, name)
		}
		if head := "loop-" + project + "-" + ad.Stage + "-"; !strings.HasPrefix(name, head) {
			t.Errorf("project of %d bytes: checkout name %q, want prefix %q", n, name, head)
		}
	}
	// A short project keeps the item and a slug that starts the title.
	ad := admission.Admitted{Project: "factorio", Stage: machine.StageCodeReviewer, Item: "factorio-kva", RunID: "r-f19f4d4df225"}
	if name, want := checkoutName(ad, title), "loop-factorio-code-reviewer-factorio-kva-name-the-work"; !strings.HasPrefix(name, want) {
		t.Errorf("checkout name %q, want prefix %q", name, want)
	}
	// A project whose name leaves room for the item and no slug keeps the item.
	project := strings.Repeat("p", 44)
	ad = admission.Admitted{Project: project, Stage: machine.StageCodeReviewer, Item: project + "-kva", RunID: "r-f19f4d4df225"}
	if name, want := checkoutName(ad, title), "loop-"+project+"-code-reviewer-"+ad.Item+"-r-f19f4d4df225"; name != want {
		t.Errorf("checkout name %q, want %q", name, want)
	}
}
