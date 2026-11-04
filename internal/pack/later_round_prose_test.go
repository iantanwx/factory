package pack_test

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
)

// factorio-a26c: a prose finding rejects alone in round 1 only. A later
// round names it as not blocking, and it rejects no text that an earlier
// round's proposed edit wrote below level 4 of loop-sdlc-triage.
func TestLaterDocReviewRoundRejectsNoProseAlone(t *testing.T) {
	for _, c := range []struct {
		name, prefix string
		wants        []string
	}{
		{reviewSkill, "6.", []string{
			"round 1",
			"not blocking",
			"proposed edit",
			"level 4",
			"`../loop-sdlc-triage/SKILL.md`",
		}},
		{"skills/loop-doc-readers/SKILL.md", "4.", []string{
			"round 1",
			"not blocking",
			"proposed edit",
		}},
	} {
		s := pack.Section(read(t, c.name), 2, func(title string) bool { return strings.HasPrefix(title, c.prefix) })
		if s == "" {
			t.Fatalf("%s holds no section %q", c.name, c.prefix)
		}
		s = strings.Join(strings.Fields(s), " ")
		holdsAll(t, c.name+" § "+c.prefix, s, c.wants)
	}
}
