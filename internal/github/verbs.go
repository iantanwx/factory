package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// Handlers returns the server handlers of the GitHub verbs, keyed by verb
// path. The server registers each with rpc.Server.Handle.
func (s *Service) Handlers() map[string]rpc.Handler {
	return map[string]rpc.Handler{
		"branch push":    s.branchPush,
		"branch sync":    s.branchSync,
		"pr open":        s.prOpen,
		"pr show":        s.prShow,
		"pr files":       s.prFiles,
		"pr diff":        s.prDiff,
		"pr threads":     s.prThreads,
		"pr reply":       s.prReply,
		"pr checks":      s.prChecks,
		"pr logs":        s.prLogs,
		"pr rerun":       s.prRerun,
		"review threads": s.reviewThreads,
	}
}

// subject is the project and the item a run's or a room's token holds.
func (s *Service) subject(call *rpc.Call) (config.Project, string, *cli.Refusal) {
	p := call.Principal
	if (p.Kind != cli.PrincipalRun && p.Kind != cli.PrincipalRoom) || p.Project == "" || p.Item == "" {
		return config.Project{}, "", &cli.Refusal{Code: cli.CodeCapabilityDenied, Rule: "no_item",
			Hint: "only a run or a room that holds an item acts on GitHub"}
	}
	prj, err := s.project(p.Project)
	if err != nil {
		r := Refusal(err)
		return config.Project{}, "", &r
	}
	return prj, p.Item, nil
}

// fenced refuses a run whose token epoch is not the item's lease epoch:
// a run that lost its lease writes nothing to GitHub (KTD6). A room holds
// no lease, so it passes.
func (s *Service) fenced(ctx context.Context, call *rpc.Call) (*machine.Item, *cli.Refusal) {
	m := s.cfg.Machine
	if m == nil {
		return nil, nil
	}
	p := call.Principal
	it, ref := m.Show(ctx, p.Project, p.Item)
	if ref != nil {
		return nil, ref
	}
	if p.Kind == cli.PrincipalRun && it.Epoch != p.Epoch {
		return nil, &cli.Refusal{Code: cli.CodeLeaseNotHeld, Rule: "stale_epoch", Item: it.ID, CurrentStatus: it.Status,
			Hint: fmt.Sprintf("the run holds epoch %d of %s, and the lease is at epoch %d", p.Epoch, it.ID, it.Epoch)}
	}
	return &it, nil
}

func (s *Service) branchPush(ctx context.Context, call *rpc.Call) cli.Result {
	prj, item, ref := s.subject(call)
	if ref != nil {
		return ref.Result()
	}
	head := call.Inv.Extra[cli.ExtraHead]
	if head == "" {
		return cli.Reject(cli.CodeUsage, "no_head", "the CLI sent no HEAD; push from inside the checkout")
	}
	if c := call.Inv.Extra[cli.ExtraContentHash]; c != "" && c != cli.HeadContentHash(head) {
		return cli.Reject(cli.CodeUsage, "head_moved", "HEAD moved while the CLI read it; push again")
	}
	if _, ref := s.fenced(ctx, call); ref != nil {
		return ref.Result()
	}
	branch, ref := s.itemBranch(ctx, prj.ID, item)
	if ref != nil {
		return ref.Result()
	}
	p := call.Principal
	res, err := s.Push(ctx, PushRequest{Project: prj.ID, Item: item, Branch: branch, Checkout: p.Checkout,
		Head: head, RunID: p.RunID})
	if err != nil {
		return Refuse(err)
	}
	return cli.Result{Exit: cli.ExitDone, Data: res, Replayed: res.Replayed}
}

func (s *Service) branchSync(ctx context.Context, call *rpc.Call) cli.Result {
	prj, item, ref := s.subject(call)
	if ref != nil {
		return ref.Result()
	}
	branch, ref := s.itemBranch(ctx, prj.ID, item)
	if ref != nil {
		return ref.Result()
	}
	res, err := s.Sync(ctx, prj, item, branch)
	if err != nil {
		return Refuse(err)
	}
	return cli.Found(res)
}

// openVerbs maps the status an item holds when its run opens or updates
// its PR to the table verb that hands the item to review. The amender
// pushes its revision to the PRD's open PR, and its `pr open` returns the
// epic to review.
var openVerbs = map[machine.Kind]map[string]string{
	machine.KindTicket: {machine.InProgress: "pr_open", machine.Repair: "fix_pushed"},
	machine.KindEpic:   {machine.SpecDrafting: "pr_open", machine.PRDAmending: "prd_amended"},
}

func (s *Service) prOpen(ctx context.Context, call *rpc.Call) cli.Result {
	prj, item, ref := s.subject(call)
	if ref != nil {
		return ref.Result()
	}
	it, ref := s.fenced(ctx, call)
	if ref != nil {
		return ref.Result()
	}
	title := call.Inv.Flags["title"]
	branch, ref := s.itemBranch(ctx, prj.ID, item)
	if ref != nil {
		return ref.Result()
	}
	pr, created, err := s.OpenPR(ctx, prj, branch, title, call.Inv.Flags["body"])
	if err != nil {
		return Refuse(err)
	}
	out := map[string]any{"pr": pr.View(), "created": created}
	if it == nil {
		return cli.Found(out)
	}
	verb := openVerbs[it.Kind][it.Status]
	if verb == "" {
		return cli.Found(out)
	}
	p := call.Principal
	res := s.cfg.Machine.Apply(ctx, machine.Request{
		Project: prj.ID, Item: item, Verb: verb, ExpectedStatus: it.Status, RequestID: call.Inv.RequestID,
		Params: map[string]string{"head": pr.Head.SHA, "pr": strconv.Itoa(pr.Number)},
		Actor:  machine.Actor{Name: p.Actor(), RunID: p.RunID, Fenced: p.Kind == cli.PrincipalRun, Epoch: p.Epoch},
	})
	if res.Refusal != nil {
		return res
	}
	out["transition"] = res.Data
	return cli.Found(out)
}

// itemPR is the open or latest PR of the call's item.
func (s *Service) itemPR(ctx context.Context, call *rpc.Call, item string) (config.Project, *PR, *cli.Result) {
	prj, own, ref := s.subject(call)
	if ref != nil {
		res := ref.Result()
		return prj, nil, &res
	}
	if item == "" {
		item = own
	}
	branch, ref := s.itemBranch(ctx, prj.ID, item)
	if ref != nil {
		res := ref.Result()
		return prj, nil, &res
	}
	pr, err := s.FindPR(ctx, prj, branch)
	if err != nil {
		res := Refuse(err)
		return prj, nil, &res
	}
	if pr == nil {
		res := cli.Nothing()
		return prj, nil, &res
	}
	return prj, pr, nil
}

// prShow shows the item's PR. GitHub's PR list carries no mergeable
// state, and the implementer reads it here to learn whether to merge the
// base, so an open PR is read again by number.
func (s *Service) prShow(ctx context.Context, call *rpc.Call) cli.Result {
	prj, pr, res := s.itemPR(ctx, call, "")
	if res != nil {
		return *res
	}
	if pr.State == "open" {
		cur, err := s.GetPR(ctx, prj, pr.Number)
		if err != nil {
			return Refuse(err)
		}
		pr = &cur
	}
	return cli.Found(pr.View())
}

func (s *Service) prFiles(ctx context.Context, call *rpc.Call) cli.Result {
	prj, pr, res := s.itemPR(ctx, call, "")
	if res != nil {
		return *res
	}
	files, err := s.PRFiles(ctx, prj, pr.Number)
	if err != nil {
		return Refuse(err)
	}
	return cli.List(files)
}

func (s *Service) prDiff(ctx context.Context, call *rpc.Call) cli.Result {
	prj, pr, res := s.itemPR(ctx, call, "")
	if res != nil {
		return *res
	}
	diff, cut, err := s.Diff(ctx, prj, pr.Number)
	if err != nil {
		return Refuse(err)
	}
	return cli.Found(map[string]any{"number": pr.Number, "head_sha": pr.Head.SHA, "truncated": cut,
		"author_class": AuthorOther, "diff": diff})
}

func (s *Service) threads(ctx context.Context, call *rpc.Call, item string) cli.Result {
	prj, pr, res := s.itemPR(ctx, call, item)
	if res != nil {
		return *res
	}
	cs, err := s.Threads(ctx, prj, pr.Number)
	if err != nil {
		return Refuse(err)
	}
	return cli.List(cs)
}

func (s *Service) prThreads(ctx context.Context, call *rpc.Call) cli.Result {
	return s.threads(ctx, call, "")
}

// reviewThreads is `review threads <item>`: a reviewer run reads the
// threads of the item it holds. The capability matrix checks the item.
func (s *Service) reviewThreads(ctx context.Context, call *rpc.Call) cli.Result {
	return s.threads(ctx, call, call.Inv.Arg("item"))
}

func (s *Service) prReply(ctx context.Context, call *rpc.Call) cli.Result {
	prj, pr, res := s.itemPR(ctx, call, "")
	if res != nil {
		return *res
	}
	if _, ref := s.fenced(ctx, call); ref != nil {
		return ref.Result()
	}
	var thread int64
	if t := call.Inv.Flags["thread"]; t != "" {
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil || n <= 0 {
			return cli.Reject(cli.CodeUsage, "bad_thread", "--thread is a review comment id")
		}
		thread = n
	}
	// The comment's natural key is the write's idempotency key, or the
	// request id when the call carries none.
	key := call.Request.GetIdempotencyKey()
	if key == "" {
		key = call.Inv.RequestID
	}
	sum := sha256.Sum256([]byte(key))
	c, replayed, err := s.Reply(ctx, prj, pr.Number, thread, call.Inv.Flags["body"], hex.EncodeToString(sum[:8]))
	if err != nil {
		return Refuse(err)
	}
	return cli.Result{Exit: cli.ExitDone, Data: c, Replayed: replayed}
}

func (s *Service) prChecks(ctx context.Context, call *rpc.Call) cli.Result {
	prj, pr, res := s.itemPR(ctx, call, "")
	if res != nil {
		return *res
	}
	rep, err := s.Checks(ctx, prj, pr.Head.SHA)
	if err != nil {
		return Refuse(err)
	}
	return cli.Found(rep)
}

func (s *Service) prLogs(ctx context.Context, call *rpc.Call) cli.Result {
	prj, pr, res := s.itemPR(ctx, call, "")
	if res != nil {
		return *res
	}
	var job int64
	if j := call.Inv.Flags["job"]; j != "" {
		n, err := strconv.ParseInt(j, 10, 64)
		if err != nil || n <= 0 {
			return cli.Reject(cli.CodeUsage, "bad_job", "--job is a CI job id")
		}
		job = n
	}
	logs, err := s.Logs(ctx, prj, pr.Head.SHA, job)
	if err != nil {
		return Refuse(err)
	}
	return cli.List(logs)
}

func (s *Service) prRerun(ctx context.Context, call *rpc.Call) cli.Result {
	prj, pr, res := s.itemPR(ctx, call, "")
	if res != nil {
		return *res
	}
	if _, ref := s.fenced(ctx, call); ref != nil {
		return ref.Result()
	}
	ids, err := s.Rerun(ctx, prj, pr.Head.SHA)
	if err != nil {
		return Refuse(err)
	}
	if len(ids) == 0 {
		return cli.Nothing()
	}
	return cli.Found(map[string]any{"head_sha": pr.Head.SHA, "reran": ids})
}

// ReviewComment posts the body of `review submit` on the item's pull
// request, once by key. The machine calls it after every check of the
// verdict passed and before it records the verdict (machine.Request.Before),
// so a comment GitHub refuses records no verdict.
func (s *Service) ReviewComment(ctx context.Context, project, item, body, key string) *cli.Refusal {
	prj, err := s.project(project)
	if err != nil {
		r := Refusal(err)
		return &r
	}
	branch, ref := s.itemBranch(ctx, project, item)
	if ref != nil {
		return ref
	}
	pr, err := s.FindPR(ctx, prj, branch)
	if err != nil {
		r := Refusal(err)
		return &r
	}
	if pr == nil {
		return &cli.Refusal{Code: cli.CodeNotFound, Rule: "no_pr", Item: item,
			Hint: fmt.Sprintf("%s has no pull request on %s to carry the review comment", item, branch)}
	}
	if _, _, err := s.Reply(ctx, prj, pr.Number, 0, body, key); err != nil {
		r := Refusal(err)
		return &r
	}
	return nil
}

// PRHead reads the head of the item's open pull request, as the
// machine's PRHeadReader. The machine calls it after every other check of
// a `review submit --head` passed (machine.Request.Before), so the verdict
// is compared with the head GitHub holds at the write, not the head the
// reviewer read earlier. The PR is read again by number, as prShow does.
func (s *Service) PRHead(ctx context.Context, project, item string) (string, *cli.Refusal) {
	prj, err := s.project(project)
	if err != nil {
		r := Refusal(err)
		return "", &r
	}
	branch, ref := s.itemBranch(ctx, project, item)
	if ref != nil {
		return "", ref
	}
	pr, err := s.FindPR(ctx, prj, branch)
	if err == nil && pr != nil && pr.State == "open" {
		var cur PR
		if cur, err = s.GetPR(ctx, prj, pr.Number); err == nil {
			pr = &cur
		}
	}
	if err != nil {
		r := Refusal(err)
		return "", &r
	}
	if pr == nil || pr.State != "open" {
		return "", &cli.Refusal{Code: cli.CodeNotFound, Rule: "no_open_pr", Item: item,
			Hint: fmt.Sprintf("%s has no open pull request on %s to hold the reviewed head", item, branch)}
	}
	return pr.Head.SHA, nil
}

// SupersedeEvidence reads the evidence of `work supersede` back, as the
// machine's SupersedeChecker. The machine calls it after every other check
// of the close passed (machine.Request.Before), so a refused or failed
// read records nothing. An open pull request on the ticket's branch
// refuses first, whatever the evidence: it holds work, and the close
// would leave it with no ticket to merge against. A commit must be one
// the base reaches, and an ADR a file under ADRDir on the base. Both are
// read in the mirror after a fetch of the base, so a commit that merged a
// minute ago counts. The machine checks a done item itself.
func (s *Service) SupersedeEvidence(ctx context.Context, project, item, kind, evidence string) *cli.Refusal {
	if kind == machine.EvidenceADR && (!strings.HasPrefix(evidence, ADRDir) || !strings.HasSuffix(evidence, ".md")) {
		return &cli.Refusal{Code: cli.CodeUsage, Rule: "not_an_adr", Item: item,
			Hint: fmt.Sprintf("--adr names a Markdown file under %s", ADRDir)}
	}
	prj, err := s.project(project)
	if err != nil {
		r := Refusal(err)
		return &r
	}
	branch, ref := s.itemBranch(ctx, project, item)
	if ref != nil {
		return ref
	}
	pr, err := s.FindPR(ctx, prj, branch)
	if err != nil {
		r := Refusal(err)
		return &r
	}
	if pr != nil && pr.State == "open" {
		return &cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "open_pr", Item: item,
			Hint: fmt.Sprintf("%s holds the open pull request #%d, so it has work in flight; close the pull request first", item, pr.Number)}
	}
	if kind == machine.EvidenceDone {
		return nil
	}
	base, tip, err := s.SyncBase(ctx, prj)
	if err != nil {
		r := Refusal(err)
		return &r
	}
	m := s.Mirror(prj)
	switch kind {
	case machine.EvidenceCommit:
		sha := m.Resolve(ctx, evidence)
		ok := false
		if sha != "" && tip != "" {
			if ok, err = m.Reaches(ctx, tip, sha); err != nil {
				return cli.InternalRefusal(err)
			}
		}
		if !ok {
			return &cli.Refusal{Code: cli.CodeNotFound, Rule: "commit_not_on_base", Item: item,
				Hint: fmt.Sprintf("%s does not reach commit %s; name a commit the base branch holds", base, evidence)}
		}
	case machine.EvidenceADR:
		if _, err := m.Show(ctx, tip, evidence); errors.Is(err, fs.ErrNotExist) {
			return &cli.Refusal{Code: cli.CodeNotFound, Rule: "adr_not_on_base", Item: item,
				Hint: fmt.Sprintf("%s holds no %s; an ADR counts once it merged", base, evidence)}
		} else if err != nil {
			return cli.InternalRefusal(err)
		}
	}
	return nil
}
