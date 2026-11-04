package shape_test

import (
	"reflect"
	"testing"

	"github.com/iantanwx/loop/internal/shape"
)

func TestChecklistOwesEachContractDoneLineAndStopCondition(t *testing.T) {
	cases := []struct {
		name  string
		shape shape.Shape
		body  string
		want  []shape.Owed
	}{
		{
			"a ticket owes its contracts, D<n> for each Done means item and stop<n> for each stop condition",
			shape.Ticket,
			"## Contracts to prove\n\n- C1. One.\n- C2. Two.\n\n## Done means\n\n- The gates pass.\n- `make acceptance`\n  passes.\n\n" +
				"## Stop conditions\n\n1. The change spends money.\n\n## Out of scope\n\n- Nothing.\n",
			[]shape.Owed{{ID: "C1"}, {ID: "C2"}, {ID: "D1", Text: "The gates pass."}, {ID: "D2", Text: "`make acceptance` passes."},
				{ID: "stop1", Text: "The change spends money."}},
		},
		{
			"Done means with text and no list is one line, and a body with no stop conditions owes none",
			shape.Bug,
			"## Contract C-bug-1\n\nThe verb refuses.\n\n## Done means\n\nThe test passes.\nThe gates pass.\n",
			[]shape.Owed{{ID: "C-bug-1"}, {ID: "D1", Text: "The test passes. The gates pass."}},
		},
		{"an empty Done means owes no line", shape.Ticket, "## Contracts to prove\n\n- C1. One.\n\n## Done means\n\n", []shape.Owed{{ID: "C1"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shape.Checklist(c.shape, c.body); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Checklist = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestChecksReadsEachChecklistLineOfAReview(t *testing.T) {
	review := "Findings first.\n\n- C1 pass: the test fails at the base.\n* `D2` fail: no proof command.\n" +
		"3. stop1 waived: answer-12 waives it.\nC-bug-1 PASS - red at the base.\n- X1 fail: a stray log line.\n" +
		"- D1 pass\n- Note pass: not an id.\n```\n- C9 fail: inside a fence.\n```\n"
	want := []shape.Check{
		{ID: "C1", Result: shape.Pass, Evidence: "the test fails at the base."},
		{ID: "D2", Result: shape.Fail, Evidence: "no proof command."},
		{ID: "stop1", Result: shape.Waived, Evidence: "answer-12 waives it."},
		{ID: "C-bug-1", Result: shape.Pass, Evidence: "red at the base."},
		{ID: "X1", Result: shape.Fail, Evidence: "a stray log line."},
		{ID: "D1", Result: shape.Pass, Evidence: ""},
	}
	if got := shape.Checks(review); !reflect.DeepEqual(got, want) {
		t.Fatalf("Checks = %+v, want %+v", got, want)
	}
}
