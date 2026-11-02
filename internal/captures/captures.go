// Package captures names the capture branch of a ticket and its
// manifest, and checks one set of captures against the states the ticket
// cites (factorio-4lso). The capture branch holds <head>/<state id>.png
// for each head the loop pushed, and screens.json at its root. It never
// merges, and the loop never deletes it. It does no I/O.
package captures

import (
	"bytes"
	"maps"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/design"
)

// Branch is the one capture branch of ticket.
func Branch(ticket string) string { return "loop/" + ticket + "-captures" }

// ManifestFile is the manifest's path at the root of the capture branch.
const ManifestFile = "screens.json"

// Manifest is what CI reads of the ticket, since CI has no server. The
// server writes it at each capture push from the ticket's screens fact.
type Manifest struct {
	Ticket string   `json:"ticket"`
	Epic   string   `json:"epic"`
	Record string   `json:"record"`
	States []string `json:"states"`
	// Accepted maps a state id to hundredths of a percent. Work entry 8
	// writes it, entry 9 reads it.
	Accepted map[string]int `json:"accepted,omitempty"`
}

// ManifestOf is the manifest of ticket, of epic, that cites states.
func ManifestOf(ticket, epic string, states []string) Manifest {
	return Manifest{Ticket: ticket, Epic: epic, Record: design.Dir(epic) + design.RecordFile, States: states}
}

// The rules of a capture set that Check refuses.
const (
	RuleMissing = "captures_missing" // a cited state has no capture
	RuleUnknown = "capture_unknown"  // a file names no cited state
	RuleNotPNG  = "capture_not_png"  // a file's bytes are not a PNG
)

// Fault is one refusal of a capture set: its rule, and each state or file
// that breaks it, sorted.
type Fault struct {
	Rule  string
	Names []string
}

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// Check checks files, keyed by file name, against the cited states. It
// answers nil for exactly one PNG named <state id>.png for each state,
// and nothing else. Otherwise it answers the first fault of an unknown
// file, a file that is not a PNG, and a missing state.
func Check(states []string, files map[string][]byte) *Fault {
	var unknown, notPNG []string
	for _, name := range slices.Sorted(maps.Keys(files)) {
		id, ok := strings.CutSuffix(name, ".png")
		switch {
		case !ok || !slices.Contains(states, id):
			unknown = append(unknown, name)
		case !bytes.HasPrefix(files[name], pngSignature):
			notPNG = append(notPNG, name)
		}
	}
	var missing []string
	for _, id := range states {
		if _, ok := files[id+".png"]; !ok {
			missing = append(missing, id)
		}
	}
	slices.Sort(missing)
	for _, f := range []Fault{{RuleUnknown, unknown}, {RuleNotPNG, notPNG}, {RuleMissing, missing}} {
		if len(f.Names) > 0 {
			return &f
		}
	}
	return nil
}
