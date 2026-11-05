package gates

import (
	"strings"
	"testing"

	docshape "github.com/iantanwx/loop/internal/shape"
)

// C-bug-1: the spec gate refuses an open question that the sign-off refuses.
func TestBugBwxoSpecGateRefusesAnUndecidedOpenQuestion(t *testing.T) {
	good := goodSpec(t)
	text := strings.Replace(good, "## Open questions\n\nNone.\n",
		"## Open questions\n\nNone.\n\n- Which cache key wins? This blocks entry 1.\n", 1)
	if len(docshape.Undecided(text)) != 1 {
		t.Fatalf("fixture: the sign-off must refuse one open question")
	}
	got := docshape.CheckSpec(specFile, text)
	at := lineOf(t, text, "- Which cache key wins?")
	for _, f := range got {
		if f.Line == at {
			return
		}
	}
	t.Fatalf("CheckSpec = %v; want a finding at line %d, the open question the sign-off refuses", got, at)
}
