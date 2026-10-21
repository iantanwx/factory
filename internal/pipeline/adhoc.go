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
// on its own. A run's PRD-less ticket is PRD_REQUIRED.
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
	facts := map[string]string{}
	switch {
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

	cr := machine.CreateRequest{Project: prj, Kind: machine.KindTicket, Title: title, Description: body, Facts: facts}
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
		res := p.cfg.Machine.Apply(ctx, machine.Request{Project: prj, Item: it.ID, Verb: "create", ExpectedStatus: machine.Initial,
			RequestID: inv.RequestID, Actor: machine.Actor{Name: pr.Actor(), RunID: pr.RunID}})
		if res.Refusal != nil {
			return res
		}
		it.Status = machine.Todo
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
