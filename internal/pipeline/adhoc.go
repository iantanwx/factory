package pipeline

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/design"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/shape"
)

// CreateResult is what work create answers.
type CreateResult struct {
	Item   string `json:"item"`
	Status string `json:"status"`
	Epic   string `json:"epic,omitempty"`
}

// workCreate is `loop work create` (R44, R48). Work is PRD-first: a
// ticket names the epic whose merged PRD it implements and the story
// labels it cites. Only the coordinator creates a ticket with no PRD, and
// only when it cites the operator's own request; it never waives the PRD
// on its own. A run's PRD-less ticket is PRD_REQUIRED. The ticket that
// writes an architecture epic's ADR is such PRD-less work, and it still
// names its epic. With --kind bug the
// coordinator files the operator's bug report in reported, the state the
// reproducer stage claims from, under the same rule, and a QA run files
// what it found there (bugFacts). With --blocked-by
// the ticket waits in backlog on each blocker, which is checked before any
// write. A bug takes no blocker. A ticket under an epic with a PRD holds
// the Ticket shape, and records it as its shape fact. PRD-less work, a bug
// and the ADR ticket of a PRD epic in architecture are not checked. No
// ticket body calls a host verb, shaped or not.
func (p *Pipeline) workCreate(ctx context.Context, call *rpc.Call) cli.Result {
	pr, inv := call.Principal, call.Inv
	prj, ref := call.Project(ownProject)
	if ref != nil {
		return ref.Result()
	}
	title, body := strings.TrimSpace(inv.Flags["title"]), inv.Flags["body"]
	epicID, request := inv.Flags["epic"], strings.TrimSpace(inv.Flags["request"])
	stories := splitStories(inv.Flags["story"])
	blockers := machine.ParseBlockers(inv.Flags["blocked-by"])
	if title == "" {
		return cli.Reject(cli.CodeUsage, "no_title", "--title names the ticket")
	}
	kind, verb := machine.KindTicket, machine.VerbCreate
	facts := map[string]string{}
	switch {
	case inv.Flags["kind"] == string(machine.KindBug):
		kind, verb = machine.KindBug, machine.VerbFile
		if len(blockers) > 0 {
			return cli.Reject(cli.CodeUsage, "bug_takes_no_blocker",
				"a bug report waits on nothing; the reproducer shapes it into a ticket, and work shape takes --blocked-by")
		}
		var ref *cli.Refusal
		if facts, ref = p.bugFacts(ctx, call, prj, request); ref != nil {
			return ref.Result()
		}
		if epicID != "" || len(stories) > 0 {
			return cli.Reject(cli.CodeUsage, "bug_without_epic", "a bug cites no epic and no story; the reproducer shapes it into a ticket")
		}
	case epicID != "":
		epic, ref := p.cfg.Machine.Read(ctx, prj, epicID)
		if ref != nil {
			return ref.Result()
		}
		if epic.Kind != machine.KindEpic {
			return cli.Reject(cli.CodeUsage, "not_an_epic", fmt.Sprintf("%s is a %s, not an epic", epicID, epic.Kind))
		}
		if epic.Fact(machine.FactPRDPath) == "" {
			// An architecture epic follows an ADR and never has a PRD, so
			// its ticket (the one that writes the ADR) is PRD-less work
			// under R48 that still names its epic: the run that holds it
			// reads the epic it decides (F-16).
			switch {
			case pr.Kind != cli.PrincipalCoordinator:
				return cli.Reject(cli.CodePRDRequired, "prd_first",
					fmt.Sprintf("%s is an architecture epic with no PRD; only the coordinator creates its ticket, when the operator asks for it", epicID))
			case len(stories) > 0:
				return cli.Reject(cli.CodeUsage, "story_without_prd",
					fmt.Sprintf("%s is an architecture epic with no PRD, so its ticket cites no story; drop --story", epicID))
			case request == "":
				return cli.Reject(cli.CodePRDRequired, "no_operator_request",
					"a ticket under an architecture epic has no PRD, so it needs the operator's request: quote the operator's own words with --request")
			}
			facts[machine.FactEpic] = epicID
			facts[machine.FactRequest] = request
			break
		}
		// A PRD epic in architecture waits for the ADR the operator sent it
		// to. Its ticket writes that ADR at the operator's word, as under
		// an architecture epic, and still cites the PRD's stories. It takes
		// no ticket shape: the spec that names its contracts comes later.
		adr := epic.Status == machine.Architecture
		switch {
		case adr && pr.Kind != cli.PrincipalCoordinator:
			return cli.Reject(cli.CodeCapabilityDenied, "coordinator_only",
				fmt.Sprintf("%s waits in architecture for its ADR; only the coordinator creates the ticket that writes it, when the operator asks for it", epicID))
		case adr && request == "":
			return cli.Reject(cli.CodeUsage, "no_operator_request",
				"the ticket that writes a PRD epic's ADR needs the operator's request: quote the operator's own words with --request")
		}
		labels, ref := p.prdLabels(ctx, epic)
		if ref != nil {
			return ref.Result()
		}
		if m := citeCheck("the ticket", stories, labels); m != "" {
			return cli.Reject(cli.CodeStoryUnknown, "story_not_in_prd", m)
		}
		if !adr {
			if r := machine.BodyRefusal(shape.Ticket, "", body); r != nil {
				return r.Result()
			}
			facts[machine.FactShape] = shape.Ticket.Name
		}
		facts[machine.FactEpic] = epicID
		facts[machine.FactStories] = strings.Join(stories, ",")
		if request != "" {
			facts[machine.FactRequest] = request
		}
	case pr.Kind != cli.PrincipalCoordinator:
		return cli.Reject(cli.CodePRDRequired, "prd_first",
			"work starts with a PRD: name the epic whose merged PRD the ticket implements with --epic and --story. "+
				"Only the coordinator creates PRD-less work, when the operator asks for it")
	case request == "":
		return cli.Reject(cli.CodePRDRequired, "no_operator_request",
			"a PRD-less ticket needs the operator's request: quote the operator's own words with --request")
	case len(stories) > 0:
		return cli.Reject(cli.CodeUsage, "story_without_epic", "--story cites the labels of an epic's PRD; name the epic with --epic")
	default:
		facts[machine.FactRequest] = request
	}
	if kind == machine.KindTicket {
		if r := machine.HostVerbRefusal("", body); r != nil {
			return r.Result()
		}
	}
	if ref := p.cfg.Redactor.Refusal("secret_in_text", "text", title, body, request); ref != nil {
		return ref.Result()
	}
	if ref := p.cfg.Machine.CheckBlockers(ctx, prj, "", blockers); ref != nil {
		return ref.Result()
	}

	cr := machine.CreateRequest{Project: prj, Kind: kind, Title: title, Description: body, Facts: facts}
	it, ref := p.create(ctx, call, "work", cr)
	if ref != nil {
		return ref.Result()
	}
	if it.Status == machine.Initial {
		params := map[string]string{}
		if len(blockers) > 0 {
			if ref := p.addBlockers(ctx, prj, it.ID, blockers); ref != nil {
				return ref.Result()
			}
			params["blocked"] = "true"
		}
		res := p.cfg.Machine.Apply(ctx, machine.Request{Project: prj, Item: it.ID, Verb: verb, Params: params,
			ExpectedStatus: machine.Initial, RequestID: inv.RequestID, Actor: machine.Actor{Name: pr.Actor(), RunID: pr.RunID}})
		if res.Refusal != nil {
			return res
		}
		switch {
		case kind == machine.KindBug:
			it.Status = machine.Reported
		case len(blockers) > 0:
			it.Status = machine.Backlog
		default:
			it.Status = machine.Todo
		}
	}
	return cli.Found(CreateResult{Item: it.ID, Status: it.Status, Epic: epicID})
}

// QABugCap is the bugs one QA run may file. A run that misreads the merge
// base could flood the reproducer, and each bug costs a reproducer run.
const QABugCap = 3

// bugFacts checks the filer and the source of a bug, and returns the bug's
// facts. A bug skips the PRD, so the coordinator files one as it creates
// PRD-less work under R48: on the operator's request, with source
// operator. A QA run that holds a ticket files what it found with source
// qa and no request, and the bug records the run and the head it read. No
// other run files a bug. The value of --source is checked first, so every
// filer gets one answer for a value that is neither.
func (p *Pipeline) bugFacts(ctx context.Context, call *rpc.Call, prj, request string) (map[string]string, *cli.Refusal) {
	pr, source := call.Principal, call.Inv.Flags["source"]
	if source != "" && source != machine.SourceOperator && source != machine.SourceQA {
		return nil, &cli.Refusal{Code: cli.CodeUsage, Rule: "source_unknown",
			Hint: fmt.Sprintf("--source is operator or qa, not %q; nothing was written", source)}
	}
	switch {
	case pr.Kind == cli.PrincipalCoordinator && source == machine.SourceQA:
		return nil, &cli.Refusal{Code: cli.CodeUsage, Rule: "source_mismatch",
			Hint: "the coordinator files the operator's report, with source operator; only a QA run files a bug with --source qa"}
	case pr.Kind == cli.PrincipalCoordinator && request == "":
		return nil, &cli.Refusal{Code: cli.CodePRDRequired, Rule: "no_operator_request",
			Hint: "a bug skips the PRD, so it needs the operator's request: quote the operator's own words with --request"}
	case pr.Kind == cli.PrincipalCoordinator:
		return map[string]string{machine.FactSource: machine.SourceOperator, machine.FactRequest: request}, nil
	case pr.Kind != cli.PrincipalRun || pr.Stage != config.StageQA || pr.Item == "":
		return nil, &cli.Refusal{Code: cli.CodeCapabilityDenied, Rule: "bug_filer",
			Hint: "only the coordinator files a bug, from the operator's report, and a QA run files what it found; a run reports a fault in the loop with `loop fault report`"}
	case source != machine.SourceQA:
		return nil, &cli.Refusal{Code: cli.CodeUsage, Rule: "source_mismatch", Hint: "a QA run files a bug with --source qa"}
	case request != "":
		return nil, &cli.Refusal{Code: cli.CodeUsage, Rule: "qa_takes_no_request",
			Hint: "--request quotes the operator, and a QA run files what it found; drop --request"}
	}
	ticket, ref := p.cfg.Machine.Show(ctx, prj, pr.Item)
	if ref != nil {
		return nil, ref
	}
	if pr.Epoch != ticket.Epoch {
		return nil, &cli.Refusal{Code: cli.CodeLeaseNotHeld, Rule: "stale_epoch", Item: ticket.ID, CurrentStatus: ticket.Status,
			Hint: fmt.Sprintf("the run holds epoch %d of %s, and the lease is at epoch %d", pr.Epoch, ticket.ID, ticket.Epoch)}
	}
	if ref := p.qaBugCap(ctx, call, prj); ref != nil {
		return nil, ref
	}
	return map[string]string{machine.FactSource: machine.SourceQA, machine.FactQARun: pr.RunID,
		machine.FactQAHead: ticket.Fact(machine.FactReviewedHead)}, nil
}

// qaBugCap refuses the QA run's bug past QABugCap, unless a replay finds
// the bug its create key already made.
func (p *Pipeline) qaBugCap(ctx context.Context, call *rpc.Call, prj string) *cli.Refusal {
	if key := createKey(call, "work"); key != "" {
		it, ref := p.byKey(ctx, prj, machine.KindBug, key)
		if ref != nil || it != nil {
			return ref
		}
	}
	run := call.Principal.RunID
	filed, ref := p.cfg.Machine.List(ctx, prj, machine.ListFilter{Kind: machine.KindBug, Facts: map[string]string{machine.FactQARun: run}})
	if ref != nil {
		return ref
	}
	if len(filed) >= QABugCap {
		return &cli.Refusal{Code: cli.CodeCapabilityDenied, Rule: "qa_bug_cap",
			Hint: fmt.Sprintf("%s already filed %d bugs, and a QA run files at most %d", run, len(filed), QABugCap)}
	}
	return nil
}

// addBlockers writes one blocks edge from a ticket or an epic in Initial
// to each blocker, before its create move, as decompose does: a crash
// between leaves the item in Initial, in no queue.
func (p *Pipeline) addBlockers(ctx context.Context, prj, item string, blockers []string) *cli.Refusal {
	bc, err := p.cfg.Beads(prj)
	if err != nil {
		return cli.InternalRefusal(err)
	}
	for _, b := range blockers {
		if err := bc.AddDependency(ctx, item, b, beadsx.DepBlocks); err != nil {
			return cli.InternalRefusal(fmt.Errorf("pipeline: record that %s waits on %s: %w", item, b, err))
		}
	}
	if p.stop("blockers_added") {
		return cli.InternalRefusal(errCrash)
	}
	return nil
}

// create makes the item of cr. A run's keyed write records its key, under
// scope, as a fact, so a retry of the same write finds the item it made.
func (p *Pipeline) create(ctx context.Context, call *rpc.Call, scope string, cr machine.CreateRequest) (machine.Item, *cli.Refusal) {
	if key := createKey(call, scope); key != "" {
		cr.Facts[machine.FactCreateKey] = key
		return p.findOrCreate(ctx, cr)
	}
	id, ref := p.cfg.Machine.CreateBead(ctx, cr)
	if ref != nil {
		return machine.Item{}, ref
	}
	return p.cfg.Machine.Read(ctx, cr.Project, id)
}

// createKey is the create key of a run's keyed write under scope, or ""
// for a write with no key.
func createKey(call *rpc.Call, scope string) string {
	if key := call.Request.GetIdempotencyKey(); key != "" && call.Principal.RunID != "" {
		return scope + "/" + call.Principal.RunID + "/" + key
	}
	return ""
}

// splitStories reads --story: labels separated by commas.
func splitStories(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// EpicResult is what epic create answers.
type EpicResult struct {
	Epic   string `json:"epic"`
	Status string `json:"status"`
}

// epicCreate is `loop epic create`, the coordinator's one verb for a new
// epic. With --architecture the epic follows an ADR, not a PRD: it waits
// in architecture until the GitHub reactor sees an ADR whose Ticket line
// names it on the base branch, and that ADR is the operator's sign-off
// (R46). With none, it is a PRD epic that waits in prd_needed for an
// unattended-brainstorm run, and it carries the operator's words in its
// request fact. With --blocked-by either waits in backlog on each blocker,
// and records in epic_entry where unblock sends it. Every refusal comes
// before any write. A PRD epic with no blocker asks the brainstorm cap, after
// a replay found no epic under its create key, so a cap that filled after
// the first call does not refuse its replay.
func (p *Pipeline) epicCreate(ctx context.Context, call *rpc.Call) cli.Result {
	pr, inv := call.Principal, call.Inv
	if pr.Kind != cli.PrincipalCoordinator {
		return cli.Reject(cli.CodeCapabilityDenied, "coordinator_only", "only the coordinator creates an epic")
	}
	prj, ref := call.Project(ownProject)
	if ref != nil {
		return ref.Result()
	}
	title, body, request := strings.TrimSpace(inv.Flags["title"]), inv.Flags["body"], inv.Flags["request"]
	if title == "" {
		return cli.Reject(cli.CodeUsage, "no_title", "--title names the epic")
	}
	arch := inv.Flags["architecture"] == "true"
	verb, to := machine.VerbCreateArchitecture, machine.Architecture
	if !arch {
		verb, to = machine.VerbCreatePRD, machine.PRDNeeded
		if strings.TrimSpace(request) == "" {
			return cli.Reject(cli.CodePRDRequired, "no_operator_request",
				"a PRD epic needs the operator's request: quote the operator's own words with --request, or pass --architecture for an epic that follows an ADR")
		}
		on, err := p.cfg.StageEnabled(ctx, prj, brainstormStage)
		if err != nil {
			return cli.Internal(err)
		}
		if !on {
			return cli.Reject(cli.CodeUsage, "prd_author_disabled",
				fmt.Sprintf("no stage writes the PRD in %s: add `%s: {}` under stages in its .loop/config.yaml, then create the epic again", prj, brainstormStage))
		}
	}
	blockers := machine.ParseBlockers(inv.Flags["blocked-by"])
	if ref := p.epicBlockers(ctx, prj, blockers); ref != nil {
		return ref.Result()
	}
	if ref := p.cfg.Redactor.Refusal("secret_in_text", "text", title, body, request); ref != nil {
		return ref.Result()
	}
	facts := map[string]string{}
	if request != "" {
		facts[machine.FactRequest] = request
	}
	cr := machine.CreateRequest{Project: prj, Kind: machine.KindEpic, Title: title, Description: body, Facts: facts}
	if !arch && len(blockers) == 0 {
		if ref := p.brainstormCap(ctx, call, cr); ref != nil {
			return ref.Result()
		}
	}
	it, ref := p.create(ctx, call, "epic", cr)
	if ref != nil {
		return ref.Result()
	}
	if it.Status == machine.Initial {
		params := map[string]string{}
		if len(blockers) > 0 {
			if ref := p.addBlockers(ctx, prj, it.ID, blockers); ref != nil {
				return ref.Result()
			}
			params["blocked"], to = "true", machine.Backlog
		}
		res := p.cfg.Machine.Apply(ctx, machine.Request{Project: prj, Item: it.ID, Verb: verb, Params: params,
			ExpectedStatus: machine.Initial, RequestID: inv.RequestID, Actor: machine.Actor{Name: pr.Actor(), RunID: pr.RunID}})
		if res.Refusal != nil {
			return res
		}
		it.Status = to
	}
	return cli.Found(EpicResult{Epic: it.ID, Status: it.Status})
}

// epicADR is `loop epic adr`, the coordinator's send of a PRD epic to the
// ADR stage with the operator's words. The epic table picks the row by the
// epic's status: before the PRD merges the send only records adr_sent, and
// in the spec stage it moves the epic to architecture.
func (p *Pipeline) epicADR(ctx context.Context, call *rpc.Call) cli.Result {
	pr, inv := call.Principal, call.Inv
	if pr.Kind != cli.PrincipalCoordinator {
		return cli.Reject(cli.CodeCapabilityDenied, "coordinator_only", "only the coordinator sends an epic to the ADR stage, at the operator's word")
	}
	prj, ref := call.Project(ownProject)
	if ref != nil {
		return ref.Result()
	}
	request := strings.TrimSpace(inv.Flags["request"])
	if request == "" {
		return cli.Reject(cli.CodeUsage, "no_operator_request",
			"the send needs the operator's request: quote the operator's own words with --request")
	}
	// --off is the operator's overrule at sign-off: it clears both reasons,
	// so the PRD merge lands the epic on spec_needed.
	verb := machine.VerbADRSend
	if inv.Flags["off"] == "true" {
		verb = machine.VerbADROff
	}
	return p.cfg.Machine.Apply(ctx, machine.Request{Project: prj, Item: inv.Arg("item"), Verb: verb,
		Params: map[string]string{"request": request}, Command: inv.Verb.Path, ExpectedStatus: inv.Extra[cli.ExtraExpectedStatus],
		RequestID: inv.RequestID, Actor: machine.Actor{Name: pr.Actor(), RunID: pr.RunID}})
}

// epicSkipDesign is `loop epic skip-design`, the coordinator's skip of an
// epic's design stage with the operator's words. The epic table moves only
// an epic in design. An open pull request of the design branch holds the
// skip, because its merge would land a record on an epic past the stage.
func (p *Pipeline) epicSkipDesign(ctx context.Context, call *rpc.Call) cli.Result {
	pr, inv := call.Principal, call.Inv
	if pr.Kind != cli.PrincipalCoordinator {
		return cli.Reject(cli.CodeCapabilityDenied, "coordinator_only", "only the coordinator skips the design stage, at the operator's word")
	}
	prj, ref := call.Project(ownProject)
	if ref != nil {
		return ref.Result()
	}
	request := strings.TrimSpace(inv.Flags["request"])
	if request == "" {
		return cli.Reject(cli.CodeUsage, "no_operator_request",
			"the skip needs the operator's request: quote the operator's own words with --request")
	}
	epic, ref := p.cfg.Machine.Read(ctx, prj, inv.Arg("item"))
	if ref != nil {
		return ref.Result()
	}
	// The pull request read and the move are not one write, so the move
	// expects the status the read saw.
	expected := inv.Extra[cli.ExtraExpectedStatus]
	if epic.Status == machine.Design {
		reg, ref := p.registration(prj)
		if ref != nil {
			return ref.Result()
		}
		branch := github.DesignBranchFor(epic.ID)
		open, err := p.cfg.GitHub.FindPR(ctx, reg, branch)
		if err != nil {
			return github.Refuse(err)
		}
		if open != nil && open.State == "open" {
			return cli.Reject(cli.CodeShapeRefused, "design_pr_open", fmt.Sprintf(
				"%s heads the open pull request #%d; the operator closes it first, so its merge cannot land a record on an epic past the design stage",
				branch, open.Number))
		}
		if expected == "" {
			expected = machine.Design
		}
	}
	return p.cfg.Machine.Apply(ctx, machine.Request{Project: prj, Item: epic.ID, Verb: machine.VerbSkipDesign,
		Params: map[string]string{"request": request}, Command: inv.Verb.Path, ExpectedStatus: expected,
		RequestID: inv.RequestID, Actor: machine.Actor{Name: pr.Actor(), RunID: pr.RunID}})
}

// epicAcceptDifference is `loop epic accept-difference`, the coordinator's
// record of the operator's acceptance of how a shipped screen differs from
// one state of the epic's merged design record. The server checks the
// state against the record at the base, and the epic table appends the
// acceptance to accepted_differences. No file and no ref changes.
func (p *Pipeline) epicAcceptDifference(ctx context.Context, call *rpc.Call) cli.Result {
	pr, inv := call.Principal, call.Inv
	if pr.Kind != cli.PrincipalCoordinator {
		return cli.Reject(cli.CodeCapabilityDenied, "coordinator_only", "only the coordinator records an accepted difference, at the operator's word")
	}
	prj, ref := call.Project(ownProject)
	if ref != nil {
		return ref.Result()
	}
	request := inv.Flags["request"]
	if strings.TrimSpace(request) == "" {
		return cli.Reject(cli.CodeUsage, "no_operator_request",
			"the acceptance needs the operator's request: quote the operator's own words with --request")
	}
	epic, ref := p.cfg.Machine.Read(ctx, prj, inv.Arg("item"))
	if ref != nil {
		return ref.Result()
	}
	state := inv.Flags["state"]
	// The record read and the write are not one write, so the write
	// expects the status the read saw. The epic table refuses any other
	// status, and an epic with no merged record.
	expected := inv.Extra[cli.ExtraExpectedStatus]
	if epic.Status == machine.Execution && epic.Fact(machine.FactDesignMerged) != "" && epic.Fact(machine.FactDesignPath) != "" {
		if ref := p.recordHolds(ctx, epic, state); ref != nil {
			return ref.Result()
		}
		if expected == "" {
			expected = machine.Execution
		}
	}
	return p.cfg.Machine.Apply(ctx, machine.Request{Project: prj, Item: epic.ID, Verb: machine.VerbAcceptDifference,
		Params:  map[string]string{"state": state, "request": request, "at": time.Now().UTC().Format(time.RFC3339)},
		Command: inv.Verb.Path, ExpectedStatus: expected, RequestID: inv.RequestID, Actor: machine.Actor{Name: pr.Actor(), RunID: pr.RunID}})
}

// recordHolds refuses a state that the epic's merged design record at the
// base does not hold.
func (p *Pipeline) recordHolds(ctx context.Context, epic machine.Item, state string) *cli.Refusal {
	rec := epic.Fact(machine.FactDesignPath)
	data, _, err := p.cfg.GitHub.ReadAtBase(ctx, epic.Project, rec)
	if err != nil {
		return &cli.Refusal{Code: cli.CodeShapeRefused, Rule: "no_design_record", Item: epic.ID, CurrentStatus: epic.Status,
			Hint: fmt.Sprintf("the base branch holds no readable %s: %v; nothing was recorded", rec, err)}
	}
	parsed, _ := design.Parse(path.Dir(rec), string(data))
	var ids []string
	for _, s := range parsed.Screens {
		for _, st := range s.States {
			if st.ID == state {
				return nil
			}
			ids = append(ids, st.ID)
		}
	}
	return &cli.Refusal{Code: cli.CodeShapeRefused, Rule: "state_unknown", Item: epic.ID, CurrentStatus: epic.Status,
		Hint: fmt.Sprintf("%s holds no state %s at the base, and it holds %s; nothing was recorded", rec, state, strings.Join(ids, ", "))}
}

// brainstormStage is the run stage that writes a PRD epic's PRD. A project
// whose config does not list it gets no PRD epic, because no run would
// ever claim one there.
const brainstormStage = machine.StageUnattendedBrainstorm

// brainstormCap asks the brainstorm cap for the PRD epic of cr, unless a replay
// finds the epic its create key already made.
func (p *Pipeline) brainstormCap(ctx context.Context, call *rpc.Call, cr machine.CreateRequest) *cli.Refusal {
	if key := createKey(call, "epic"); key != "" {
		it, ref := p.byKey(ctx, cr.Project, cr.Kind, key)
		if ref != nil || it != nil {
			return ref
		}
	}
	return p.cfg.BrainstormCap(ctx, cr.Project)
}

// epicBlockers refuses a blocker list for a new epic before any write: an
// id that is no item of the project, or a bug. A bug ends shaped or
// duplicate, never done, so dependency-advance would read it as open
// forever. A ticket, an epic and a decision each reach a landed state.
func (p *Pipeline) epicBlockers(ctx context.Context, prj string, blockers []string) *cli.Refusal {
	for _, id := range blockers {
		it, ref := p.cfg.Machine.Read(ctx, prj, id)
		if ref != nil && ref.Code == cli.CodeNotFound {
			return &cli.Refusal{Code: cli.CodeNotFound, Rule: "no_blocker", Item: id,
				Hint: fmt.Sprintf("%s is no item of %s; nothing was written", id, prj)}
		}
		if ref != nil {
			return ref
		}
		switch it.Kind {
		case machine.KindTicket, machine.KindEpic, machine.KindDecision:
		default:
			return &cli.Refusal{Code: cli.CodeUsage, Rule: "blocker_kind", Item: id,
				Hint: fmt.Sprintf("%s is a %s, which never lands; an epic waits on a ticket, an epic or a decision; nothing was written", id, it.Kind)}
		}
	}
	return nil
}
