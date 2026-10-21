package server

import (
	"errors"

	"github.com/iantanwx/loop/internal/pipeline"
)

// Pipeline wires the document pipeline and work entry into a
// server: prd submit, epic decompose and work create. Pass Setup in
// Options.Setup after the GitHub setup, because it pushes, opens PRs and
// reads the merged PRD through the GitHub service:
//
//	gh := &server.GitHub{}
//	pl := &server.Pipeline{GitHub: gh}
//	opts.Setup = append(opts.Setup, gh.Setup, pl.Setup)
//
// The moves of the pipeline are rows of the epic table, and the GitHub
// reactor observes the operator's merge of a document PR.
type Pipeline struct {
	GitHub *GitHub

	// Pipeline is set by Setup. It is nil on a server without beads.
	Pipeline *pipeline.Pipeline
}

// Setup composes the pipeline over the server's store, machine, beads,
// projects and GitHub service, and registers its verbs.
func (p *Pipeline) Setup(s *Server) error {
	if p.GitHub == nil || p.GitHub.Service == nil {
		return errors.New("server: the pipeline setup needs the GitHub setup first")
	}
	if s.Machine == nil {
		return nil
	}
	pl, err := pipeline.New(pipeline.Config{Store: s.Store, Machine: s.Machine, Beads: s.opts.Beads, GitHub: p.GitHub.Service,
		Projects: s.Projects, Redactor: s.Redactor, Logger: s.RPC.Logger()})
	if err != nil {
		return err
	}
	p.Pipeline = pl
	return pl.Register(s.RPC)
}
