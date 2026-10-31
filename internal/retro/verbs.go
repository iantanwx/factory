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
	return map[string]rpc.Handler{"retro record": s.record}
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
	if p.Kind != cli.PrincipalRun || p.Stage != Stage || p.Project == "" {
		return cli.Reject(cli.CodeCapabilityDenied, "retro_only", "only a retro run reads a retro's record")
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
