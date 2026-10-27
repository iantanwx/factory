package shape_test

import (
	"reflect"
	"testing"

	"github.com/iantanwx/loop/internal/shape"
)

// C1: an entry is decided only by a bold **Answered or **Deferred run.
func TestUndecidedReadsOnlyABoldAnswerOrDeferral(t *testing.T) {
	const head = "# Spec\n\n## Contracts\n\nC1.\n\n## Open questions\n\n"
	cases := []struct {
		name string
		spec string
		want []shape.Question
	}{
		{"None. holds no entry", head + "None.\n", nil},
		{"an empty section holds no entry", head + "\n", nil},
		{"a spec with no section holds no entry", "# Spec\n\n## Contracts\n\n- Which store?\n", nil},
		{
			"a plain entry is undecided",
			head + "- Which store holds the cache?\n",
			[]shape.Question{{Line: 9, Text: "Which store holds the cache?"}},
		},
		{
			"a numbered entry is undecided",
			head + "1. Which store holds the cache?\n",
			[]shape.Question{{Line: 9, Text: "Which store holds the cache?"}},
		},
		{"a bold Answered run decides", head + "- Which store? **Answered (operator, 2026-10-27):** Redis.\n", nil},
		{"a bold Deferred run decides", head + "- Which store?\n  **Deferred:** to the next epic.\n", nil},
		{"a nested item belongs to its entry", head + "- Which store?\n  - **Answered:** Redis.\n", nil},
		{
			"the plain word answered decides nothing",
			head + "- Which store? This was not answered in the PRD, and is deferred.\n",
			[]shape.Question{{Line: 9, Text: "Which store? This was not answered in the PRD, and is deferred."}},
		},
		{
			"Answered with no bold decides nothing",
			head + "- Which store?\n  Answered: Redis.\n",
			[]shape.Question{{Line: 9, Text: "Which store?"}},
		},
		{
			"an italic Answered decides nothing",
			head + "- Which store? *Answered: Redis.*\n",
			[]shape.Question{{Line: 9, Text: "Which store? *Answered: Redis.*"}},
		},
		{
			"a bold run in a code span decides nothing",
			head + "- Does `**Answered**` decide?\n",
			[]shape.Question{{Line: 9, Text: "Does `**Answered**` decide?"}},
		},
		{"a list item inside a fenced block is no entry", head + "```\n- Which store?\n```\n", nil},
		{
			"a bold Answered inside a fenced block decides nothing",
			head + "- Which store?\n\n  ```\n  **Answered:** Redis.\n  ```\n",
			[]shape.Question{{Line: 9, Text: "Which store?"}},
		},
		{
			"an answer under the next entry decides only that entry",
			head + "- Which store?\n- Which port? **Answered:** 8080.\n- Which region?\n",
			[]shape.Question{{Line: 9, Text: "Which store?"}, {Line: 11, Text: "Which region?"}},
		},
		{
			"an entry ends at a heading",
			head + "- Which store?\n\n### Notes\n\n**Answered:** Redis.\n",
			[]shape.Question{{Line: 9, Text: "Which store?"}},
		},
		{
			"an entry in another section is no question",
			"# Spec\n\n## Decisions\n\n- Which store?\n\n## Open questions\n\nNone.\n",
			nil,
		},
		{
			"the text is cut at 80 characters",
			head + "- " + "a123456789b123456789c123456789d123456789e123456789f123456789g123456789h123456789i123\n",
			[]shape.Question{{Line: 9, Text: "a123456789b123456789c123456789d123456789e123456789f123456789g123456789h123456789"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shape.Undecided(c.spec); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Undecided =\n%#v\nwant\n%#v", got, c.want)
			}
		})
	}
}
