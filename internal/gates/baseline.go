package gates

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Counts maps a finding key, such as "<file>::<rule>", to its count.
type Counts map[string]int

// readCounts reads a count baseline, {"allowed": {"<key>": <count>}}, at
// rel under root. An absent file reads as empty, and exists says which.
func readCounts(root, rel string) (c Counts, exists bool, err error) {
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var b struct {
		Allowed Counts `json:"allowed"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, true, fmt.Errorf("%s: %w", rel, err)
	}
	return b.Allowed, true, nil
}

// ratchet holds found to baseline in both directions. over names the rule of
// a count above the baseline; a count below is stale_baseline. file is the
// baseline's path and noun what it counts, for the messages.
func ratchet(found, baseline Counts, file, over, noun string) []Finding {
	var findings []Finding
	keys := make([]string, 0, len(found)+len(baseline))
	for k := range found {
		keys = append(keys, k)
	}
	for k := range baseline {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range slices.Compact(keys) {
		where, rule, _ := strings.Cut(key, "::")
		n, allowed := found[key], baseline[key]
		switch {
		case n > allowed:
			findings = append(findings, Finding{File: where, Rule: over, Message: fmt.Sprintf("%d %s of %s, and the baseline allows %d. Fix the code, or raise the count in %s with a reason", n, noun, rule, allowed, file)})
		case n < allowed:
			findings = append(findings, Finding{File: where, Rule: "stale_baseline", Message: fmt.Sprintf("the baseline allows %d %s of %s, and %d remain. Lower the count in %s", allowed, noun, rule, n, file)})
		}
	}
	return findings
}
