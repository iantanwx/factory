package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
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
// reproducer stage claims from, under the same rule. With --blocked-by
// the ticket waits in backlog on each blocker, which is checked before any
// write. A bug takes no blocker. A ticket under an epic with a PRD holds
// the Ticket shape, and records it as its shape fact. PRD-less work and a
// bug are not checked.
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
		// A bug skips the PRD, so it follows R48 as a PRD-less ticket
		// does: only the coordinator files one, on the operator's request.
		kind, verb = machine.KindBug, machine.VerbFile
		switch {
		case len(blockers) > 0:
			return cli.Reject(cli.CodeUsage, "bug_takes_no_blocker",
				"a bug report waits on nothing; the reproducer shapes it into a ticket, and work shape takes --blocked-by")
		case pr.Kind != cli.PrincipalCoordinator:
			return cli.Reject(cli.CodeCapabilityDenied, "coordinator_only",
				"only the coordinator files a bug, from the operator's report; a run reports a fault in the loop with `loop fault report`")
		case request == "":
			return cli.Reject(cli.CodePRDRequired, "no_operator_request",
				"a bug skips the PRD, so it needs the operator's request: quote the operator's own words with --request")
		case epicID != "" || len(stories) > 0:
			return cli.Reject(cli.CodeUsage, "bug_without_epic", "a bug cites no epic and no story; the reproducer shapes it into a ticket")
		}
		facts[machine.FactRequest] = request
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
		labels, ref := p.prdLabels(ctx, epic)
		if ref != nil {
			return ref.Result()
		}
		if m := citeCheck("the ticket", stories, labels); m != "" {
			return cli.Reject(cli.CodeStoryUnknown, "story_not_in_prd", m)
		}
		if r := machine.BodyRefusal(shape.Ticket, "", body); r != nil {
			return r.Result()
		}
		facts[machine.FactShape] = shape.Ticket.Name
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

// addBlockers writes one blocks edge from a ticket in Initial to each
// blocker, before its create move, as decompose does: a crash between
// leaves the ticket in Initial, in no queue.
func (p *Pipeline) addBlockers(ctx context.Context, prj, ticket string, blockers []string) *cli.Refusal {
	bc, err := p.cfg.Beads(prj)
	if err != nil {
		return cli.InternalRefusal(err)
	}
	for _, b := range blockers {
		if err := bc.AddDependency(ctx, ticket, b, beadsx.DepBlocks); err != nil {
			return cli.InternalRefusal(fmt.Errorf("pipeline: record that %s waits on %s: %w", ticket, b, err))
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
	pr := call.Principal
	if key := call.Request.GetIdempotencyKey(); key != "" && pr.RunID != "" {
		cr.Facts[machine.FactCreateKey] = scope + "/" + pr.RunID + "/" + key
		return p.findOrCreate(ctx, cr)
	}
	id, ref := p.cfg.Machine.CreateBead(ctx, cr)
	if ref != nil {
		return machine.Item{}, ref
	}
	return p.cfg.Machine.Read(ctx, cr.Project, id)
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
// the GitHub reactor sees an ADR whose Ticket line names it on the base
// branch, and that ADR is the operator's sign-off (R46). A PRD epic
// starts with prd submit in an intake room instead.
func (p *Pipeline) epicCreate(ctx context.Context, call *rpc.Call) cli.Result {
	pr, inv := call.Principal, call.Inv
	if pr.Kind != cli.PrincipalCoordinator {
		return cli.Reject(cli.CodeCapabilityDenied, "coordinator_only", "only the coordinator creates an architecture epic")
	}
	if inv.Flags["architecture"] != "true" {
		return cli.Reject(cli.CodeUsage, "architecture_only",
			"epic create makes an architecture epic only, so pass --architecture; a PRD epic starts with prd submit in an intake room")
	}
	prj, ref := call.Project(ownProject)
	if ref != nil {
		return ref.Result()
	}
	title, body := strings.TrimSpace(inv.Flags["title"]), inv.Flags["body"]
	if title == "" {
		return cli.Reject(cli.CodeUsage, "no_title", "--title names the epic")
	}
	if ref := p.cfg.Redactor.Refusal("secret_in_text", "text", title, body); ref != nil {
		return ref.Result()
	}
	cr := machine.CreateRequest{Project: prj, Kind: machine.KindEpic, Title: title, Description: body, Facts: map[string]string{}}
	it, ref := p.create(ctx, call, "epic", cr)
	if ref != nil {
		return ref.Result()
	}
	if it.Status == machine.Initial {
		res := p.cfg.Machine.Apply(ctx, machine.Request{Project: prj, Item: it.ID, Verb: machine.VerbCreateArchitecture,
			ExpectedStatus: machine.Initial, RequestID: inv.RequestID, Actor: machine.Actor{Name: pr.Actor(), RunID: pr.RunID}})
		if res.Refusal != nil {
			return res
		}
		it.Status = machine.Architecture
	}
	return cli.Found(EpicResult{Epic: it.ID, Status: it.Status})
}
