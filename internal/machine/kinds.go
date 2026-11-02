// Package machine is the server's state machine for work items: one
// transition table for each item kind, the intent verbs that the tables
// check, the write order of KTD6 across SQLite and beads, and the start-up
// reconcile that finishes every write a crash interrupted.
//
// A table row names a verb, the states it starts from, the state it lands
// on, and the facts the write carries. No verb sets a raw state (R5). A
// fact that must change with a status changes in the same beads write
// (R12).
package machine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
)

// Kind is a work-item kind. Each kind has one table.
type Kind string

// The kinds of v1.
const (
	KindTicket   Kind = "ticket"
	KindEpic     Kind = "epic"
	KindDecision Kind = "decision"
	KindBug      Kind = "bug"
)

// Initial is the status bd gives every new bead. The create rows of each
// table start from it.
const Initial = "open"

// Special targets of a row.
const (
	// ToResume lands on the resume target that escalate stored (R12).
	ToResume = "@resume"
	// ToSame keeps the status: the write carries facts only.
	ToSame = "@same"
)

// Verbs that callers outside the machine apply.
const (
	VerbAcceptDifference   = "accept_difference" // epic: execution -> execution, the operator's acceptance of a design difference
	VerbADRLanded          = "adr_landed"
	VerbADRMisjoined       = "adr_misjoined"
	VerbADROff             = "adr_off"
	VerbADROpened          = "adr_opened"
	VerbADRSend            = "adr_send"
	VerbADRWithdrawn       = "adr_withdrawn"
	VerbAnswer             = "answer"
	VerbAwaitBuild         = "await_build"    // bug: reported, reproducing -> awaiting_build
	VerbBaseMerged         = "base_merged"    // ticket: queued -> queued at the server's merge of the base
	VerbBuildCanceled      = "build_canceled" // bug: awaiting_build -> reported (server)
	VerbBuildLanded        = "build_landed"   // bug: awaiting_build -> duplicate (server)
	VerbCIRed              = "ci_red"
	VerbCreate             = "create"
	VerbCreateArchitecture = "create_architecture"
	VerbCreatePRD          = "create_prd"
	VerbDecomposed         = "decomposed"
	VerbDefer              = "defer"
	VerbEnqueue            = "enqueue"
	VerbEscalate           = "escalate"
	VerbFile               = "file"
	VerbFire               = "fire"
	VerbNote               = "note"
	VerbPersonComment      = "person_comment"
	VerbPRClosed           = "pr_closed"
	VerbPRDSubmit          = "prd_submit"
	VerbQADue              = "qa_due"  // ticket: ready_to_merge, queued -> in_qa when the stage turns on (server)
	VerbQAFail             = "qa_fail" // ticket: in_qa -> todo, a reject by qa report
	VerbQAOff              = "qa_off"  // ticket: in_qa -> ready_to_merge when the stage turns off (server)
	VerbQAPass             = "qa_pass" // ticket: in_qa -> ready_to_merge by qa report
	VerbSignoffHead        = "signoff_head"
	VerbSimplified         = "simplified"   // ticket: in_simplify -> in_review at the simplify run's pr open
	VerbSimplifyOff        = "simplify_off" // ticket: in_simplify -> in_review when the project disables the stage (server)
	VerbSkipDesign         = "skip_design"
	VerbSupersede          = "supersede"
	VerbUnblock            = "unblock"
)

// Stages that claim an item. The config package owns the stage list, and
// the scheduler reads the claim rows for the states each stage takes.
const (
	StageImplementer          = config.StageImplementer
	StageCodeReviewer         = config.StageCodeReviewer
	StageDocReviewer          = config.StageDocReviewer
	StageReviser              = config.StageReviser
	StageUnattendedBrainstorm = config.StageUnattendedBrainstorm
	StagePlanner              = config.StagePlanner
	StageDecomposer           = config.StageDecomposer
	StageReproducer           = config.StageReproducer
	StageSimplify             = config.StageSimplify
	StageQA                   = config.StageQA
)

// Lease effects of a row.
const (
	LeaseTake = "take" // the write grants the lease to the claiming run
	LeaseDrop = "drop" // the write ends the lease
)

// State is one status of a kind.
type State struct {
	Name     string                `json:"name"`
	Category beadsx.StatusCategory `json:"category"`
	Terminal bool                  `json:"terminal,omitempty"`
}

// Row is one edge of a table. Two rows may share a verb and a state only
// when their When selectors differ: the first row whose selector holds
// applies.
type Row struct {
	Verb string   `json:"verb"`
	From []string `json:"from"`
	To   string   `json:"to"`
	// ResumeTo lists the states a ToResume row may land on.
	ResumeTo []string `json:"resume_to,omitempty"`
	// When names the selector that picks this row among rows with the same
	// verb and state.
	When string `json:"when,omitempty"`
	// Stage is the stage a claim row admits.
	Stage string `json:"stage,omitempty"`
	// Command is the CLI verb that performs the row. Empty means only the
	// server performs it, for example from the GitHub reactor.
	Command string `json:"command,omitempty"`
	// Facts lists the facts the write sets or clears, by name.
	Facts      []string `json:"facts,omitempty"`
	BumpsEpoch bool     `json:"bumps_epoch,omitempty"`
	Lease      string   `json:"lease,omitempty"`
	// Says is what the history records that the move means. Decision rows
	// carry one each, and no two say the same.
	Says string `json:"says,omitempty"`

	when  func(c *change) bool
	guard func(c *change) *cli.Refusal
	set   func(c *change) *cli.Refusal
}

// Table is the transition table of one kind. The schema prints it.
type Table struct {
	Kind     Kind    `json:"kind"`
	BeadType string  `json:"bead_type"`
	States   []State `json:"states"`
	Rows     []Row   `json:"rows"`
}

// state returns the named state.
func (t *Table) state(name string) (State, bool) {
	i := slices.IndexFunc(t.States, func(s State) bool { return s.Name == name })
	if i < 0 {
		return State{}, false
	}
	return t.States[i], true
}

// Terminal reports whether the kind ends in the named state.
func (t *Table) Terminal(name string) bool {
	s, ok := t.state(name)
	return ok && s.Terminal
}

// match returns the row for verb from the change's current status, or nil.
func (t *Table) match(verb string, c *change) *Row {
	for i := range t.Rows {
		r := &t.Rows[i]
		if r.Verb != verb || !slices.Contains(r.From, c.item.Status) {
			continue
		}
		if r.when != nil && !r.when(c) {
			continue
		}
		return r
	}
	return nil
}

// AllowedNext names every verb that has a row from status, in table order.
// A refusal carries it, so it names verbs and never states (KTD11).
func (t *Table) AllowedNext(status string) []string {
	out := []string{}
	for _, r := range t.Rows {
		if slices.Contains(r.From, status) && !slices.Contains(out, r.Verb) {
			out = append(out, r.Verb)
		}
	}
	return out
}

// Allows reports whether some row moves verb from status.
func (t *Table) Allows(verb, status string) bool {
	return slices.ContainsFunc(t.Rows, func(r Row) bool { return r.Verb == verb && slices.Contains(r.From, status) })
}

// tables holds every kind's table, in kind order. init fills it, because
// a guard reads it back.
var tables []*Table

// Tables returns every table. Callers must not change them.
func Tables() []*Table { return slices.Clone(tables) }

// TableFor returns the table of one kind.
func TableFor(k Kind) (*Table, bool) {
	i := slices.IndexFunc(tables, func(t *Table) bool { return t.Kind == k })
	if i < 0 {
		return nil, false
	}
	return tables[i], true
}

func kindOfType(beadType string) (*Table, bool) {
	i := slices.IndexFunc(tables, func(t *Table) bool { return t.BeadType == beadType })
	if i < 0 {
		return nil, false
	}
	return tables[i], true
}

// Statuses lists the custom statuses every project workspace configures:
// each state of each table that bd does not define itself.
func Statuses() []beadsx.StatusDef {
	var out []beadsx.StatusDef
	seen := map[string]beadsx.StatusCategory{}
	for _, t := range tables {
		for _, s := range t.States {
			if beadsx.IsBuiltinStatus(s.Name) {
				continue
			}
			if c, ok := seen[s.Name]; ok {
				if c != s.Category {
					panic(fmt.Sprintf("machine: state %s has two categories", s.Name))
				}
				continue
			}
			seen[s.Name] = s.Category
			out = append(out, beadsx.StatusDef{Name: s.Name, Category: s.Category})
		}
	}
	return out
}

func init() {
	tables = []*Table{ticketTable(), epicTable(), decisionTable(), bugTable()}
	for _, t := range tables {
		if err := t.check(); err != nil {
			panic(err)
		}
		cli.RegisterSurfaceTable(string(t.Kind), t)
	}
}

// check refuses a malformed table: a row from or to an unknown state, a
// row from a terminal state, or a ToResume row with no targets.
func (t *Table) check() error {
	for _, r := range t.Rows {
		for _, f := range r.From {
			s, ok := t.state(f)
			if f != Initial && !ok {
				return fmt.Errorf("machine: %s row %s starts from unknown state %q", t.Kind, r.Verb, f)
			}
			if s.Terminal {
				return fmt.Errorf("machine: %s row %s starts from terminal state %q", t.Kind, r.Verb, f)
			}
		}
		switch r.To {
		case ToSame:
		case ToResume:
			if len(r.ResumeTo) == 0 {
				return fmt.Errorf("machine: %s row %s resumes to no state", t.Kind, r.Verb)
			}
		default:
			if _, ok := t.state(r.To); !ok {
				return fmt.Errorf("machine: %s row %s lands on unknown state %q", t.Kind, r.Verb, r.To)
			}
		}
		if strings.TrimSpace(r.Verb) == "" {
			return fmt.Errorf("machine: %s has a row with no verb", t.Kind)
		}
	}
	return nil
}
