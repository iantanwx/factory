package rpc

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/iantanwx/loop/internal/cli"
)

// HoldsItem reports whether p holds item now. The default compares item
// with the token's bound item. The server widens it for a triage run, and
// the machine checks the lease of each write.
type HoldsItem func(ctx context.Context, p Principal, item string) (bool, error)

// ReadsItem reports whether p may read an item it does not hold. A write
// verb never asks it (KTD12: a run moves only the item it holds). The
// server opens the epic of the held ticket, and a room's read of each item
// its reason names or it created.
type ReadsItem func(ctx context.Context, p Principal, item string) (bool, error)

// TokenItem is the default HoldsItem.
func TokenItem(_ context.Context, p Principal, item string) (bool, error) {
	return p.Item != "" && p.Item == item, nil
}

// CheckoutLister lists the directories that count as a checkout or an agent
// session: a host verb from inside one is refused.
type CheckoutLister func(ctx context.Context) ([]string, error)

func denied(rule, item, hint string) *cli.Result {
	res := cli.Refuse(cli.Refusal{Code: cli.CodeCapabilityDenied, Rule: rule, Item: item, Hint: hint})
	return &res
}

// authorize applies the capability matrix and the peer checks (KTD12). It
// may mark p.CwdUnverified.
func (s *Server) authorize(ctx context.Context, inv cli.Invocation, p *Principal) *cli.Result {
	path := inv.Verb.Path
	m := s.matrix

	if m.IsHostVerb(path) {
		if res := s.checkHostCaller(ctx, path, p); res != nil {
			return res
		}
	}
	if p.Checkout != "" {
		if res := s.checkTokenCaller(ctx, path, p); res != nil {
			return res
		}
	}
	if !m.Allows(path, p.Kind) {
		hint := fmt.Sprintf("a %s caller may not call %s", p.Kind, path)
		if p.Kind == cli.PrincipalNone {
			hint = fmt.Sprintf("%s needs a run token or the host-admin credential; a call with neither reaches the meta verbs only", path)
		}
		return denied("principal_not_allowed", "", hint)
	}
	if (p.Kind == cli.PrincipalRun || p.Kind == cli.PrincipalRoom) && m.IsItemScoped(path) {
		item := inv.Arg("item")
		holds, err := s.opts.HoldsItem(ctx, *p, item)
		if err != nil {
			return new(cli.Internal(fmt.Errorf("check whether %s holds %s: %w", p.Actor(), item, err)))
		}
		if !holds && !inv.Verb.Write && s.opts.ReadsItem != nil {
			if holds, err = s.opts.ReadsItem(ctx, *p, item); err != nil {
				return new(cli.Internal(fmt.Errorf("check whether %s reads %s: %w", p.Actor(), item, err)))
			}
		}
		if !holds {
			return denied("item_not_held", item, fmt.Sprintf("%s does not hold %s; a run moves only the item it holds", p.Actor(), item))
		}
	}
	return nil
}

// checkHostCaller refuses a host verb whose caller, or any ancestor of the
// caller, works inside a registered checkout or agent session. The one
// exception is the coordinator's token inside the coordinator's own
// checkout (factorio-obc): checkTokenCaller pins the token there, and the
// matrix admits the coordinator only on the verbs the surface opens to it.
func (s *Server) checkHostCaller(ctx context.Context, path string, p *Principal) *cli.Result {
	dirs, err := s.opts.Checkouts(ctx)
	if err != nil {
		return new(cli.Internal(fmt.Errorf("list registered checkouts: %w", err)))
	}
	cwds, err := ancestry(s.opts.Proc, p.Peer.PID)
	if errors.Is(err, ErrProcUnsupported) || p.Peer.PID == 0 {
		p.CwdUnverified = true
		return nil
	}
	if err != nil {
		return new(cli.Internal(fmt.Errorf("read the caller's working directory: %w", err)))
	}
	for i, cwd := range cwds {
		for _, dir := range dirs {
			if !within(cwd, dir) || (p.Kind == cli.PrincipalCoordinator && ownCheckout(p, cwd, dir)) {
				continue
			}
			who := "the caller"
			if i > 0 {
				who = fmt.Sprintf("ancestor %d of the caller", i)
			}
			quiet := slices.Contains(hostReads, path) && (p.Kind == cli.PrincipalRun || p.Kind == cli.PrincipalRoom) &&
				ownCheckout(p, cwd, dir)
			s.alert(ctx, AuditAlert{
				Actor: p.Actor(), Project: p.Project, Kind: AlertHostVerbFromCheckout, Subject: dir,
				Body:    fmt.Sprintf("%s ran %s from inside the checkout %s", who, path, dir),
				Detail:  map[string]any{"verb": path, "cwd": cwd, "checkout": dir, "pid": p.Peer.PID, "depth": i},
				NoAlert: quiet,
			})
			hint := fmt.Sprintf("%s is a host verb, and %s works inside the checkout %s; run it from outside every checkout", path, who, dir)
			if quiet {
				hint = fmt.Sprintf("%s is a host verb that no run or room calls; your own project is the project field of .loop/brief.json", path)
			}
			return denied("host_verb_from_checkout", "", hint)
		}
	}
	return nil
}

// ownCheckout reports whether cwd and the registered dir both lie inside
// the token's own checkout.
func ownCheckout(p *Principal, cwd, dir string) bool {
	return p.Checkout != "" && within(cwd, p.Checkout) && within(dir, p.Checkout)
}

// hostReads are the host verbs that only read. A run or a room that calls
// one from its own checkout is refused without an alert (factorio-kq28):
// the refusal already answers it, and nothing rings the coordinator.
var hostReads = []string{"project list", "project show"}

// checkTokenCaller refuses a run token used from outside its own checkout.
func (s *Server) checkTokenCaller(ctx context.Context, path string, p *Principal) *cli.Result {
	if p.Peer.PID == 0 {
		p.CwdUnverified = true
		return nil
	}
	cwd, err := s.opts.Proc.Cwd(p.Peer.PID)
	if errors.Is(err, ErrProcUnsupported) {
		p.CwdUnverified = true
		return nil
	}
	if err != nil {
		return new(cli.Internal(fmt.Errorf("read the caller's working directory: %w", err)))
	}
	if within(cwd, p.Checkout) {
		return nil
	}
	s.alert(ctx, AuditAlert{
		Actor: p.Actor(), Project: p.Project, Item: p.Item, Kind: AlertTokenOutsideCheckout, Subject: p.RunID,
		Body:   fmt.Sprintf("the token of %s was used from %s, outside its checkout %s", p.Actor(), cwd, p.Checkout),
		Detail: map[string]any{"verb": path, "cwd": cwd, "checkout": p.Checkout, "pid": p.Peer.PID},
	})
	return denied("token_outside_checkout", p.Item,
		fmt.Sprintf("the token of %s works only inside its own checkout", p.Actor()))
}

func (s *Server) alert(ctx context.Context, a AuditAlert) {
	if err := WriteAuditAlert(context.WithoutCancel(ctx), s.opts.Store, s.opts.Redactor, a); err != nil {
		s.log.ErrorContext(ctx, "audit alert failed", "kind", a.Kind, "err", err)
	}
}
