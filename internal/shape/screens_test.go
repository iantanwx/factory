package shape_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/shape"
)

// source is a ticket body whose ## Source section holds lines.
func source(lines ...string) string {
	return "## Source\n\nWork entry 1.\n\n" + strings.Join(lines, "\n") + "\n\n## Change\n\nA trader sees widgets.\n"
}

// C2, C5: a line cites its ids in line order, or holds a none reason.
func TestScreensOfReadsTheIdsOrTheReason(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    shape.Screens
		present bool
	}{
		{"no line", source(), shape.Screens{}, false},
		{"ids in line order", source("**Screens:** S1.2, S1.1"), shape.Screens{States: []string{"S1.2", "S1.1"}}, true},
		{"one id and a full stop", source("**Screens:** S10.3."), shape.Screens{States: []string{"S10.3"}}, true},
		{"a none line with its reason", source("**Screens:** none. It adds the API route only."),
			shape.Screens{None: "It adds the API route only."}, true},
		{"a line outside ## Source is not read", source() + "\n## Files\n\n**Screens:** S1.1\n", shape.Screens{}, false},
		{"a line in a fenced block is not read", source("```", "**Screens:** S1.1", "```"), shape.Screens{}, false},
	}
	for _, c := range cases {
		got, present, faults := shape.ScreensOf(c.body)
		if len(faults) > 0 {
			t.Fatalf("%s: faults %+v", c.name, faults)
		}
		if present != c.present || !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s: got %+v present %v, want %+v present %v", c.name, got, present, c.want, c.present)
		}
	}
}

// C3, C4: a none with no reason, an id of another shape, an empty line and
// a second line are each a fault, and a faulty line is no Screens.
func TestScreensOfFaults(t *testing.T) {
	cases := []struct {
		name, body, rule, says string
	}{
		{"none", source("**Screens:** none"), "screens_no_reason", "no reason"},
		{"none and a full stop", source("**Screens:** none."), "screens_no_reason", "no reason"},
		{"an id of another shape", source("**Screens:** S1.1, S1"), "screens_bad_id", "S1"},
		{"a word for an id", source("**Screens:** the list"), "screens_bad_id", "the list"},
		{"an empty line", source("**Screens:**"), "screens_missing", "**Screens:**"},
		{"a second line", source("**Screens:** S1.1", "**Screens:** S1.2"), "screens_twice", "line 6"},
	}
	for _, c := range cases {
		got, present, faults := shape.ScreensOf(c.body)
		if !present {
			t.Fatalf("%s: the line is not present", c.name)
		}
		if len(faults) != 1 || faults[0].Rule != c.rule || !strings.Contains(faults[0].Msg, c.says) {
			t.Fatalf("%s: faults %+v, want one %s that says %q", c.name, faults, c.rule, c.says)
		}
		if !reflect.DeepEqual(got, shape.Screens{}) {
			t.Fatalf("%s: a faulty line gave %+v", c.name, got)
		}
	}
}
