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
	"unicode"
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

// EndLine is the last non-blank line of a ratcheted command's stdout. A
// silent or crashed tool prints no end line, so it never reads as clean.
const EndLine = "loop:end"

// maxKey is the longest key, in bytes, that parseKeys reads.
const maxKey = 512

// parseKeys reads the keys of a ratcheted command's stdout. ok is false when
// the end line is not the last non-blank line.
func parseKeys(stdout string) (keys []string, ok bool, bad []Finding) {
	lines := strings.Split(stdout, "\n")
	end := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == EndLine })
	if end < 0 || slices.ContainsFunc(lines[end+1:], func(l string) bool { return strings.TrimSpace(l) != "" }) {
		return nil, false, nil
	}
	for i, l := range lines[:end] {
		key := strings.TrimSpace(l)
		switch {
		case key == "":
		case len(key) > maxKey || strings.ContainsFunc(key, unicode.IsControl):
			bad = append(bad, Finding{Rule: "bad_key", Message: fmt.Sprintf("line %d of stdout is no key: a key is at most %d bytes and holds no control character", i+1, maxKey)})
		default:
			keys = append(keys, key)
		}
	}
	return keys, true, bad
}
