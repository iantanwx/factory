package pipeline

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// intakeStage is the token stage of an intake room's session
// (room.Stage("intake")).
const intakeStage = "intake-room"

// PRDResult is what prd submit answers.
type PRDResult struct {
	Epic    string            `json:"epic"`
	Status  string            `json:"status"`
	PR      github.PRView     `json:"pr"`
	Stories []string          `json:"stories"`
	Push    github.PushResult `json:"push"`
}

// prdSubmit is `loop prd submit` (R44, R46): from an intake room, it
// pushes the checkout's HEAD to the epic's branch, opens the PRD's PR and
// creates the epic in prd_review, in one call. The epic's create key is
// the room, so a replay, or a retry after a crash at any step, finds the
// same epic, the same branch and the same PR.
func (p *Pipeline) prdSubmit(ctx context.Context, call *rpc.Call) cli.Result {
	pr, inv := call.Principal, call.Inv
	if pr.Kind != cli.PrincipalRoom || pr.Stage != intakeStage {
		return cli.Reject(cli.CodeCapabilityDenied, "intake_room_only", "only an intake room submits a PRD")
	}
	if pr.Item != "" {
		return cli.Reject(cli.CodeUsage, "room_holds_item", fmt.Sprintf(
			"this room revises %s: push with `loop branch push`, and the operator's inbox answer returns it to review", pr.Item))
	}
	file := path.Clean(strings.TrimSpace(inv.Flags["path"]))
	if file == "." || path.IsAbs(file) || strings.HasPrefix(file, "../") {
		return cli.Reject(cli.CodeUsage, "bad_path", "--path is the PRD's path inside the repository, for example docs/prd/widgets.md")
	}
	head := inv.Extra[cli.ExtraHead]
	if head == "" {
		return cli.Reject(cli.CodeUsage, "no_head", "the CLI sent no HEAD; submit from inside the room's checkout")
	}
	if c := inv.Extra[cli.ExtraContentHash]; c != "" && c != cli.HeadContentHash(head) {
		return cli.Reject(cli.CodeUsage, "head_moved", "HEAD moved while the CLI read it; submit again")
	}
	title, body := strings.TrimSpace(inv.Flags["title"]), inv.Flags["body"]
	if ref := p.cfg.Redactor.Refusal("secret_in_text", "text", title, body); ref != nil {
		return ref.Result()
	}
	prj, ref := p.registration(pr.Project)
	if ref != nil {
		return ref.Result()
	}
	room, err := p.roomOf(ctx, pr.RunID)
	if err != nil {
		return cli.Internal(err)
	}

	// Read the PRD at the head before any write: a submit that fails here
	// leaves nothing behind.
	mirror := p.cfg.GitHub.Mirror(prj)
	if err := mirror.Init(ctx); err != nil {
		return cli.Internal(err)
	}
	if err := mirror.FetchCheckout(ctx, pr.Checkout, head, "refs/loop/intake/"+room); err != nil {
		return cli.Reject(cli.CodeUsage, "head_unreadable", "the server could not read the checkout's HEAD commit; commit the PRD and submit again")
	}
	text, err := mirror.Show(ctx, head, file)
	if err != nil {
		return cli.Reject(cli.CodeUsage, "no_prd", fmt.Sprintf("the checkout's HEAD holds no %s; commit the PRD and submit again", file))
	}
	stories := storyLabels(string(text))
	if len(stories) == 0 {
		return cli.Reject(cli.CodeUsage, "no_story_labels",
			"the PRD holds no story label; label each user story US-01, US-02 and on (R45), then submit again")
	}

	epic, ref := p.findOrCreate(ctx, machine.CreateRequest{Project: prj.ID, Kind: machine.KindEpic, Title: title,
		Facts: map[string]string{machine.FactCreateKey: "intake/" + room, machine.FactPRDPath: file}})
	if ref != nil {
		return ref.Result()
	}
	if p.stop("epic_created") {
		return cli.Internal(errCrash)
	}
	switch {
	case epic.Status != machine.Initial && epic.Status != machine.PRDReview:
		return cli.Refuse(cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "prd_past_review", Item: epic.ID,
			CurrentStatus: epic.Status, AllowedNext: epic.AllowedNext,
			Hint: fmt.Sprintf("this room already submitted %s, and it is %s now", epic.ID, epic.Status)})
	case epic.Fact(machine.FactPRDPath) != file:
		return cli.Reject(cli.CodeUsage, "prd_moved", fmt.Sprintf("this room submitted %s as %s; submit that path",
			epic.Fact(machine.FactPRDPath), epic.ID))
	}
	push, err := p.cfg.GitHub.Push(ctx, github.PushRequest{Project: prj.ID, Item: epic.ID, Branch: github.BranchFor(epic.ID),
		Checkout: pr.Checkout, Head: head, RunID: pr.RunID})
	if err != nil {
		return github.Refuse(err)
	}
	ghpr, _, _, err := p.cfg.GitHub.OpenPR(ctx, prj, github.BranchFor(epic.ID), title, body)
	if err != nil {
		return github.Refuse(err)
	}
	if epic.Status == machine.Initial {
		res := p.cfg.Machine.Apply(ctx, machine.Request{Project: prj.ID, Item: epic.ID, Verb: machine.VerbPRDSubmit,
			Params: map[string]string{"pr": strconv.Itoa(ghpr.Number)}, ExpectedStatus: machine.Initial,
			Command: inv.Verb.Path, RequestID: inv.RequestID, Actor: machine.Actor{Name: pr.Actor(), RunID: pr.RunID}})
		if res.Refusal != nil {
			return res
		}
	}
	return cli.Found(PRDResult{Epic: epic.ID, Status: machine.PRDReview, PR: ghpr.View(), Stories: stories, Push: push})
}
