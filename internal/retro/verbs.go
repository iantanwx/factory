package retro

import (
	"context"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
)

// Stage is the run stage that learns from a finished item.
const Stage = "retro"

// Service serves the retro verbs.
type Service struct {
	World
	// Redactor masks each secret in the record's text. Optional.
	Redactor *secrets.Redactor
}

// Handlers returns the server handlers of the retro verbs, keyed by verb
// path.
func (s *Service) Handlers() map[string]rpc.Handler {
	return map[string]rpc.Handler{"retro record": s.record, "retro held": s.held, "retro route": s.route}
}

// Register sets the handlers of the retro verbs.
func (s *Service) Register(srv *rpc.Server) error {
	for path, h := range s.Handlers() {
		if err := srv.Handle(path, h); err != nil {
			return err
		}
	}
	return nil
}

// record is `loop retro record`: it binds the run's retro, or the oldest
// pending one of its project, and prints the retro's record.
func (s *Service) record(ctx context.Context, call *rpc.Call) cli.Result {
	p := call.Principal
	if ref := retroOnly(p, "reads a retro's record"); ref != nil {
		return ref.Result()
	}
	r, ok, err := Bind(ctx, s.Store, p.Project, p.RunID)
	if err != nil {
		return cli.Internal(err)
	}
	if !ok {
		return cli.Nothing()
	}
	rec, ref := BuildRecord(ctx, s.World, r)
	if ref != nil {
		return ref.Result()
	}
	if s.Redactor != nil {
		for _, cs := range rec.caseLists() {
			for i := range cs {
				cs[i].Text = s.Redactor.Redact(cs[i].Text)
			}
		}
	}
	return cli.Found(rec)
}

// retroOnly refuses every caller but a retro run of a project.
func retroOnly(p rpc.Principal, act string) *cli.Refusal {
	if p.Kind != cli.PrincipalRun || p.Stage != Stage || p.Project == "" {
		return &cli.Refusal{Code: cli.CodeCapabilityDenied, Rule: "retro_only", Hint: "only a retro run " + act}
	}
	return nil
}

// held is `loop retro held`: the held learnings of the run's project, with
// their cases.
func (s *Service) held(ctx context.Context, call *rpc.Call) cli.Result {
	p := call.Principal
	if ref := retroOnly(p, "reads the held learnings"); ref != nil {
		return ref.Result()
	}
	ls, err := Held(ctx, s.Store, p.Project)
	if err != nil {
		return cli.Internal(err)
	}
	return cli.List(ls)
}

// route is `loop retro route`: the run sends the panel file that
// `loop retro judge` wrote. The server decides each route again with its
// own thresholds, so a route is never a value the run typed.
func (s *Service) route(ctx context.Context, call *rpc.Call) cli.Result {
	p := call.Principal
	if ref := retroOnly(p, "routes a retro's learnings"); ref != nil {
		return ref.Result()
	}
	content, ok := cli.FileContent(call.Inv)
	if !ok {
		return cli.Reject(cli.CodeUsage, "no_file", "--file names the panel file that `loop retro judge` wrote")
	}
	if r := s.Redactor.Refusal("secret_in_panel_file", "panel file", content); r != nil {
		return r.Result()
	}
	panels, err := ParsePanels([]byte(content))
	if err != nil {
		return cli.Reject(cli.CodeUsage, "bad_panel", err.Error())
	}
	res, ref := RouteAll(ctx, s.World, p.RunID, panels)
	if ref != nil {
		return ref.Result()
	}
	return cli.Found(res)
}
