package inbox

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/shape"
	"github.com/iantanwx/loop/internal/store"
)

// DocGitHub reads and merges an epic's document PR for a sign-off answer,
// and fills the merge event as the reactor does. *github.Service is one.
type DocGitHub interface {
	GetPR(ctx context.Context, p config.Project, n int) (github.PR, error)
	PRFiles(ctx context.Context, p config.Project, n int) ([]github.File, error)
	ReadAt(ctx context.Context, p config.Project, sha, path string) ([]byte, error)
	MergePR(ctx context.Context, p config.Project, n int, sha string) (string, error)
	DefaultBranch(ctx context.Context, p config.Project) (string, error)
	DocMerge(ctx context.Context, p config.Project, it machine.Item, sha string) (machine.MergeEvent, error)
}

// SetGitHub gives the inbox GitHub, so a sign-off answer merges. The
// GitHub setup calls it after the inbox is composed.
func (in *Inbox) SetGitHub(gh DocGitHub) { in.cfg.GitHub = gh }

// signoff answers a sign-off entry with the operator's approval (ADR
// 0010). The server, not an agent, merges the document PR, and only at
// the head the entry named: the head the review passed at, or the head
// the server pinned when the PR moved. A PR that moved since the entry
// was raised merges nothing. The server pins the new head, so the entry
// is raised again at it, and the operator approves that head or not.
func (in *Inbox) signoff(ctx context.Context, call *rpc.Call, id, project, item, body string) cli.Result {
	if in.cfg.GitHub == nil {
		return cli.Reject(cli.CodeNotImplemented, "no_github", "this server has no GitHub, so it cannot merge a document")
	}
	it, ref := in.cfg.Machine.Read(ctx, project, item)
	if ref != nil {
		return ref.Result()
	}
	if it.Kind != machine.KindEpic || it.Status != machine.PRDSignoff && it.Status != machine.SpecSignoff {
		return cli.Refuse(cli.Refusal{Code: cli.CodeStatusChanged, Rule: "not_in_signoff", Item: item, CurrentStatus: it.Status,
			AllowedNext: it.AllowedNext, Hint: fmt.Sprintf("%s is %s, so no document of it waits for a sign-off; run `loop inbox list` again", item, it.Status)})
	}
	n, err := strconv.Atoi(it.Fact(machine.FactDocPR))
	if err != nil {
		return cli.Internal(fmt.Errorf("%s in %s names no document PR", item, it.Status))
	}
	p, err := in.project(project)
	if err != nil {
		return cli.Internal(err)
	}
	pr, err := in.cfg.GitHub.GetPR(ctx, p, n)
	if err != nil {
		return github.Refuse(err)
	}
	if pr.State != "open" {
		return cli.Refuse(cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "pr_not_open", Item: item, CurrentStatus: it.Status,
			Hint: fmt.Sprintf("PR #%d is %s; the reactor reads its merge or its close on its next tick", n, pr.State)})
	}
	pinned := it.Fact(machine.FactReviewedHead)
	if pr.Head.SHA != pinned {
		return in.reraise(ctx, it, n, pinned, pr.Head.SHA)
	}
	var spec string
	if it.Status == machine.SpecSignoff {
		var refused *cli.Refusal
		if spec, refused = in.decidedSpec(ctx, p, it, n, pinned); refused != nil {
			return cli.Refuse(*refused)
		}
	}
	actor := machine.Actor{Name: call.Principal.Actor(), RunID: call.Principal.RunID}
	// The approval is recorded before the merge, so a merge that lands
	// always has the words that approved it.
	if err := in.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO audit (at, actor, action, detail) VALUES (?, ?, 'signoff_answered',
			json_object('entry', ?, 'pr', ?, 'head', ?, 'answer', ?))`, in.cfg.Now().UnixMilli(), actor.Name, id, n, pinned, body)
		return err
	}); err != nil {
		return cli.Internal(err)
	}
	merged, err := in.cfg.GitHub.MergePR(ctx, p, n, pinned)
	if err != nil {
		// GitHub refuses a merge whose head moved between the read and
		// the merge. Read the head again, and raise the entry at it.
		if again, rerr := in.cfg.GitHub.GetPR(ctx, p, n); rerr == nil && again.State == "open" && again.Head.SHA != pinned {
			return in.reraise(ctx, it, n, pinned, again.Head.SHA)
		}
		return github.Refuse(err)
	}
	base, err := in.cfg.GitHub.DefaultBranch(ctx, p)
	if err != nil {
		base = pr.Base.Ref
	}
	// The epic moves on at once. The reactor reads the same merge later,
	// and an epic past sign-off takes it as Nothing.
	ev, err := in.cfg.GitHub.DocMerge(ctx, p, it, merged)
	if err == nil {
		ev.Base, ev.Into, ev.SpecPath = base, pr.Base.Ref, spec
		err = machine.Settled(in.cfg.Machine.MergeObserved(ctx, ev))
	}
	if err != nil {
		in.log.WarnContext(ctx, "the sign-off merged, and the epic waits for the reactor", "item", item, "pr", n, "err", err)
	}
	if err := in.cfg.Store.Write(ctx, func(tx *store.Tx) error { return clearDefer(tx, id) }); err != nil {
		return cli.Internal(err)
	}
	in.Changed()
	status := it.Status
	if now, ref := in.cfg.Machine.Read(ctx, project, item); ref == nil {
		status = now.Status
	}
	return cli.Found(map[string]any{"entry": id, "merged": true, "pr": n, "head": pinned, "merge_commit": merged, "status": status})
}

// decidedSpec returns the path of the epic's spec at the pinned head, or
// refuses the sign-off when the PR holds no single spec or the spec holds
// an open question with no decision (ADR 0002, G1). A PRD's open
// questions pass on to the spec, so a PRD sign-off never reads them.
func (in *Inbox) decidedSpec(ctx context.Context, p config.Project, it machine.Item, n int, pinned string) (string, *cli.Refusal) {
	files, err := in.cfg.GitHub.PRFiles(ctx, p, n)
	if err != nil {
		ref := github.Refusal(err)
		return "", &ref
	}
	changed := make([]shape.ChangedFile, len(files))
	for i, f := range files {
		changed[i] = shape.ChangedFile{Path: f.Path, Removed: f.Status == "removed"}
	}
	spec, matches := shape.SpecFile(it.ID, changed)
	if spec == "" {
		return "", &cli.Refusal{Code: cli.CodeShapeRefused, Rule: "no_spec", Item: it.ID, CurrentStatus: it.Status,
			Hint: fmt.Sprintf("PR #%d changes %d files at %s, and a spec sign-off needs exactly one; nothing merged",
				n, matches, shape.SpecPattern(it.ID))}
	}
	raw, err := in.cfg.GitHub.ReadAt(ctx, p, pinned, spec)
	if err != nil {
		ref := github.Refusal(err)
		return "", &ref
	}
	open := shape.Undecided(string(raw))
	if len(open) == 0 {
		return spec, nil
	}
	lines := make([]string, len(open))
	for i, q := range open {
		lines[i] = fmt.Sprintf("line %d: %s", q.Line, q.Text)
	}
	return "", &cli.Refusal{Code: cli.CodeShapeRefused, Rule: "open_questions", Item: it.ID, CurrentStatus: it.Status,
		Hint: fmt.Sprintf("%s at %s holds %d open questions with no **Answered or **Deferred run, so nothing merged: %s. "+
			"The answer belongs in the spec: a review room on PR #%d writes it, or the operator comments on the PR",
			spec, pinned, len(open), strings.Join(lines, "; "), n)}
}

// reraise pins the document PR's new head, which raises the sign-off
// entry again at it, and refuses the answer.
func (in *Inbox) reraise(ctx context.Context, it machine.Item, n int, pinned, head string) cli.Result {
	res := in.cfg.Machine.Apply(ctx, machine.Request{Project: it.Project, Item: it.ID, Verb: machine.VerbSignoffHead,
		Params: map[string]string{"head": head}, Actor: machine.Server("inbox"), ExpectedStatus: it.Status})
	if res.Refusal != nil {
		return res
	}
	in.Changed()
	was := pinned
	if was == "" {
		was = "no pinned head"
	}
	return cli.Refuse(cli.Refusal{Code: cli.CodeStatusChanged, Rule: "head_moved", Item: it.ID, CurrentStatus: it.Status,
		Hint: fmt.Sprintf("PR #%d moved from %s to %s after the entry was raised, so nothing merged; the entry is raised again at %s. "+
			"Bring the operator the new head, and record a new approval", n, was, head, head)})
}

func (in *Inbox) project(id string) (config.Project, error) {
	if in.cfg.Projects == nil {
		return config.Project{}, fmt.Errorf("the inbox knows no projects")
	}
	ps, err := in.cfg.Projects()
	if err != nil {
		return config.Project{}, err
	}
	for _, p := range ps {
		if p.ID == id {
			return p, nil
		}
	}
	return config.Project{}, fmt.Errorf("no project %s", id)
}

// answerAtOtherHead guards the replay of an answer to a sign-off entry.
// The PRD and the spec of one epic share the entry, so a stored answer
// can approve another head than the head the entry names now. Such an
// answer is refused, and never replayed as success. A stored answer at
// the entry's head, or after the epic left its sign-off, replays.
func (in *Inbox) answerAtOtherHead(ctx context.Context, call *rpc.Call, stored cli.Envelope) *cli.Result {
	kind, project, item, _, ok := parseEntry(call.Inv.Arg("entry"))
	data, _ := stored.Data.(map[string]any)
	approved, _ := data["head"].(string)
	if !ok || kind != KindSignoff || approved == "" {
		return nil
	}
	it, ref := in.cfg.Machine.Read(ctx, project, item)
	if ref != nil || it.Status != machine.PRDSignoff && it.Status != machine.SpecSignoff {
		return nil
	}
	head := it.Fact(machine.FactReviewedHead)
	if head == approved {
		return nil
	}
	return new(cli.Refuse(cli.Refusal{Code: cli.CodeStatusChanged, Rule: "answer_at_other_head", Item: item, CurrentStatus: it.Status,
		Hint: fmt.Sprintf("the stored answer of this command approved head %s, and the entry names head %s now; "+
			"run the command again, and the CLI sends it as a new answer at head %s", approved, head, head)}))
}
