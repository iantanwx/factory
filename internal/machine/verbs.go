package machine

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/shape"
	"github.com/iantanwx/loop/internal/store"
)

// Register sets the handlers of the machine's CLI verbs. The reactor, the
// scheduler, admission and the inbox call Apply, Claim, MergeObserved and
// HeadChanged instead.
func (m *Machine) Register(s *rpc.Server) error {
	for path, h := range m.Handlers() {
		if err := s.Handle(path, h); err != nil {
			return err
		}
	}
	return nil
}

// Handlers returns the handlers of the machine's CLI verbs, keyed by path.
func (m *Machine) Handlers() map[string]rpc.Handler {
	return map[string]rpc.Handler{
		"work show":       m.workShow,
		"work list":       m.workList,
		"work note":       m.verb("note", "item"),
		"work edit":       m.workEdit,
		"work cancel":     m.workCancel,
		"work block":      m.workBlock,
		"work unblock":    m.workUnblock,
		"work shape":      m.workShape,
		"work ask":        m.workAsk,
		"work duplicate":  m.workDuplicate,
		"work supersede":  m.workSupersede,
		"review submit":   m.reviewSubmit,
		"review rounds":   m.reviewRounds,
		"decision fire":   m.decisionMove("fire", "why", "why"),
		"decision defer":  m.decisionMove("defer", "why", "why"),
		"decision moot":   m.decisionMove("moot", "note", "note"),
		"decision settle": m.decisionSettle,
	}
}

// ownProject is the hint of a run that names another project.
const ownProject = "a run works only in its own project"

// request builds the machine request of one CLI write on target.
func request(call *rpc.Call, prj, target, verb string, params map[string]string) Request {
	p, inv := call.Principal, call.Inv
	req := Request{
		Project: prj, Item: target, Verb: verb, Params: params, Command: inv.Verb.Path,
		RequestID: inv.RequestID, Actor: Actor{Name: p.Actor(), RunID: p.RunID},
	}
	held := (p.Kind == cli.PrincipalRun || p.Kind == cli.PrincipalRoom) && target == p.Item && prj == p.Project
	if held {
		req.Actor.Fenced, req.Actor.Epoch = true, p.Epoch
	}
	// The CLI reads the status of the verb's item argument, or of the
	// token's item when the verb names none. Only that read is this
	// target's expected status.
	if inv.Arg("item") == target || inv.Arg("item") == "" && target == p.Item {
		req.ExpectedStatus = inv.Extra[cli.ExtraExpectedStatus]
	}
	if key := call.Request.GetIdempotencyKey(); key != "" && p.RunID != "" && inv.Verb.Write {
		req.Key = &Key{Key: key, Fingerprint: store.Fingerprint{
			RunID: p.RunID, Verb: inv.Verb.Path, ArgsHash: cli.ArgsHash(inv.Verb.Path, inv.Args, inv.Flags),
			ContentHash: inv.Extra[cli.ExtraContentHash],
		}}
	}
	return req
}

// borrowed refuses a run's write on an item its token does not hold. The
// capability check lets a run reach such an item in one case only: the
// fixer reads and notes the item of the escalation it routes. So the only
// writes a run makes on a borrowed item are a note, and the fixer's
// supersede when a finding's answer leaves the ticket no work.
func borrowed(call *rpc.Call, target string) *cli.Refusal {
	p, path := call.Principal, call.Inv.Verb.Path
	if p.Kind != cli.PrincipalRun || p.Item == target || path == "work note" ||
		path == "work supersede" && p.Stage == config.StageFixer {
		return nil
	}
	return &cli.Refusal{Code: cli.CodeCapabilityDenied, Rule: "borrowed_item", Item: target,
		Hint: fmt.Sprintf("%s does not hold %s; it may only note it with `loop work note`", p.Actor(), target)}
}

// verb is the handler of a CLI write that maps to one table verb on the
// item named by the argument arg.
func (m *Machine) verb(verb, arg string) rpc.Handler {
	return func(ctx context.Context, call *rpc.Call) cli.Result {
		prj, r := call.Project(ownProject)
		if r != nil {
			return r.Result()
		}
		target := call.Inv.Arg(arg)
		if r := borrowed(call, target); r != nil {
			return r.Result()
		}
		params := map[string]string{}
		if b := call.Inv.Flags["body"]; b != "" {
			params["body"] = b
		}
		return m.Apply(ctx, request(call, prj, target, verb, params))
	}
}

// commentKey is the natural key of the comment a write posts: its
// idempotency key, or its request id when it carries none.
func commentKey(call *rpc.Call) string {
	key := call.Request.GetIdempotencyKey()
	if key == "" {
		key = call.Inv.RequestID
	}
	sum := sha256.Sum256([]byte("review\x00" + key))
	return hex.EncodeToString(sum[:8])
}

// reviewSubmit records a verdict. A body is the review comment: the
// server checks it for a secret, posts it on the item's pull request once
// every other check of the write passed, and then records it as the
// review fact under the same compare-and-set as the verdict. A comment
// GitHub refuses records nothing. A code pass first needs green CI at the
// head it names (alpha pr-comment.ts 74-91): the pass writes the reviewed
// head that loop/eligible is posted on, so a pass at red or pending CI
// records nothing and posts no comment. A reject needs no green build, and
// a document's pass reads no CI.
func (m *Machine) reviewSubmit(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := call.Project(ownProject)
	if r != nil {
		return r.Result()
	}
	item := call.Inv.Arg("item")
	if r := borrowed(call, item); r != nil {
		return r.Result()
	}
	verb := "review_pass"
	if call.Inv.Flags["verdict"] == "reject" {
		verb = "review_reject"
	}
	params := map[string]string{}
	head := call.Inv.Flags["head"]
	if head != "" {
		params["head"] = head
	}
	body := call.Inv.Flags["body"]
	// The comment holds the requirements, so the operator reads them at
	// the sign-off.
	adrNeeded := strings.TrimSpace(call.Inv.Flags["adr-needed"])
	if adrNeeded != "" {
		params["adr_needed"] = adrNeeded
		body = strings.TrimSpace(body + "\n\nRequirements that pass the ADR test: " + adrNeeded)
	}
	if body != "" {
		params["body"] = body
	}
	req := request(call, prj, item, verb, params)
	key := commentKey(call)
	req.Before = func(ctx context.Context, it Item) *cli.Refusal {
		if adrNeeded != "" && (verb != "review_pass" || it.Kind != KindEpic || it.Status != PRDReview) {
			return refusedAt(&cli.Refusal{Code: cli.CodeUsage, Rule: "adr_needed_scope",
				Hint: "--adr-needed goes only on a pass of a PRD in prd_review; submit the verdict again without it"}, item, it)
		}
		// The live head first: a stale head is refused head_changed, not
		// for CI at a commit the PR no longer holds.
		if head != "" && m.prHead != nil {
			if r := m.headHeld(ctx, prj, item, head); r != nil {
				return refusedAt(r, item, it)
			}
		}
		if verb == "review_pass" && it.Kind == KindTicket && m.passGate != nil {
			if r := m.passGate(ctx, prj, item, params["head"]); r != nil {
				return refusedAt(r, item, it)
			}
		}
		if r := contractsNamed(it, body); r != nil {
			return refusedAt(r, item, it)
		}
		if body != "" && m.reviewComment != nil {
			return m.reviewComment(ctx, prj, item, body, key)
		}
		return nil
	}
	return m.Apply(ctx, req)
}

// contractsNamed refuses a review of a shaped ticket whose body omits a
// contract id of the ticket's task text (spec factorio-wnl, C13). A ticket
// with no shape fact was never checked at its creation, so its review is
// not checked either.
func contractsNamed(it Item, review string) *cli.Refusal {
	s, ok := shape.ByName(it.Fact(FactShape))
	if it.Kind != KindTicket || !ok {
		return nil
	}
	missing := shape.Unnamed(shape.ContractIDs(s, it.Body), review)
	if len(missing) == 0 {
		return nil
	}
	return &cli.Refusal{Code: cli.CodeShapeRefused, Rule: "contract_unnamed",
		Hint: fmt.Sprintf("the review does not name %s; write one line for each contract id of the ticket, and submit again",
			strings.Join(missing, ", "))}
}

// refusedAt names the item, its status and its next moves in a refusal
// from a Before hook, which the hook's reader leaves out.
func refusedAt(r *cli.Refusal, item string, it Item) *cli.Refusal {
	r.Item, r.CurrentStatus = item, it.Status
	if r.AllowedNext == nil {
		r.AllowedNext = it.AllowedNext
	}
	return r
}

// headHeld refuses a verdict at a head the item's pull request no longer
// holds. The reviewer read the head before it reviewed, and a push since
// then puts unread text under the verdict. Ported from candidateFeedback
// in scripts/sdlc/github-health.ts:46-70 (alpha).
func (m *Machine) headHeld(ctx context.Context, prj, item, head string) *cli.Refusal {
	live, r := m.prHead(ctx, prj, item)
	if r != nil {
		return r
	}
	if live == head {
		return nil
	}
	return &cli.Refusal{Code: cli.CodeStatusChanged, Rule: "head_changed", Item: item,
		Hint: fmt.Sprintf("the pull request of %s is at %s, not at %s; read the new head with loop pr show and review it",
			item, live, head)}
}

// workEdit replaces an item's title or body. Beads keeps the text the item
// was created with, and the edit is a fact that replaces it: the item's
// title, TaskText and so the retry identity (R10) read the edit. A new
// body holds the shape that the item's shape fact names, so an item keeps
// the shape it was checked against at its creation. A new ticket body
// calls no host verb, shaped or not.
func (m *Machine) workEdit(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := call.Project(ownProject)
	if r != nil {
		return r.Result()
	}
	item := call.Inv.Arg("item")
	if r := borrowed(call, item); r != nil {
		return r.Result()
	}
	params := map[string]string{}
	if t, ok := call.Inv.Flags["title"]; ok {
		if strings.TrimSpace(t) == "" {
			return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: "empty_title", Item: item, Hint: "--title names the item; it cannot be empty"})
		}
		params["title"] = t
	}
	if b, ok := call.Inv.Flags["body"]; ok && b != "" {
		params["body"] = b
	}
	if len(params) == 0 {
		return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: "nothing_to_edit", Item: item, Hint: "name the new text with --title, --body or both"})
	}
	req := request(call, prj, item, "edit", params)
	if body, ok := params["body"]; ok {
		req.Before = func(_ context.Context, it Item) *cli.Refusal {
			if s, ok := shape.ByName(it.Fact(FactShape)); ok {
				if r := BodyRefusal(s, item, body); r != nil {
					return refusedAt(r, item, it)
				}
			}
			if it.Kind == KindTicket {
				if r := HostVerbRefusal(item, body); r != nil {
					return refusedAt(r, item, it)
				}
			}
			return nil
		}
	}
	return m.Apply(ctx, req)
}

// BodyRefusal refuses a body that does not hold s, with SHAPE_REFUSED,
// the rule <shape>_shape and every fault. It is nil for a body that holds
// s.
func BodyRefusal(s shape.Shape, item, body string) *cli.Refusal {
	faults := shape.CheckBody(s, body)
	if len(faults) == 0 {
		return nil
	}
	text := make([]string, len(faults))
	for i, f := range faults {
		text[i] = f.String()
	}
	return &cli.Refusal{Code: cli.CodeShapeRefused, Rule: s.Name + "_shape", Item: item,
		Hint: fmt.Sprintf("the body does not hold the %s shape: %s. Write each section as a `## ` heading, and send the body again",
			s.Name, strings.Join(text, "; "))}
}

// hostVerbs are the verb paths that need the host-admin credential, read
// once from the capability matrix.
var hostVerbs = sync.OnceValue(func() []string {
	m, _ := cli.Surface().CapabilityMatrix()
	return m.HostVerbs
})

// HostVerbRefusal refuses a ticket body that tells a run to call a host verb
// under Done means or Contracts to prove, with SHAPE_REFUSED, the rule
// host_verb_in_ticket and every call. The server refuses a host verb from a
// run's checkout (factorio-bxv0). It is nil for a body that calls none.
func HostVerbRefusal(item, body string) *cli.Refusal {
	calls := shape.HostVerbCalls(body, hostVerbs())
	if len(calls) == 0 {
		return nil
	}
	text := make([]string, len(calls))
	for i, c := range calls {
		text[i] = fmt.Sprintf("`loop %s` on line %d under %s", c.Verb, c.Line, c.Section)
	}
	return &cli.Refusal{Code: cli.CodeShapeRefused, Rule: "host_verb_in_ticket", Item: item,
		Hint: fmt.Sprintf("the body tells a run to call a host verb: %s. A run cannot call a host verb, "+
			"so prove the behaviour with a test, and send the body again", strings.Join(text, "; "))}
}

// askQuestion is one top-level list item of an ask's body. An indented
// item is part of the question above it.
var askQuestion = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+\S`)

// maxQuestions bounds one ask (scripts/sdlc/bugs-report-ask.ts:42, alpha):
// a reporter is a person, and answers a short ask.
const maxQuestions = 3

// workAsk asks a bug's reporter one to three questions. The table counts
// the asks and refuses the third.
func (m *Machine) workAsk(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := call.Project(ownProject)
	if r != nil {
		return r.Result()
	}
	bug, body := call.Inv.Arg("item"), strings.TrimSpace(call.Inv.Flags["body"])
	if r := borrowed(call, bug); r != nil {
		return r.Result()
	}
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if askQuestion.MatchString(line) {
			n++
		}
	}
	if n == 0 || n > maxQuestions {
		return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: "bad_questions", Item: bug,
			Hint: fmt.Sprintf("an ask holds one to three questions, each a top-level list item, and this body holds %d", n)})
	}
	return m.Apply(ctx, request(call, prj, bug, "ask", map[string]string{"body": body}))
}

// workDuplicate closes a bug as a duplicate of another item. With
// --unbuilt the bug waits for the item instead, a ticket or an epic that
// is not terminal, because only those build a change (spec Decision 11).
func (m *Machine) workDuplicate(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := call.Project(ownProject)
	if r != nil {
		return r.Result()
	}
	bug, of := call.Inv.Arg("item"), strings.TrimSpace(call.Inv.Flags["of"])
	if r := borrowed(call, bug); r != nil {
		return r.Result()
	}
	if of == "" || of == bug {
		return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: "bad_original", Item: bug, Hint: "--of names another item, the one the bug repeats"})
	}
	it, r := m.Read(ctx, prj, of)
	if r != nil {
		return r.Result()
	}
	if call.Inv.Flags["unbuilt"] != "true" {
		return m.Apply(ctx, request(call, prj, bug, "duplicate", map[string]string{"of": of}))
	}
	t, _ := TableFor(it.Kind)
	switch {
	case it.Kind != KindTicket && it.Kind != KindEpic:
		return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: "build_kind", Item: of,
			Hint: fmt.Sprintf("%s is a %s; a bug waits only for a ticket or an epic, which builds a change", of, it.Kind)})
	case t.Terminal(it.Status):
		return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: "build_terminal", Item: of,
			Hint: fmt.Sprintf("%s is %s already, so it builds nothing more; close the bug without --unbuilt, or name the build in flight", of, it.Status)})
	}
	return m.Apply(ctx, request(call, prj, bug, VerbAwaitBuild, map[string]string{"of": of}))
}

// workCancel closes an escalated item, or a bug the reproducer routes as
// a loop fault. The reason says why, and the item keeps it for a person.
func (m *Machine) workCancel(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := call.Project(ownProject)
	if r != nil {
		return r.Result()
	}
	item := call.Inv.Arg("item")
	if r := borrowed(call, item); r != nil {
		return r.Result()
	}
	reason := strings.TrimSpace(call.Inv.Flags["reason"])
	if reason == "" {
		return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: "no_reason", Item: item,
			Hint: "--reason says why the item closes; a person reads it"})
	}
	return m.Apply(ctx, request(call, prj, item, "cancel", map[string]string{"reason": reason}))
}

// Kill points of work block and work unblock --by, between their two
// writes.
const (
	pointBlockMoved  = "block_moved"
	pointEdgeRemoved = "edge_removed"
)

// workBlock parks a todo ticket in backlog on a blocker, or adds a blocker
// to a backlog ticket. The move records the blocker as pending_block, then
// the edge goes in, so a crash between the two leaves the ticket held, and
// a replay adds the edge (spec factorio-9wh Decision 4).
func (m *Machine) workBlock(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := call.Project(ownProject)
	if r != nil {
		return r.Result()
	}
	item, by := call.Inv.Arg("item"), strings.TrimSpace(call.Inv.Flags["by"])
	if r := borrowed(call, item); r != nil {
		return r.Result()
	}
	unlock := m.lock(prj, item)
	defer unlock()
	if r := m.CheckBlockers(ctx, prj, item, []string{by}); r != nil {
		return r.Result()
	}
	bc, it, deps, r := m.withEdges(ctx, prj, item)
	if r != nil {
		return r.Result()
	}
	if r := m.blockPending(ctx, prj, it, deps, by); r != nil {
		return r.Result()
	}
	req := request(call, prj, item, "block", map[string]string{"by": by})
	res := m.apply(ctx, &req)
	if res.Refusal != nil {
		return res
	}
	if m.stop(pointBlockMoved) {
		return cli.Internal(errStopped)
	}
	if err := bc.AddDependency(ctx, item, by, beadsx.DepBlocks); err != nil {
		return beadsRefusal(item, err).Result()
	}
	return res
}

// UnblockResult is what work unblock answers.
type UnblockResult struct {
	Item        string `json:"item"`
	Status      string `json:"status"`
	EdgeRemoved bool   `json:"edge_removed"`
}

// workUnblock deletes the blocks edge to --by, then moves a backlog
// ticket to todo when no open blocker remains, or keeps it in backlog.
// With no --by it deletes no edge, and frees only a ticket with no open
// blocker. A todo ticket with --by is free already, so the call deletes
// its edge to --by, if any, and moves nothing (spec factorio-9wh
// Decisions 6 and 23). The delete goes before the move, so a crash
// between the two leaves the ticket in backlog. Any other kind takes its
// table's unblock row, and keeps its edges.
func (m *Machine) workUnblock(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := call.Project(ownProject)
	if r != nil {
		return r.Result()
	}
	item, by := call.Inv.Arg("item"), strings.TrimSpace(call.Inv.Flags["by"])
	if r := borrowed(call, item); r != nil {
		return r.Result()
	}
	unlock := m.lock(prj, item)
	defer unlock()
	bc, it, deps, r := m.withEdges(ctx, prj, item)
	if r != nil {
		return r.Result()
	}
	req := request(call, prj, item, VerbUnblock, map[string]string{})
	if it.Kind != KindTicket || it.Status != Backlog && (it.Status != Todo || by == "") {
		return m.apply(ctx, &req) // an epic's rows, or the table refuses every other state
	}
	open, r := m.openBlockers(ctx, prj, it, deps, by)
	if r != nil {
		return r.Result()
	}
	if by == "" && len(open) > 0 {
		return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: "blocker_open", Item: item, CurrentStatus: it.Status,
			Hint: fmt.Sprintf("%s still waits on %s; name each one with --by to end its block", item, strings.Join(open, ", "))})
	}
	if by != "" {
		if r := m.blockPending(ctx, prj, it, deps, by); r != nil {
			return r.Result()
		}
	}
	removed := by != "" && hasEdge(deps, by)
	if removed {
		if err := bc.RemoveDependency(ctx, item, by, beadsx.DepBlocks); err != nil {
			return beadsRefusal(item, err).Result()
		}
		if m.stop(pointEdgeRemoved) {
			return cli.Internal(errStopped)
		}
	}
	if it.Status == Todo {
		return cli.Found(UnblockResult{Item: item, Status: Todo, EdgeRemoved: removed})
	}
	status := Todo
	if len(open) > 0 {
		status = Backlog
		req.Params["open"], req.Params["by"] = "true", by
	}
	if res := m.apply(ctx, &req); res.Refusal != nil {
		return res
	}
	return cli.Found(UnblockResult{Item: item, Status: status, EdgeRemoved: removed})
}

// withEdges reads a ticket and its dependencies.
func (m *Machine) withEdges(ctx context.Context, prj, item string) (beadsx.Client, Item, []beadsx.Dependency, *cli.Refusal) {
	bc, err := m.cfg.Beads(prj)
	if err != nil {
		return nil, Item{}, nil, cli.InternalRefusal(err)
	}
	it, r := m.Read(ctx, prj, item)
	if r != nil {
		return nil, Item{}, nil, r
	}
	deps, err := bc.Dependencies(ctx, item)
	if err != nil {
		return nil, Item{}, nil, beadsRefusal(item, err)
	}
	return bc, it, deps, nil
}

func hasEdge(deps []beadsx.Dependency, to string) bool {
	return slices.ContainsFunc(deps, func(d beadsx.Dependency) bool { return d.Kind == beadsx.DepBlocks && d.ID == to })
}

// openBlockers lists the open blockers of a ticket other than by: each
// item that is not done, and that a blocks edge or pending_block names
// (spec factorio-9wh Decision 22). An edge to a canceled item is open, so
// a person names it with --by.
func (m *Machine) openBlockers(ctx context.Context, prj string, it Item, deps []beadsx.Dependency, by string) ([]string, *cli.Refusal) {
	var open []string
	for _, d := range deps {
		if d.Kind == beadsx.DepBlocks && d.ID != by && d.Status != Done {
			open = append(open, d.ID)
		}
	}
	if p := it.Fact(FactPendingBlock); p != "" && p != by && !hasEdge(deps, p) {
		pi, r := m.Read(ctx, prj, p)
		if r != nil {
			return nil, r
		}
		if pi.Status != Done {
			open = append(open, p)
		}
	}
	return open, nil
}

// blockPending refuses work block and work unblock --by another blocker
// while pending_block names an item the ticket holds no edge to: the work
// block on that item stopped before its edge, and a later call must not
// lose it. The hint names the call that ends the case.
func (m *Machine) blockPending(ctx context.Context, prj string, it Item, deps []beadsx.Dependency, by string) *cli.Refusal {
	p := it.Fact(FactPendingBlock)
	if p == "" || p == by || hasEdge(deps, p) {
		return nil
	}
	pi, r := m.Read(ctx, prj, p)
	if r != nil {
		return r
	}
	hint := fmt.Sprintf("the work block of %s on %s stopped before its edge; run `loop work block %s --by %s` again first",
		it.ID, p, it.ID, p)
	if t, _ := TableFor(pi.Kind); t.Terminal(pi.Status) {
		hint = fmt.Sprintf("the work block of %s on %s stopped before its edge, and %s is %s; run `loop work unblock %s --by %s` first",
			it.ID, p, p, pi.Status, it.ID, p)
	}
	return &cli.Refusal{Code: cli.CodeUsage, Rule: "block_pending", Item: it.ID, CurrentStatus: it.Status, Hint: hint}
}

// SupersedeChecker reads the evidence of `work supersede` back from GitHub
// and the project's base branch. kind is an Evidence constant. It refuses
// when the ticket's branch holds an open pull request, whatever the
// evidence.
type SupersedeChecker func(ctx context.Context, project, item, kind, ref string) *cli.Refusal

// SetSupersedeChecker sets the reader of supersede evidence. The GitHub
// setup sets it before the socket binds.
func (m *Machine) SetSupersedeChecker(f SupersedeChecker) { m.supersedeCheck = f }

// workSupersede closes a ticket that has no work left, because a commit on
// the base, a done item or an ADR on the base already answers it (alpha
// 4fc932ebd, 161a2158a). Without it, an implementer that finds its change
// on main has no exit: the rubric routes that finding to the loop, so its
// escalation is refused, and the ticket fails twice and is held. The
// table checks the state, the lease and the evidence's shape first, and
// the GitHub reads run last, inside the item's lock, so a refused read
// records nothing.
func (m *Machine) workSupersede(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := call.Project(ownProject)
	if r != nil {
		return r.Result()
	}
	item := call.Inv.Arg("item")
	if r := borrowed(call, item); r != nil {
		return r.Result()
	}
	var kind, ref string
	for _, k := range []string{EvidenceCommit, EvidenceDone, EvidenceADR} {
		if v := strings.TrimSpace(call.Inv.Flags[k]); v != "" {
			if kind != "" {
				kind = ""
				break
			}
			kind, ref = k, v
		}
	}
	if kind == "" {
		return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: "bad_evidence", Item: item,
			Hint: "name the evidence with exactly one of --commit, --done or --adr"})
	}
	reason := strings.TrimSpace(call.Inv.Flags["reason"])
	if reason == "" {
		return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: "no_reason", Item: item,
			Hint: "--reason says how the evidence answers the ticket; a person reads it"})
	}
	req := request(call, prj, item, VerbSupersede, map[string]string{"evidence": kind, "ref": ref, "reason": reason})
	req.Before = func(ctx context.Context, _ Item) *cli.Refusal {
		if m.supersedeCheck == nil {
			return cli.InternalRefusal(errors.New("machine: the server reads no GitHub, so it cannot check the evidence of a supersede"))
		}
		return m.supersedeCheck(ctx, prj, item, kind, ref)
	}
	return m.Apply(ctx, req)
}

// ShapeResult is what work shape answers.
type ShapeResult struct {
	Bug    string `json:"bug"`
	Ticket string `json:"ticket"`
	Status string `json:"status"`
}

// workShape shapes a bug into a ticket: it creates the ticket, which
// names the bug, then closes the bug as shaped with the ticket's id. The
// ticket's create key is the bug, so a retry after a crash between the
// two writes finds the ticket it made. The ticket keeps --priority as a
// fact. The body holds the Bug shape, and the ticket records it as its
// shape fact. With --blocked-by the ticket waits in backlog on each
// blocker, which is checked before any write.
func (m *Machine) workShape(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := call.Project(ownProject)
	if r != nil {
		return r.Result()
	}
	bug := call.Inv.Arg("item")
	if r := borrowed(call, bug); r != nil {
		return r.Result()
	}
	title, body := strings.TrimSpace(call.Inv.Flags["title"]), call.Inv.Flags["body"]
	if title == "" {
		return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: "no_title", Item: bug, Hint: "--title names the ticket"})
	}
	priority := strings.TrimSpace(call.Inv.Flags["priority"])
	if !slices.Contains(priorities, priority) {
		return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: "no_priority", Item: bug,
			Hint: "--priority is 1, 2, 3 or 4, by the reproducer runbook, section 4"})
	}
	unlock := m.lock(prj, bug)
	defer unlock()
	it, r := m.Show(ctx, prj, bug)
	if r != nil {
		return r.Result()
	}
	t, _ := TableFor(it.Kind)
	blockers := ParseBlockers(call.Inv.Flags["blocked-by"])
	switch {
	case it.Kind != KindBug:
		return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: "not_a_bug", Item: bug, Hint: fmt.Sprintf("%s is a %s; only a bug is shaped", bug, it.Kind)})
	case !t.Allows("shape", it.Status):
		return cli.Refuse(cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "no_edge", Item: bug, CurrentStatus: it.Status,
			AllowedNext: it.AllowedNext, Hint: fmt.Sprintf("a bug in %s has no shape move", it.Status)})
	}
	if r := BodyRefusal(shape.Bug, bug, body); r != nil {
		return refusedAt(r, bug, it).Result()
	}
	if r := m.CheckBlockers(ctx, prj, "", blockers); r != nil {
		return r.Result()
	}
	ticket, r := m.shapedTicket(ctx, prj, bug, title, body, priority, blockers, call)
	if r != nil {
		return r.Result()
	}
	req := request(call, prj, bug, "shape", map[string]string{"ticket": ticket})
	res := m.apply(ctx, &req)
	if res.Refusal != nil {
		return res
	}
	return cli.Found(ShapeResult{Bug: bug, Ticket: ticket, Status: Shaped})
}

// priorities are the values of work shape --priority (Decision 8): the
// fact holds one, and the native beads priority stays 2.
var priorities = []string{"1", "2", "3", "4"}

// shapedTicket finds the ticket a bug was shaped into, or creates it in
// todo, or in backlog with an edge to each blocker. The caller holds the
// bug's lock.
func (m *Machine) shapedTicket(ctx context.Context, prj, bug, title, body, priority string, blockers []string, call *rpc.Call) (string, *cli.Refusal) {
	key := shapeKey(bug)
	found, r := m.List(ctx, prj, ListFilter{Kind: KindTicket, Facts: map[string]string{FactCreateKey: key}})
	if r != nil {
		return "", r
	}
	var tk Item
	if len(found) > 0 {
		tk = found[0]
	} else {
		id, r := m.CreateBead(ctx, CreateRequest{Project: prj, Kind: KindTicket, Title: title, Description: body,
			Facts: map[string]string{FactCreateKey: key, FactBug: bug, FactPriority: priority, FactShape: shape.Bug.Name}})
		if r != nil {
			return "", r
		}
		if tk, r = m.Read(ctx, prj, id); r != nil {
			return "", r
		}
	}
	if tk.Status == Initial {
		params := map[string]string{}
		if len(blockers) > 0 {
			if r := m.addBlockers(ctx, prj, tk.ID, blockers); r != nil {
				return "", r
			}
			params["blocked"] = "true"
		}
		p := call.Principal
		res := m.Apply(ctx, Request{Project: prj, Item: tk.ID, Verb: VerbCreate, Params: params, ExpectedStatus: Initial,
			RequestID: call.Inv.RequestID, Actor: Actor{Name: p.Actor(), RunID: p.RunID}})
		if res.Refusal != nil {
			return "", res.Refusal
		}
	}
	return tk.ID, nil
}

// decisionMove is the handler of a decision verb that records one text:
// the flag's value goes to the row as param.
func (m *Machine) decisionMove(verb, flag, param string) rpc.Handler {
	return func(ctx context.Context, call *rpc.Call) cli.Result {
		if r := borrowed(call, call.Inv.Arg("decision")); r != nil {
			return r.Result()
		}
		return m.decisionText(ctx, call, verb, flag, param)
	}
}

// decisionSettle stores the note as the answer, or as the close note of a
// drop.
func (m *Machine) decisionSettle(ctx context.Context, call *rpc.Call) cli.Result {
	if call.Inv.Flags["dropped"] == "true" {
		return m.decisionText(ctx, call, "drop", "note", "note")
	}
	return m.decisionText(ctx, call, VerbAnswer, "note", "answer")
}

// decisionText refuses a decision move with no text before it reaches the
// table, so the hint names the flag and not the row's parameter.
func (m *Machine) decisionText(ctx context.Context, call *rpc.Call, verb, flag, param string) cli.Result {
	prj, r := call.Project(ownProject)
	if r != nil {
		return r.Result()
	}
	target := call.Inv.Arg("decision")
	text := strings.TrimSpace(call.Inv.Flags[flag])
	if text == "" {
		return cli.Refuse(cli.Refusal{Code: cli.CodeUsage, Rule: "no_" + flag, Item: target,
			Hint: fmt.Sprintf("%s needs --%s, the text the decision keeps; a person reads it", call.Inv.Verb.Path, flag)})
	}
	return m.Apply(ctx, request(call, prj, target, verb, map[string]string{param: text}))
}

func (m *Machine) workShow(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := call.Project(ownProject)
	if r != nil {
		return r.Result()
	}
	it, r := m.Show(ctx, prj, call.Inv.Arg("item"))
	if r != nil {
		return r.Result()
	}
	if m.wait != nil {
		if w, err := m.wait(ctx, prj, it.ID); err != nil {
			it.WaitError = err.Error()
		} else {
			it.Wait = &w
		}
	}
	if m.retro != nil {
		if r, err := m.retro(ctx, prj, it.ID); err != nil {
			it.RetroError = err.Error()
		} else {
			it.RetroOnItem = r
		}
	}
	return cli.Found(it)
}

func (m *Machine) workList(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := call.Project(ownProject)
	if r != nil {
		return r.Result()
	}
	f := ListFilter{Kind: Kind(call.Inv.Flags["kind"]), Status: call.Inv.Flags["status"]}
	if ep := call.Inv.Flags["epic"]; ep != "" {
		f.Facts = map[string]string{FactEpic: ep}
	}
	items, r := m.List(ctx, prj, f)
	if r != nil {
		return r.Result()
	}
	// A decision is no work: `decision list` lists it.
	return cli.List(slices.DeleteFunc(items, func(it Item) bool { return it.Kind == KindDecision }))
}

// ReviewCommenter posts a review's body on the item's pull request. key is
// the comment's natural key, so a retry posts no second comment.
type ReviewCommenter func(ctx context.Context, project, item, body, key string) *cli.Refusal

// SetReviewCommenter sets the poster of review comments. The GitHub setup
// sets it before the socket binds.
func (m *Machine) SetReviewCommenter(f ReviewCommenter) { m.reviewComment = f }

// PRHeadReader reads the head commit of the item's open pull request. It
// refuses when the item has no open pull request.
type PRHeadReader func(ctx context.Context, project, item string) (string, *cli.Refusal)

// SetPRHeadReader sets the reader of live pull request heads. The GitHub
// setup sets it before the socket binds.
func (m *Machine) SetPRHeadReader(f PRHeadReader) { m.prHead = f }

// WaitReader reads why an item waits: its lease, its holds, its counted
// failures and the open alerts that name it.
type WaitReader func(ctx context.Context, project, item string) (Wait, error)

// SetWaitReader sets the reader of work show's wait. The server sets it
// before the socket binds.
func (m *Machine) SetWaitReader(f WaitReader) { m.wait = f }

// RetroReader reads what the retro keeps on an item: its signals and its
// held learnings.
type RetroReader func(ctx context.Context, project, item string) (RetroOnItem, error)

// SetRetroReader sets the reader of work show's signals and held
// learnings. The server sets it before the socket binds, as it sets the
// wait reader.
func (m *Machine) SetRetroReader(f RetroReader) { m.retro = f }

// PassGate reads CI at head, the head a code review passes. It refuses
// unless every required check is green there.
type PassGate func(ctx context.Context, project, item, head string) *cli.Refusal

// SetPassGate sets the reader of CI for a code pass. The GitHub setup sets
// it before the socket binds.
func (m *Machine) SetPassGate(f PassGate) { m.passGate = f }

// TaskText is the title and the body of an item's task: the text work
// edit wrote, or the bead's own, and then an answer that amends the task.
// Admission hashes it for the retry identity (R10), so an edit, or a
// person's amendment through the inbox, re-admits a twice-failed
// item.
func TaskText(is beadsx.Issue) (title, body string) {
	title, body = is.Title, is.Description
	if t := is.Metadata[factPrefix+FactTitle]; t != "" {
		title = t
	}
	if b := is.Metadata[factPrefix+FactBody]; b != "" {
		body = b
	}
	if a := is.Metadata[factPrefix+FactAmendment]; a != "" {
		body += "\n\n" + a
	}
	return title, body
}

// shapeKey is the create key of the ticket a bug is shaped into.
func shapeKey(bug string) string { return "bug/" + bug }

// Round is one review verdict of an item, as `review rounds` lists it.
type Round struct {
	Round   int    `json:"round"`
	Verdict string `json:"verdict"` // pass or reject
	// Head is the head review submit --head named for the verdict; "" when
	// it named none.
	Head  string    `json:"head"`
	Body  string    `json:"body"` // the review comment; "" when the verdict sent none
	From  string    `json:"from"`
	To    string    `json:"to"`
	Actor string    `json:"actor"`
	RunID string    `json:"run_id"`
	At    time.Time `json:"at"`
}

// reviewRounds lists every verdict review submit recorded on the item, in
// order, from the item's history.
func (m *Machine) reviewRounds(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := call.Project(ownProject)
	if r != nil {
		return r.Result()
	}
	item := call.Inv.Arg("item")
	if _, r := m.Read(ctx, prj, item); r != nil {
		return r.Result()
	}
	hist, err := m.History(ctx, prj, item)
	if err != nil {
		return cli.Internal(err)
	}
	return cli.List(Rounds(hist))
}

// Rounds lists every verdict review submit recorded in an item's history,
// in order. The retro's record reads the same rounds.
func Rounds(hist []Transition) []Round {
	var out []Round
	for _, t := range hist {
		verdict, ok := map[string]string{"review_pass": "pass", "review_reject": "reject"}[t.Verb]
		if !ok {
			continue
		}
		out = append(out, Round{Round: len(out) + 1, Verdict: verdict, Head: cmp.Or(t.Fact(FactReviewedHead), t.Fact(FactRejectedHead)),
			Body: t.Set[factPrefix+FactReview], From: t.From, To: t.To, Actor: t.Actor, RunID: t.RunID, At: t.At})
	}
	return out
}
