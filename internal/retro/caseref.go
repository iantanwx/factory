// Package retro records what goes wrong on an item, and learns from it
// once the item finishes (factorio-1xq0).
package retro

import (
	"fmt"
	"strings"
)

// CaseRef names one case in the record of an item, such as "run:r-1a2b",
// "round:factorio-x1y:2" or "transition:418". Two equal strings name one
// case.
type CaseRef string

// casePrefixes are the records a case can name.
var casePrefixes = []string{"run", "round", "transition", "escalation", "fault", "comment", "signal", "learning"}

// Parse reads a case reference. It refuses an unknown prefix and an empty
// name.
func Parse(s string) (CaseRef, error) {
	prefix, name, ok := strings.Cut(s, ":")
	if !ok || name == "" {
		return "", fmt.Errorf("retro: case %q is not <record>:<name>", s)
	}
	for _, p := range casePrefixes {
		if p == prefix {
			return CaseRef(s), nil
		}
	}
	return "", fmt.Errorf("retro: case %q names no known record; the records are %s", s, strings.Join(casePrefixes, ", "))
}
