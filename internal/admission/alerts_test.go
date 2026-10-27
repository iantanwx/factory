package admission

import "testing"

// C5: an alert names a run or an item only as a whole token of its dedupe
// key or its subject. A run also takes the checkout suffix, and an item
// does not.
func TestAnAlertNamesAnIDOnlyAsAWholeToken(t *testing.T) {
	const checkout = "/w/loop-factorio-implementer-factorio-5w6-fix-the-gate-r-1"
	for _, tc := range []struct {
		name             string
		key, subject, id string
		run, item, want  bool
	}{
		{"a run in a dedupe key", "run_liveness_unknown:r-1", "", "r-1", true, false, true},
		{"a run in a subject", "x", "implementer run r-1 on x finished blocked", "r-1", true, false, true},
		{"a run before a final full stop", "x", "stopped at r-1.", "r-1", true, false, true},
		{"a run's checkout with no item", "host_verb_from_checkout:/w/loop-factorio-implementer-r-1", "", "r-1", true, false, true},
		{"a run's checkout with its item", "checkout_kept:" + checkout, "", "r-1", true, false, true},
		{"a longer run id", "run_over_budget:r-12", "", "r-1", true, false, false},
		{"a checkout of a longer run id", "x", "/w/loop-factorio-implementer-factorio-5w6-fix-the-gate-r-12", "r-1", true, false, false},
		{"a run in neither field", "coordinator_start_failed", "the coordinator did not start", "r-1", true, false, false},
		{"an item in a dedupe key", "run_blocked:factorio/factorio-5w6/implementer/ab12", "", "factorio-5w6", false, true, true},
		{"an item in a subject", "x", "implementer stopped taking factorio-5w6 after 2 failed attempts", "factorio-5w6", false, true, true},
		{"a shorter item id", "run_blocked:factorio/factorio-5w6/implementer/ab12", "", "factorio-5w", false, true, false},
		{"an item inside a checkout", "x", checkout, "factorio-5w6", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.run {
				if got := AlertNamesRun(tc.key, tc.subject, tc.id); got != tc.want {
					t.Fatalf("AlertNamesRun(%q, %q, %q) = %v, want %v", tc.key, tc.subject, tc.id, got, tc.want)
				}
			}
			if tc.item {
				if got := AlertNamesItem(tc.key, tc.subject, tc.id); got != tc.want {
					t.Fatalf("AlertNamesItem(%q, %q, %q) = %v, want %v", tc.key, tc.subject, tc.id, got, tc.want)
				}
			}
		})
	}
}
