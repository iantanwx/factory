package machine

import (
	"context"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// Register sets the handlers of the CLI verbs U5 implements. Every other
// write stays NOT_IMPLEMENTED until its unit lands: the reactor, the
// scheduler, admission and the inbox call Apply, Claim, MergeObserved and
// HeadChanged instead.
func (m *Machine) Register(s *rpc.Server) error {
	for path, h := range map[string]rpc.Handler{
		"work show":       m.workShow,
		"work list":       m.workList,
		"work note":       m.verb("note", "item"),
		"work cancel":     m.verb("cancel", "item"),
		"work unblock":    m.verb("unblock", "item"),
		"review submit":   m.reviewSubmit,
		"decision fire":   m.verb("fire", "decision"),
		"decision defer":  m.verb("defer", "decision"),
		"decision moot":   m.verb("moot", "decision"),
		"decision settle": m.decisionSettle,
	} {
		if err := s.Handle(path, h); err != nil {
			return err
		}
	}
	return nil
}

// project is the call's project: a run's or a room's own, or the
// coordinator's --project.
func project(call *rpc.Call) (string, *cli.Refusal) {
	p, flag := call.Principal, call.Inv.Flags["project"]
	switch {
	case flag != "" && p.Project != "" && flag != p.Project && p.Kind != cli.PrincipalCoordinator:
		return "", &cli.Refusal{Code: cli.CodeCapabilityDenied, Rule: "other_project",
			Hint: "a run works only in its own project"}
	case flag != "":
		return flag, nil
	case p.Project != "":
		return p.Project, nil
	}
	return "", &cli.Refusal{Code: cli.CodeUsage, Rule: "no_project", Hint: "name the project with --project"}
}

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

// verb is the handler of a CLI write that maps to one table verb on the
// item named by the argument arg.
func (m *Machine) verb(verb, arg string) rpc.Handler {
	return func(ctx context.Context, call *rpc.Call) cli.Result {
		prj, r := project(call)
		if r != nil {
			return refuse(r)
		}
		params := map[string]string{}
		if b := call.Inv.Flags["body"]; b != "" {
			params["body"] = b
		}
		return m.Apply(ctx, request(call, prj, call.Inv.Arg(arg), verb, params))
	}
}

func (m *Machine) reviewSubmit(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := project(call)
	if r != nil {
		return refuse(r)
	}
	verb := "review_pass"
	if call.Inv.Flags["verdict"] == "reject" {
		verb = "review_reject"
	}
	params := map[string]string{}
	if h := call.Inv.Flags["head"]; h != "" {
		params["head"] = h
	}
	return m.Apply(ctx, request(call, prj, call.Inv.Arg("item"), verb, params))
}

func (m *Machine) decisionSettle(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := project(call)
	if r != nil {
		return refuse(r)
	}
	verb := "answer"
	if call.Inv.Flags["dropped"] == "true" {
		verb = "drop"
	}
	return m.Apply(ctx, request(call, prj, call.Inv.Arg("decision"), verb, nil))
}

func (m *Machine) workShow(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := project(call)
	if r != nil {
		return refuse(r)
	}
	it, r := m.Show(ctx, prj, call.Inv.Arg("item"))
	if r != nil {
		return refuse(r)
	}
	return cli.Found(it)
}

func (m *Machine) workList(ctx context.Context, call *rpc.Call) cli.Result {
	prj, r := project(call)
	if r != nil {
		return refuse(r)
	}
	items, r := m.List(ctx, prj, ListFilter{Kind: Kind(call.Inv.Flags["kind"]), Status: call.Inv.Flags["status"]})
	if r != nil {
		return refuse(r)
	}
	return cli.List(items)
}
