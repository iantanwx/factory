//go:build acceptance

package scenarios

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/machine"
)

// tableOnly names each ticket or epic row that no CLI verb, reactor path
// or scheduler path performs in v1, with the unit test that applies it.
// Every other row of the two tables must be applied end to end by a
// scenario of a full run.
var tableOnly = map[string]string{}

// rowName names one row: its kind, verb and selector, and its edge.
func rowName(k machine.Kind, r machine.Row) string {
	v := r.Verb
	if r.When != "" {
		v += "[" + r.When + "]"
	}
	return fmt.Sprintf("%s %s %s -> %s", k, v, strings.Join(r.From, ","), r.To)
}

// covers reports whether an applied transition performed row r.
func covers(r machine.Row, t acceptance.Transition) bool {
	if t.Verb != r.Verb || !slices.Contains(r.From, t.From) {
		return false
	}
	switch r.To {
	case machine.ToSame:
		return t.To == t.From
	case machine.ToResume:
		return slices.Contains(r.ResumeTo, t.To)
	}
	return t.To == r.To
}

// checkCatalogue holds the run to the row catalogue. A row of the ticket
// or epic table that is in neither tableOnly nor the applied history of a
// full run fails it, and so does a stage kind no scenario started. A run
// with -run checks the table's names only.
func checkCatalogue() int {
	code := 0
	full := flag.Lookup("test.run").Value.String() == ""
	var rows, applied int
	for _, tb := range machine.Tables() {
		if tb.Kind != machine.KindTicket && tb.Kind != machine.KindEpic {
			continue
		}
		for _, r := range tb.Rows {
			rows++
			name := rowName(tb.Kind, r)
			if _, ok := tableOnly[string(tb.Kind)+" "+r.Verb]; ok {
				continue
			}
			hit := slices.ContainsFunc(observed, func(t acceptance.Transition) bool {
				return t.Kind == string(tb.Kind) && covers(r, t)
			})
			if hit {
				applied++
			} else if full {
				fmt.Fprintf(os.Stderr, "catalogue: no scenario applied %s\n", name)
				code = 1
			}
		}
	}
	if full {
		for _, s := range checkStages() {
			fmt.Fprintf(os.Stderr, "catalogue: no scenario started a %s\n", s)
			code = 1
		}
		fmt.Printf("catalogue: %d of %d ticket and epic rows applied end to end, %d table-only\n", applied, rows, len(tableOnly))
	}
	return code
}
