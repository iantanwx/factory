package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
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
// on its own. A run's PRD-less ticket is PRD_REQUIRED. With --kind bug the
// coordinator files the operator's bug report in reported, the state the
// reproducer stage claims from, under the same rule (U5).
func (p *Pipeline) workCreate(ctx context.Context, call *rpc.Call) cli.Result {
	pr, inv := call.Principal, call.Inv
	prj, ref := project(call)
	if ref != nil {
		return refuseRef(ref)
	}
	title, body := strings.TrimSpace(inv.Flags["title"]), inv.Flags["body"]
	epicID, request := inv.Flags["epic"], strings.TrimSpace(inv.Flags["request"])
	stories := splitStories(inv.Flags["story"])
	if title == "" {
		return refuse(cli.CodeUsage, "no_title", "--title names the ticket")
	}
	kind, verb := machine.KindTicket, "create"
	facts := map[string]string{}
	switch {
	case inv.Flags["kind"] == string(machine.KindBug):
		// A bug skips the PRD, so it follows R48 as a PRD-less ticket
		// does: only the coordinator files one, on the operator's request.
		kind, verb = machine.KindBug, "file"
		switch {
		case pr.Kind != cli.PrincipalCoordinator:
			return refuse(cli.CodeCapabilityDenied, "coordinator_only",
				"only the coordinator files a bug, from the operator's report; a run reports a fault in the loop with `loop fault report`")
		case request == "":
			return refuse(cli.CodePRDRequired, "no_operator_request",
				"a bug skips the PRD, so it needs the operator's request: quote the operator's own words with --request")
		case epicID != "" || len(stories) > 0:
			return refuse(cli.CodeUsage, "bug_without_epic", "a bug cites no epic and no story; the reproducer shapes it into a ticket")
		}
		facts[machine.FactRequest] = request
	case epicID != "":
		epic, ref := p.cfg.Machine.Read(ctx, prj, epicID)
		if ref != nil {
			return refuseRef(ref)
		}
		if epic.Kind != machine.KindEpic {
			return refuse(cli.CodeUsage, "not_an_epic", fmt.Sprintf("%s is a %s, not an epic", epicID, epic.Kind))
		}
		labels, ref := p.prdLabels(ctx, epic)
		if ref != nil {
			return refuseRef(ref)
		}
		if m := citeCheck("the ticket", stories, labels); m != "" {
			return refuse(cli.CodeStoryUnknown, "story_not_in_prd", m)
		}
		facts[machine.FactEpic] = epicID
		facts[machine.FactStories] = strings.Join(stories, ",")
		if request != "" {
			facts[machine.FactRequest] = request
		}
	case pr.Kind != cli.PrincipalCoordinator:
		return refuse(cli.CodePRDRequired, "prd_first",
			"work starts with a PRD: name the epic whose merged PRD the ticket implements with --epic and --story. "+
				"Only the coordinator creates PRD-less work, when the operator asks for it")
	case request == "":
		return refuse(cli.CodePRDRequired, "no_operator_request",
			"a PRD-less ticket needs the operator's request: quote the operator's own words with --request")
	case len(stories) > 0:
		return refuse(cli.CodeUsage, "story_without_epic", "--story cites the labels of an epic's PRD; name the epic with --epic")
	default:
		facts[machine.FactRequest] = request
	}
	if ref := p.secretIn(title, body, request); ref != nil {
		return refuseRef(ref)
	}

	cr := machine.CreateRequest{Project: prj, Kind: kind, Title: title, Description: body, Facts: facts}
	var it machine.Item
	if key := call.Request.GetIdempotencyKey(); key != "" && pr.RunID != "" {
		// A retry of the same write finds the ticket it made.
		cr.Facts[machine.FactCreateKey] = "work/" + pr.RunID + "/" + key
		it, ref = p.findOrCreate(ctx, cr)
	} else {
		var id string
		if id, ref = p.cfg.Machine.CreateBead(ctx, cr); ref == nil {
			it, ref = p.cfg.Machine.Read(ctx, prj, id)
		}
	}
	if ref != nil {
		return refuseRef(ref)
	}
	if it.Status == machine.Initial {
		res := p.cfg.Machine.Apply(ctx, machine.Request{Project: prj, Item: it.ID, Verb: verb, ExpectedStatus: machine.Initial,
			RequestID: inv.RequestID, Actor: machine.Actor{Name: pr.Actor(), RunID: pr.RunID}})
		if res.Refusal != nil {
			return res
		}
		it.Status = machine.Todo
		if kind == machine.KindBug {
			it.Status = machine.Reported
		}
	}
	return cli.Found(CreateResult{Item: it.ID, Status: it.Status, Epic: epicID})
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

// epicCreate is `loop epic create --architecture`: the coordinator creates
// an epic that follows an ADR, not a PRD. It waits in architecture until
// the GitHub reactor sees an ADR that names it on the base branch, and
// that ADR is the operator's sign-off (R46). A PRD epic starts with prd
// submit in an intake room instead.
func (p *Pipeline) epicCreate(ctx context.Context, call *rpc.Call) cli.Result {
	pr, inv := call.Principal, call.Inv
	if pr.Kind != cli.PrincipalCoordinator {
		return refuse(cli.CodeCapabilityDenied, "coordinator_only", "only the coordinator creates an architecture epic")
	}
	if inv.Flags["architecture"] != "true" {
		return refuse(cli.CodeUsage, "architecture_only",
			"epic create makes an architecture epic only, so pass --architecture; a PRD epic starts with prd submit in an intake room")
	}
	prj, ref := project(call)
	if ref != nil {
		return refuseRef(ref)
	}
	title, body := strings.TrimSpace(inv.Flags["title"]), inv.Flags["body"]
	if title == "" {
		return refuse(cli.CodeUsage, "no_title", "--title names the epic")
	}
	if ref := p.secretIn(title, body); ref != nil {
		return refuseRef(ref)
	}
	cr := machine.CreateRequest{Project: prj, Kind: machine.KindEpic, Title: title, Description: body, Facts: map[string]string{}}
	var it machine.Item
	if key := call.Request.GetIdempotencyKey(); key != "" && pr.RunID != "" {
		cr.Facts[machine.FactCreateKey] = "epic/" + pr.RunID + "/" + key
		it, ref = p.findOrCreate(ctx, cr)
	} else {
		var id string
		if id, ref = p.cfg.Machine.CreateBead(ctx, cr); ref == nil {
			it, ref = p.cfg.Machine.Read(ctx, prj, id)
		}
	}
	if ref != nil {
		return refuseRef(ref)
	}
	if it.Status == machine.Initial {
		res := p.cfg.Machine.Apply(ctx, machine.Request{Project: prj, Item: it.ID, Verb: "create_architecture",
			ExpectedStatus: machine.Initial, RequestID: inv.RequestID, Actor: machine.Actor{Name: pr.Actor(), RunID: pr.RunID}})
		if res.Refusal != nil {
			return res
		}
		it.Status = machine.Architecture
	}
	return cli.Found(EpicResult{Epic: it.ID, Status: it.Status})
}
