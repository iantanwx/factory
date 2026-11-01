package machine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
)

type edge struct{ from, verb, to string }

func (e edge) String() string { return e.from + " -" + e.verb + "-> " + e.to }

// edgesOf expands every row of a table into its edges.
func edgesOf(t *Table) map[edge]bool {
	out := map[edge]bool{}
	for _, r := range t.Rows {
		for _, f := range r.From {
			switch r.To {
			case ToSame:
				out[edge{f, r.Verb, f}] = true
			case ToResume:
				for _, to := range r.ResumeTo {
					out[edge{f, r.Verb, to}] = true
				}
			default:
				out[edge{f, r.Verb, r.To}] = true
			}
		}
	}
	return out
}

func each(from []string, verb, to string) []edge {
	var out []edge
	for _, f := range from {
		e := edge{f, verb, to}
		if to == ToSame {
			e.to = f
		}
		out = append(out, e)
	}
	return out
}

func sameEdges(t *testing.T, kind Kind, want []edge) {
	t.Helper()
	tbl, _ := TableFor(kind)
	got := edgesOf(tbl)
	w := map[edge]bool{}
	for _, e := range want {
		w[e] = true
	}
	var missing, extra []string
	for e := range w {
		if !got[e] {
			missing = append(missing, e.String())
		}
	}
	for e := range got {
		if !w[e] {
			extra = append(extra, e.String())
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing)+len(extra) > 0 {
		t.Fatalf("%s table differs from the plan:\nmissing: %v\nextra: %v", kind, missing, extra)
	}
}

var ticketLive = []string{Backlog, Todo, InProgress, InReview, Repair, ReadyToMerge, Queued, Escalated}

// The ticket diagram of the plan, edge by edge, then the edges its text
// adds, then the edges this unit adds with the reason for each.
func ticketEdges() []edge {
	e := []edge{
		{Initial, "create", Backlog}, {Initial, "create", Todo},
		{Backlog, "unblock", Todo},
		{Todo, "claim", InProgress},
		{InProgress, "pr_open", InReview},
		{InProgress, "release", Todo}, {InProgress, "reclaim", Todo}, // finish failed, or crash
		{InReview, "ci_red", Repair},
		{Repair, "fix_pushed", InReview},
		{InReview, "review_pass", ReadyToMerge},
		{InReview, "review_reject", Todo},
		{ReadyToMerge, "head_changed", InReview},
		{ReadyToMerge, "enqueue", Queued},
		{Queued, "head_changed", InReview},
		// Added (F-66): the server merged a moved base into the reviewed
		// head, and the ticket stays queued at the merge commit.
		{Queued, "base_merged", Queued},
		{Queued, "dequeue_conflict", Todo},
		{Queued, "dequeue_red", Repair},
		{Queued, "merged", Done},
		{InReview, "pr_closed", Escalated},
		{Escalated, "answer", Todo},
		{Escalated, "cancel", Canceled},
	}
	// Text: escalate moves any non-terminal state to escalated, and an
	// observed merge moves any non-terminal state to done.
	e = append(e, each(except(ticketLive, Escalated), "escalate", Escalated)...)
	e = append(e, each(ticketLive, "merged", Done)...)
	// Added: a code reviewer and a repairing implementer hold a lease on a
	// state they do not leave (KTD7 claims at dispatch), and settle it.
	for _, s := range []string{Repair, InReview} {
		e = append(e, edge{s, "claim", s}, edge{s, "release", s}, edge{s, "reclaim", s})
	}
	// Added: a PR closed unmerged from any state that holds a PR (R8).
	e = append(e, each([]string{Repair, ReadyToMerge, Queued}, "pr_closed", Escalated)...)
	// Added: a note changes no status.
	e = append(e, each(ticketLive, "note", ToSame)...)
	// Added: work edit replaces the task's text and changes no status (R10).
	e = append(e, each(ticketLive, "edit", ToSame)...)
	// Added: a ticket that the base, a done item or an ADR already answers
	// closes from each state that holds no pull request (H5, alpha
	// 4fc932ebd).
	e = append(e, each([]string{Backlog, Todo, InProgress}, "supersede", Canceled)...)
	// Added: a ticket escalated from backlog resumes to backlog (ADR 0005
	// D2, spec factorio-9wh Decision 7).
	e = append(e, edge{Escalated, "answer", Backlog})
	// Added: work block parks a todo ticket, or adds a blocker to a backlog
	// one, and work unblock --by keeps backlog while an open blocker
	// remains (spec factorio-9wh Decisions 5 and 23).
	e = append(e, edge{Todo, "block", Backlog}, edge{Backlog, "block", Backlog}, edge{Backlog, "unblock", Backlog})
	return e
}

var epicLive = []string{Backlog, PRDNeeded, PRDDrafting, PRDReview, PRDAmending, PRDSignoff, Architecture, "adr_review", "design",
	"design_review", "design_signoff", SpecNeeded, SpecDrafting, SpecReview, SpecSignoff, SpecAmending, SpecApproved, Decomposing, Execution,
	Escalated}

func epicEdges() []edge {
	e := []edge{
		{Initial, "prd_submit", PRDReview},
		{Initial, "create_architecture", Architecture},
		{PRDReview, "review_pass", PRDSignoff},
		// factorio-4n0: an epic enters by its blockers, and unblock lands
		// it on the entry its blocked create recorded.
		{Initial, "create_prd", PRDNeeded}, {Initial, "create_prd", Backlog},
		{Initial, "create_architecture", Backlog},
		{Backlog, "unblock", PRDNeeded}, {Backlog, "unblock", Architecture},
		// factorio-4n0: only an unattended-brainstorm run claims a PRD
		// epic, and its settlement returns the epic to the queue.
		{PRDNeeded, "claim", PRDDrafting},
		{PRDDrafting, "release", PRDNeeded}, {PRDDrafting, "reclaim", PRDNeeded},
		// factorio-4n0: the unattended-brainstorm run submits the PRD it wrote.
		{PRDDrafting, "prd_submit", PRDReview},
		{PRDReview, "review_reject", PRDAmending},
		{PRDAmending, "prd_amended", PRDReview},
		{PRDReview, "review_reject", Escalated},
		{PRDSignoff, "doc_merged", SpecNeeded},
		{Architecture, "adr_landed", SpecNeeded},
		{SpecNeeded, "adr_misjoined", Architecture},
		{SpecNeeded, "claim", SpecDrafting},
		{SpecDrafting, "pr_open", SpecReview},
		{SpecReview, "review_pass", SpecSignoff},
		{SpecReview, "review_reject", SpecNeeded},
		{SpecSignoff, "doc_merged", SpecApproved},
		{SpecApproved, "claim", Decomposing},
		{Decomposing, "decomposed", Execution},
		{Execution, "complete", Done},
		{Escalated, "answer", PRDReview},
		// Text: a document merged in review counts as sign-off.
		{PRDReview, "doc_merged", PRDSignoff},
		{SpecReview, "doc_merged", SpecSignoff},
		// Added (1.12): a person's unanswered comment on a document in
		// review or sign-off goes to the reviser. A revision returns to
		// review, and a reply on a signed-off document that changed
		// nothing returns to sign-off. A merge while amending is the
		// sign-off.
		{PRDReview, "person_comment", PRDAmending},
		{PRDSignoff, "person_comment", PRDAmending},
		{SpecReview, "person_comment", SpecAmending},
		{SpecSignoff, "person_comment", SpecAmending},
		{PRDAmending, "prd_amended", PRDSignoff},
		{SpecAmending, "spec_amended", SpecReview},
		{SpecAmending, "spec_amended", SpecSignoff},
		{PRDAmending, "doc_merged", PRDSignoff},
		{SpecAmending, "doc_merged", SpecSignoff},
		// factorio-8qm: the server pins a moved document head, and the
		// document stays in sign-off.
		{PRDSignoff, "signoff_head", PRDSignoff},
		{SpecSignoff, "signoff_head", SpecSignoff},
		// factorio-ku99: the operator's send marks a PRD epic for the ADR
		// stage, and the PRD merge routes a marked epic there. In the spec
		// stage the send moves the epic at once.
		{PRDSignoff, "doc_merged", Architecture},
		// factorio-ku99: the doc-reviewer reviews each ADR pull request of
		// an epic in architecture once at each head. A verdict or a close
		// returns the epic to architecture, and the merge moves it on.
		{Architecture, "adr_opened", "adr_review"},
		{"adr_review", "review_pass", Architecture},
		{"adr_review", "review_reject", Architecture},
		{"adr_review", "adr_withdrawn", Architecture},
		{"adr_review", "adr_landed", SpecNeeded},
		// factorio-a4yq: the PRD merge of an epic with a UI story, and the
		// ADR merge of one with no design outcome, land on design. An epic
		// in design that an ADR named only in prose returns to
		// architecture.
		{PRDSignoff, "doc_merged", "design"},
		{Architecture, "adr_landed", "design"},
		{"adr_review", "adr_landed", "design"},
		{"design", "adr_misjoined", Architecture},
		// The operator's skip of the design stage.
		{"design", "skip_design", SpecNeeded},
	}
	e = append(e, each([]string{PRDNeeded, PRDDrafting, PRDReview, PRDAmending, PRDSignoff}, "adr_send", ToSame)...)
	e = append(e, each([]string{SpecNeeded, SpecReview, SpecSignoff, SpecAmending}, "adr_send", Architecture)...)
	e = append(e, edge{PRDSignoff, "adr_off", PRDSignoff})
	// Text: a closed document PR escalates and never cancels.
	e = append(e, each([]string{PRDReview, PRDAmending, PRDSignoff, SpecReview, SpecSignoff, SpecAmending}, "pr_closed", Escalated)...)
	e = append(e, each(except(epicLive, Escalated), "escalate", Escalated)...)
	// Added (F-55): an epic whose work moved elsewhere cancels before it
	// has children.
	e = append(e, each(except(epicLive, Decomposing, Execution), "cancel", Canceled)...)
	// Added: answer returns to every resume target escalate can store.
	for _, to := range uniqueValues(epicResume) {
		if to != PRDReview {
			e = append(e, edge{Escalated, "answer", to})
		}
	}
	// Added: the stages that claim without a move, and settlement of
	// every claim (RESUME_TARGETS in scripts/sdlc/handoff.ts, alpha).
	for _, s := range []string{PRDReview, PRDAmending, "adr_review", SpecReview, SpecAmending} {
		e = append(e, edge{s, "claim", s}, edge{s, "release", s}, edge{s, "reclaim", s})
	}
	e = append(e, edge{SpecDrafting, "release", SpecNeeded}, edge{SpecDrafting, "reclaim", SpecNeeded},
		edge{Decomposing, "release", SpecApproved}, edge{Decomposing, "reclaim", SpecApproved})
	e = append(e, each(epicLive, "note", ToSame)...)
	e = append(e, each(epicLive, "edit", ToSame)...)
	return e
}

// DECISION_MOVES (scripts/sdlc/decision-states.ts:95, alpha), unchanged.
func decisionEdges() []edge {
	return []edge{
		{Initial, "file", DecisionPending},
		{DecisionPending, "fire", NeedsHuman},
		{NeedsHuman, "defer", DecisionPending},
		{NeedsHuman, "answer", Done},
		{NeedsHuman, "drop", Canceled},
		{DecisionPending, "moot", Canceled},
		{NeedsHuman, "moot", Canceled},
	}
}

func bugEdges() []edge {
	return []edge{
		{Initial, "file", Reported},
		{Reported, "claim", Reproducing},
		{Reproducing, "release", Reported}, {Reproducing, "reclaim", Reported},
		{Reported, "shape", Shaped}, {Reproducing, "shape", Shaped},
		{Reported, "duplicate", Duplicate}, {Reproducing, "duplicate", Duplicate},
		// A report that a build answers waits for it. The build's end routes
		// it, and the coordinator may still route it by hand (D9).
		{Reported, "await_build", "awaiting_build"}, {Reproducing, "await_build", "awaiting_build"},
		{"awaiting_build", "build_landed", Duplicate}, {"awaiting_build", "build_canceled", Reported},
		{"awaiting_build", "shape", Shaped}, {"awaiting_build", "duplicate", Duplicate},
		{"awaiting_build", "note", "awaiting_build"}, {"awaiting_build", "edit", "awaiting_build"},
		{Reported, "escalate", Escalated}, {Reproducing, "escalate", Escalated},
		{Escalated, "answer", Reported},
		{Reproducing, "ask", Asked}, {Asked, "answer", Reported},
		{Escalated, "cancel", Canceled}, {Reproducing, "cancel", Canceled},
		{Reported, "note", Reported}, {Reproducing, "note", Reproducing}, {Asked, "note", Asked}, {Escalated, "note", Escalated},
		{Reported, "edit", Reported}, {Reproducing, "edit", Reproducing}, {Asked, "edit", Asked}, {Escalated, "edit", Escalated},
	}
}

func TestTablesHoldExactlyThePlannedEdges(t *testing.T) {
	sameEdges(t, KindTicket, ticketEdges())
	sameEdges(t, KindEpic, epicEdges())
	sameEdges(t, KindDecision, decisionEdges())
	sameEdges(t, KindBug, bugEdges())
}

// setupFor is the parameters and facts that let one row apply.
func setupFor(r Row, to string) (map[string]string, map[string]string) {
	params := map[string]string{"head": "h1", "body": "a note", "run": "r-1", "ticket": "tk-x", "of": "tk-y", "sha": "s1",
		"evidence": EvidenceCommit, "ref": "abc1234", "reason": "main holds it", "why": "the trigger fired", "note": "no question left",
		"answer": "yes", "by": "tk-z"}
	facts := map[string]string{FactReviewedHead: "h1"}
	if r.Stage != "" {
		params["stage"] = r.Stage
	}
	if r.When == "open_dependency" {
		params["blocked"] = "true"
	}
	if r.When == "open_blocker" {
		params["open"] = "true"
	}
	if r.Verb == VerbUnblock && r.To != Todo {
		facts[FactEpicEntry] = r.To // the epic's blocked create recorded it
	}
	if r.Stage == StagePlanner {
		facts[FactPRDMerged] = "m1" // the operator signed the PRD off
	}
	if r.When == "adr_requested" {
		facts[FactADRSent] = "the operator's words"
	}
	if r.When == "ui_stories" {
		params["ui_stories"] = "US-01" // the PRD holds a UI story
	}
	if r.When == "design_due" {
		facts[FactUIStories] = "US-01" // the PRD merge recorded a UI story
	}
	if r.Verb == VerbADRSend || r.Verb == VerbADROff || r.Verb == VerbSkipDesign {
		params["request"] = "the operator's words"
	}
	if r.When == "second_reject" {
		facts[FactPRDRejects] = "1"
	}
	if r.Verb == VerbPRDSubmit {
		params["pr"], params["path"] = "7", "docs/prd/x.md" // a run's submit records both
	}
	if r.Verb == VerbBaseMerged {
		params["from"], params["head"] = "h1", "h2" // the server merged the base into the reviewed head
	}
	if r.Verb == "adr_opened" {
		params["pr"] = "12" // the ADR pull request under review
	}
	if r.Verb == VerbADRLanded {
		params["path"], params["added"] = "docs/adr/0001-x.md", "a1" // the commit that added the ADR
	}
	if r.Verb == VerbADRMisjoined {
		params["path"] = "docs/adr/0001-x.md"
		facts[FactADR] = "docs/adr/0001-x.md"
	}
	if r.To == ToResume {
		facts[FactResume] = to
	}
	if r.When == "unchanged" {
		facts[FactAmendFrom], facts[FactAmendHead] = r.To, "h1" // a reply on a signed-off document
	}
	return params, facts
}

// TestEveryEdgeAppliesAndEveryOtherPairRefuses is AE3 over every table:
// each edge lands where the table says, and each other pair of state and
// verb refuses with TRANSITION_NOT_ALLOWED, names allowed_next, and
// changes nothing.
func TestEveryEdgeAppliesAndEveryOtherPairRefuses(t *testing.T) {
	for _, tbl := range Tables() {
		t.Run(string(tbl.Kind), func(t *testing.T) {
			w := newWorld(t)
			ctx := context.Background()
			edges := edgesOf(tbl)
			for _, r := range tbl.Rows {
				for _, from := range r.From {
					tos := []string{r.To}
					if r.To == ToResume {
						tos = r.ResumeTo
					}
					for _, to := range tos {
						if to == ToSame {
							to = from
						}
						params, facts := setupFor(r, to)
						var id string
						if from == Initial {
							var res cli.Result
							id, res = w.m.Create(ctx, CreateRequest{Project: prj, Kind: tbl.Kind, Title: "x", Verb: r.Verb, Params: params, Actor: Server("test")})
							mustOK(t, res)
						} else {
							id = w.create(tbl.Kind, firstCreate(tbl), nil)
							w.force(id, from, facts)
							mustOK(t, w.apply(id, r.Verb, params))
						}
						if got := w.show(id).Status; got != to {
							t.Fatalf("%s: landed on %s", edge{from, r.Verb, to}, got)
						}
					}
				}
			}
			verbs := map[string]bool{}
			for _, r := range tbl.Rows {
				verbs[r.Verb] = true
			}
			for _, s := range tbl.States {
				for _, v := range slices.Sorted(maps.Keys(verbs)) {
					if slices.ContainsFunc(slices.Collect(maps.Keys(edges)), func(e edge) bool { return e.from == s.Name && e.verb == v }) {
						continue
					}
					id := w.create(tbl.Kind, firstCreate(tbl), nil)
					w.force(id, s.Name, map[string]string{FactReviewedHead: "h1", FactResume: s.Name})
					before := w.show(id)
					params, _ := setupFor(Row{}, "")
					params["stage"] = StageImplementer
					ref := wantCode(t, w.apply(id, v, params), cli.CodeTransitionNotAllowed)
					if !slices.Equal(ref.AllowedNext, tbl.AllowedNext(s.Name)) || ref.CurrentStatus != s.Name {
						t.Fatalf("%s from %s: allowed_next %v current %s; want %v", v, s.Name, ref.AllowedNext, ref.CurrentStatus, tbl.AllowedNext(s.Name))
					}
					if after := w.show(id); after.Status != s.Name || after.Seq != before.Seq {
						t.Fatalf("%s from %s changed the item: %+v", v, s.Name, after)
					}
				}
			}
		})
	}
}

func firstCreate(t *Table) string {
	for _, r := range t.Rows {
		if slices.Contains(r.From, Initial) && r.when == nil {
			return r.Verb
		}
		if slices.Contains(r.From, Initial) && r.When == "no_open_dependency" {
			return r.Verb
		}
	}
	panic(fmt.Sprintf("%s has no create row", t.Kind))
}

func TestStatusesAreConfigurableInBeads(t *testing.T) {
	for _, s := range Statuses() {
		if s.Name == Initial {
			t.Fatal("open is bd's own status")
		}
	}
	if len(Statuses()) == 0 {
		t.Fatal("no custom statuses")
	}
	// The server registers each status at workspace open, so a waiting bug
	// needs its status in the list, frozen so that no stage claims it.
	if !slices.Contains(Statuses(), beadsx.StatusDef{Name: "awaiting_build", Category: beadsx.CategoryFrozen}) {
		t.Fatalf("Statuses() = %v; want awaiting_build, frozen", Statuses())
	}
}

func TestSchemaPrintsEveryTable(t *testing.T) {
	s, err := cli.Surface().Schema()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []Kind{KindTicket, KindEpic, KindDecision, KindBug} {
		if _, ok := s.TransitionTables[string(k)]; !ok {
			t.Fatalf("the schema has no %s table", k)
		}
	}
}
