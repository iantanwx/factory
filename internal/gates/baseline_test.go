package gates

import (
	"maps"
	"slices"
	"testing"
)

// ratchet names a count above the baseline by the rule its caller passes,
// so a stack gate can report new_finding where standards reports
// new_suppression. A count below is stale_baseline for every caller.
func TestRatchetNamesTheRuleAboveTheBaseline(t *testing.T) {
	fs := ratchet(Counts{"a.ts::r": 3, "b.ts::r": 1}, Counts{"a.ts::r": 2, "b.ts::r": 2, "c.ts::r": 1}, "lint.json", "new_finding", "findings")
	got := []string{}
	for _, f := range fs {
		got = append(got, f.File+" "+f.Rule+": "+f.Message)
	}
	want := []string{
		"a.ts new_finding: 3 findings of r, and the baseline allows 2. Fix the code, or raise the count in lint.json with a reason",
		"b.ts stale_baseline: the baseline allows 2 findings of r, and 1 remain. Lower the count in lint.json",
		"c.ts stale_baseline: the baseline allows 1 findings of r, and 0 remain. Lower the count in lint.json",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("findings =\n%q\nwant\n%q", got, want)
	}
}

// An absent baseline reads as empty and says so, so a caller can tell a
// project that never adopted the ratchet from one that emptied it.
func TestReadCountsOfAnAbsentFile(t *testing.T) {
	c, exists, err := readCounts(t.TempDir(), "lint.json")
	if err != nil || exists || len(c) != 0 {
		t.Fatalf("readCounts = %v, %v, %v; want empty, absent, no error", c, exists, err)
	}
	dir := t.TempDir()
	write(t, dir, map[string]string{"lint.json": `{"allowed": {"a::r": 2}}`})
	c, exists, err = readCounts(dir, "lint.json")
	if err != nil || !exists || !maps.Equal(c, Counts{"a::r": 2}) {
		t.Fatalf("readCounts = %v, %v, %v; want {a::r: 2}, present, no error", c, exists, err)
	}
}

// C2: the standards gate reports the same rules and messages after the
// ratchet moved into this file. The fixture lives in testdata, so the
// gate's run on this repository counts none of it.
func TestStandardsRatchetMessagesAfterTheMove(t *testing.T) {
	fs := standards(t, map[string]string{"x/a.go": goFixture(t)}, map[string]int{"x/a.go::SA4006": 2})
	got := []string{}
	for _, f := range fs {
		got = append(got, f.File+" "+f.Rule+": "+f.Message)
	}
	want := []string{
		"x/a.go stale_baseline: the baseline allows 2 suppressions of SA4006, and 1 remain. Lower the count in .loop/suppressions.json",
		"x/a.go new_suppression: 1 suppressions of all, and the baseline allows 0. Fix the code, or raise the count in .loop/suppressions.json with a reason",
		"x/a.go new_suppression: 1 suppressions of errcheck, and the baseline allows 0. Fix the code, or raise the count in .loop/suppressions.json with a reason",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("findings =\n%q\nwant\n%q", got, want)
	}
}
