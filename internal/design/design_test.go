package design_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/design"
)

const pin = "0123456789abcdef0123456789abcdef01234567"

// record is a complete record of epic factorio-x, with one state for each
// kind of pixel mark.
const record = "# Show the fleet: design record\n\n" +
	"**Epic:** factorio-x\n" +
	"**PRD:** docs/prd/fleet.md\n" +
	"**Prototype:** `loop/factorio-x-prototype` at `" + pin + "`\n\n" +
	"## Screens\n\n" +
	"### S1. Fleet list\n\n" + // 9
	"**Stories:** US-01, US-03\n\n" + // 11
	"- **S1.1 Empty.** Picture `s1-1.png`, target `/fleet?empty`.\n" + // 13
	"- **S1.2 Full.** Picture `s1-2.png`, target `/fleet`, pixel.\n" +
	"- **S1.3 Sorted.** Picture `s1-3.png`, target `/fleet?sort=name`, pixel 1.5%.\n"

func TestParseReadsACompleteRecord(t *testing.T) {
	got, problems := design.Parse(design.Dir("factorio-x"), record)
	if len(problems) != 0 {
		t.Fatalf("problems: %+v", problems)
	}
	want := design.Record{
		Epic: "factorio-x",
		PRD:  "docs/prd/fleet.md",
		Pin:  design.Pin{Branch: "loop/factorio-x-prototype", Commit: pin},
		Screens: []design.Screen{{
			ID: "S1", Name: "Fleet list", Line: 9, Stories: []string{"US-01", "US-03"}, StoriesLine: 11,
			States: []design.State{
				{ID: "S1.1", Name: "Empty", Line: 13, Picture: "s1-1.png", Target: "/fleet?empty"},
				{ID: "S1.2", Name: "Full", Line: 14, Picture: "s1-2.png", Target: "/fleet", Pixel: &design.Pixel{Percent: 0.5, Default: true}},
				{ID: "S1.3", Name: "Sorted", Line: 15, Picture: "s1-3.png", Target: "/fleet?sort=name", Pixel: &design.Pixel{Percent: 1.5}},
			},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse =\n%+v\nwant\n%+v", got, want)
	}
}

// C5: a pixel mark with no number reads as the operator's default.
func TestParsePixelMarkWithNoNumberIsTheDefault(t *testing.T) {
	got, _ := design.Parse(design.Dir("factorio-x"), record)
	if p := got.Screens[0].States[1].Pixel; p == nil || p.Percent != 0.5 || !p.Default {
		t.Fatalf("pixel = %+v, want the default 0.5%%", p)
	}
}

func TestParseProblemsOfTheShape(t *testing.T) {
	cases := []struct {
		name, old, new, rule string
	}{
		{"no Epic line", "**Epic:** factorio-x\n", "", "record_shape"},
		{"an Epic that is not the directory", "**Epic:** factorio-x\n", "**Epic:** factorio-y\n", "record_shape"},
		{"no PRD line", "**PRD:** docs/prd/fleet.md\n", "", "record_shape"},
		{"no screen", record[strings.Index(record, "### S1."):], "", "record_shape"},
		{"a state with no target", ", target `/fleet?empty`", "", "record_shape"},
		{"a state of another screen", "**S1.2 Full.**", "**S2.1 Full.**", "record_shape"},
		{"a picture that is no PNG", "`s1-1.png`", "`s1-1.jpg`", "state_no_picture"},
		{"a picture outside the directory", "`s1-1.png`", "`../s1-1.png`", "state_no_picture"},
		{"a pin with no commit", " at `" + pin + "`", "", "pin_missing"},
	}
	for _, c := range cases {
		if !strings.Contains(record, c.old) {
			t.Fatalf("%s: the record holds no %q", c.name, c.old)
		}
		_, problems := design.Parse(design.Dir("factorio-x"), strings.Replace(record, c.old, c.new, 1))
		if len(problems) != 1 || problems[0].Rule != c.rule {
			t.Errorf("%s: problems = %+v, want one %s", c.name, problems, c.rule)
		}
	}
}
