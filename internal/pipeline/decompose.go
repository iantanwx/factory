package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// Decomposition is the file of `loop epic decompose --file`.
type Decomposition struct {
	Tickets []TicketSpec `json:"tickets"`
}

// TicketSpec is one ticket of a decomposition. Key names it inside the
// batch: the ticket's create key is the epic and the key (KTD24).
// BlockedBy names the keys of the tickets that must land first.
type TicketSpec struct {
	Key       string   `json:"key"`
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"`
	Stories   []string `json:"stories"`
	BlockedBy []string `json:"blocked_by,omitempty"`
}

// TicketCap is the most tickets one decompose batch may hold. It is a
// smoke alarm for a runaway decomposer, not a sizing rule: a count cannot
// see whether an epic reaches more than one subsystem, and a decomposer at
// a sizing ceiling would merge slices into fat tickets to comply. The two
// questions of loop-sdlc-split decide the unit. Twenty-five is the cap
// tickertown keeps on one epic for the same reason.
const TicketCap = 25

// BatchTicket is one ticket of the answer.
type BatchTicket struct {
	Key    string `json:"key"`
	Item   string `json:"item"`
	Status string `json:"status"`
}

// BatchResult is what epic decompose answers.
type BatchResult struct {
	Epic    string        `json:"epic"`
	Status  string        `json:"status"`
	Tickets []BatchTicket `json:"tickets"`
}

// decompose is `loop epic decompose` (KTD24): every ticket of the batch
// and every blocking edge in one call, then the epic to execution. The
// server checks the whole batch before the first write, so a refused
// batch writes nothing. Each ticket's create key is the epic and its key,
// so a call that a crash cut short, run again, finds what it created and
// makes the rest: no ticket twice.
func (p *Pipeline) decompose(ctx context.Context, call *rpc.Call) cli.Result {
	pr := call.Principal
	if pr.Kind != cli.PrincipalRun || pr.Stage != machine.StageDecomposer || pr.Item == "" {
		return cli.Reject(cli.CodeCapabilityDenied, "decomposer_only", "only the decomposer run that holds the epic decomposes it")
	}
	raw, ok := cli.FileContent(call.Inv)
	if !ok {
		return cli.Reject(cli.CodeUsage, "no_file", "--file names the decomposition file")
	}
	var d Decomposition
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return cli.Reject(cli.CodeUsage, "bad_file", "the decomposition file is not the JSON object the schema names: "+err.Error())
	}
	if n := len(d.Tickets); n > TicketCap {
		return cli.Reject(cli.CodeUsage, "runaway_batch", fmt.Sprintf("the batch holds %d tickets, and %d is the runaway limit: "+
			"no ticket was created. This is no sizing rule, so never merge slices to fit under it. An epic this large "+
			"likely needs a split, and only the operator agrees to one: escalate, and name how many slices the spec "+
			"needs and the seam where the epic would split (loop-sdlc-split)", n, TicketCap))
	}
	order, msg := topoOrder(d.Tickets)
	if msg != "" {
		return cli.Reject(cli.CodeUsage, "bad_batch", msg)
	}
	epic, ref := p.cfg.Machine.Show(ctx, pr.Project, pr.Item)
	if ref != nil {
		return ref.Result()
	}
	// An architecture epic follows an ADR and never has a PRD (F-15). Its
	// ADR stands in for the PRD, so its tickets cite no story label, and
	// a ticket that names one cites a PRD that does not exist.
	arch := epic.Fact(machine.FactADR) != "" && epic.Fact(machine.FactPRDMerged) == ""
	var labels []string
	if !arch {
		if labels, ref = p.prdLabels(ctx, epic); ref != nil {
			return ref.Result()
		}
	}
	var faults []string
	var texts []string
	for _, t := range d.Tickets {
		switch {
		case arch && len(t.Stories) > 0:
			faults = append(faults, fmt.Sprintf("ticket %s cites %s, and an architecture epic has no PRD to hold a story",
				t.Key, strings.Join(t.Stories, ", ")))
		case !arch:
			if m := citeCheck("ticket "+t.Key, t.Stories, labels); m != "" {
				faults = append(faults, m)
			}
		}
		texts = append(texts, t.Title, t.Body)
	}
	if len(faults) > 0 {
		rule := "story_not_in_prd"
		if arch {
			rule = "story_without_prd"
		}
		return cli.Refuse(cli.Refusal{Code: cli.CodeStoryUnknown, Rule: rule, Item: epic.ID,
			Hint: "the batch is refused, and no ticket was created: " + strings.Join(faults, "; ")})
	}
	if ref := p.cfg.Redactor.Refusal("secret_in_text", "text", texts...); ref != nil {
		return ref.Result()
	}

	switch epic.Status {
	case machine.Execution:
		// The batch landed, and its answer was lost: answer it again.
		return p.landed(ctx, epic, order)
	case machine.Decomposing:
	default:
		return cli.Refuse(cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "no_edge", Item: epic.ID,
			CurrentStatus: epic.Status, AllowedNext: epic.AllowedNext, Hint: fmt.Sprintf("%s is %s, not decomposing", epic.ID, epic.Status)})
	}
	if pr.Epoch != epic.Epoch {
		return cli.Refuse(cli.Refusal{Code: cli.CodeLeaseNotHeld, Rule: "stale_epoch", Item: epic.ID, CurrentStatus: epic.Status,
			Hint: fmt.Sprintf("the run holds epoch %d of %s, and the lease is at epoch %d", pr.Epoch, epic.ID, epic.Epoch)})
	}

	ids := map[string]machine.Item{}
	for _, t := range order {
		it, ref := p.findOrCreate(ctx, machine.CreateRequest{Project: epic.Project, Kind: machine.KindTicket, Title: t.Title,
			Description: t.Body, Facts: map[string]string{
				machine.FactCreateKey: epic.ID + "/" + t.Key, machine.FactEpic: epic.ID,
				machine.FactStories: strings.Join(t.Stories, ","),
			}})
		if ref != nil {
			return ref.Result()
		}
		ids[t.Key] = it
		if p.stop("ticket_created") {
			return cli.Internal(errCrash)
		}
	}
	bc, err := p.cfg.Beads(epic.Project)
	if err != nil {
		return cli.Internal(err)
	}
	actor := machine.Actor{Name: pr.Actor(), RunID: pr.RunID}
	for _, t := range order {
		it := ids[t.Key]
		if it.Status != machine.Initial {
			continue
		}
		// The edges go in before the ticket's first move, so a ticket that
		// left Initial holds all of its edges.
		for _, b := range t.BlockedBy {
			if err := bc.AddDependency(ctx, it.ID, ids[b].ID, beadsx.DepBlocks); err != nil {
				return cli.Internal(fmt.Errorf("pipeline: record that %s waits on %s: %w", it.ID, ids[b].ID, err))
			}
		}
		params := map[string]string{}
		if len(t.BlockedBy) > 0 {
			params["blocked"] = "true"
		}
		res := p.cfg.Machine.Apply(ctx, machine.Request{Project: epic.Project, Item: it.ID, Verb: machine.VerbCreate, Params: params,
			ExpectedStatus: machine.Initial, Actor: actor})
		if res.Refusal != nil {
			return res
		}
	}
	res := p.cfg.Machine.Apply(ctx, machine.Request{Project: epic.Project, Item: epic.ID, Verb: machine.VerbDecomposed,
		ExpectedStatus: machine.Decomposing, RequestID: call.Inv.RequestID,
		Actor: machine.Actor{Name: pr.Actor(), RunID: pr.RunID, Fenced: true, Epoch: pr.Epoch}})
	if res.Refusal != nil {
		return res
	}
	return p.landed(ctx, epic, order)
}

// landed answers a batch whose every ticket exists.
func (p *Pipeline) landed(ctx context.Context, epic machine.Item, order []TicketSpec) cli.Result {
	out := BatchResult{Epic: epic.ID, Status: machine.Execution}
	for _, t := range order {
		it, ref := p.byKey(ctx, epic.Project, machine.KindTicket, epic.ID+"/"+t.Key)
		if ref != nil {
			return ref.Result()
		}
		if it == nil {
			return cli.Refuse(cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "other_batch", Item: epic.ID,
				CurrentStatus: epic.Status, Hint: fmt.Sprintf("%s was decomposed by another batch, which holds no ticket %s", epic.ID, t.Key)})
		}
		out.Tickets = append(out.Tickets, BatchTicket{Key: t.Key, Item: it.ID, Status: it.Status})
	}
	return cli.Found(out)
}

// topoOrder checks the batch's shape and orders it so every blocker comes
// before the tickets it blocks. It refuses a batch with no ticket, a
// missing or repeated key, an empty title, an unknown blocker or a cycle.
func topoOrder(ts []TicketSpec) ([]TicketSpec, string) {
	if len(ts) == 0 {
		return nil, "the batch holds no ticket"
	}
	byKey := map[string]TicketSpec{}
	for _, t := range ts {
		switch {
		case strings.TrimSpace(t.Key) == "" || strings.ContainsAny(t.Key, "/ "):
			return nil, fmt.Sprintf("ticket %q needs a key with no slash and no space", t.Title)
		case strings.TrimSpace(t.Title) == "":
			return nil, fmt.Sprintf("ticket %s has no title", t.Key)
		}
		if _, dup := byKey[t.Key]; dup {
			return nil, fmt.Sprintf("two tickets have the key %s", t.Key)
		}
		byKey[t.Key] = t
	}
	for _, t := range ts {
		for _, b := range t.BlockedBy {
			if _, ok := byKey[b]; !ok || b == t.Key {
				return nil, fmt.Sprintf("ticket %s waits on %s, which is no other ticket of the batch", t.Key, b)
			}
		}
	}
	var out []TicketSpec
	done := map[string]bool{}
	for len(out) < len(ts) {
		moved := false
		for _, t := range ts {
			if done[t.Key] || slices.ContainsFunc(t.BlockedBy, func(b string) bool { return !done[b] }) {
				continue
			}
			done[t.Key], moved = true, true
			out = append(out, t)
		}
		if !moved {
			var left []string
			for _, t := range ts {
				if !done[t.Key] {
					left = append(left, t.Key)
				}
			}
			return nil, "the blocking edges hold a cycle among " + strings.Join(left, ", ")
		}
	}
	return out, ""
}
