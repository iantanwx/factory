package gates

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Counts maps a finding key, such as "<file>::<rule>", to its count.
type Counts map[string]int

// readCounts reads a count baseline, {"allowed": {"<key>": <count>}}, at
// rel under root. An absent file reads as empty, and exists says which.
func readCounts(root, rel string) (c Counts, exists bool, err error) {
	var b struct {
		Allowed Counts `json:"allowed"`
	}
	exists, err = readBaseline(root, rel, &b)
	return b.Allowed, exists, err
}

// writeCounts writes c as a count baseline at rel under root.
func writeCounts(root, rel string, c Counts) error {
	if c == nil {
		c = Counts{}
	}
	return writeBaseline(root, rel, struct {
		Allowed Counts `json:"allowed"`
	}{c})
}

// lower returns baseline with each count cut to the found count and each key
// that found lacks deleted. It never raises a count and never adds a key.
func lower(found, baseline Counts) Counts {
	out := Counts{}
	for k, n := range baseline {
		if m := min(n, found[k]); m > 0 {
			out[k] = m
		}
	}
	return out
}

// updateCounts writes the count baseline that --update leaves at rel, and
// returns it: the found counts when no file exists, else lower(found,
// baseline).
func updateCounts(root, rel string, found, baseline Counts, exists bool) (Counts, error) {
	if exists {
		found = lower(found, baseline)
	}
	return found, writeCounts(root, rel, found)
}

// Advisory is one accepted advisory of an audit baseline. A valid entry has
// a reason that is not blank and a date that is a calendar day, YYYY-MM-DD.
type Advisory struct {
	Reason string `json:"reason"`
	Date   string `json:"date"`
}

// faults reports each way the entry of id in file is not valid.
func (a Advisory) faults(id, file string) []Finding {
	var findings []Finding
	if strings.TrimSpace(a.Reason) == "" {
		findings = append(findings, Finding{File: file, Rule: "no_reason", Message: id + " is accepted with no reason. Write why the project accepts it"})
	}
	if _, err := time.Parse(time.DateOnly, a.Date); err != nil {
		findings = append(findings, Finding{File: file, Rule: "bad_date", Message: fmt.Sprintf("%s is accepted on %q, which is no calendar day in the form YYYY-MM-DD", id, a.Date)})
	}
	return findings
}

// readAccepted reads an audit baseline, {"accepted": {"<id>": {"reason":
// "...", "date": "YYYY-MM-DD"}}}, at rel under root. An absent file reads as
// empty, and exists says which.
func readAccepted(root, rel string) (a map[string]Advisory, exists bool, err error) {
	var b struct {
		Accepted map[string]Advisory `json:"accepted"`
	}
	exists, err = readBaseline(root, rel, &b)
	return b.Accepted, exists, err
}

// writeAccepted writes a as an audit baseline at rel under root.
func writeAccepted(root, rel string, a map[string]Advisory) error {
	return writeBaseline(root, rel, struct {
		Accepted map[string]Advisory `json:"accepted"`
	}{a})
}

// readBaseline reads the JSON file at rel under root into v. An absent file
// leaves v as it is, and exists says which.
func readBaseline(root, rel string, v any) (exists bool, err error) {
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return true, fmt.Errorf("%s: %w", rel, err)
	}
	return true, nil
}

// writeBaseline writes v as JSON at rel under root: sorted keys, two-space
// indent and a final newline, so two writes of one set give the same bytes.
// It escapes no HTML, so a reason a person wrote keeps its bytes.
func writeBaseline(root, rel string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
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
