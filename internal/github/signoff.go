package github

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/config"
)

// SignoffRequest is what a document PR's review request did. Reviewers
// names each login GitHub now asks for a review. Warning names the readers
// to ask by hand when the request failed.
type SignoffRequest struct {
	Reviewers []string `json:"reviewers,omitempty"`
	Warning   string   `json:"warning,omitempty"`
}

// RequestSignoff asks the project's sign-off readers to review PR number,
// so GitHub notifies the person who merges the document. The App opens
// the PR, and GitHub notifies no person of an App's PR otherwise. Only a
// PRD or a spec PR asks: a ticket's PR has no person gate, and a request
// on every PR would bury the two a person must act on. Ported from
// requestReview in scripts/sdlc/pr-after-open.ts (alpha).
//
// The readers come from the project config on the base branch in the
// mirror, which the push before every document PR fetched. The PR is open
// by the time this runs, so a failure never fails the caller: it returns
// a warning that names the readers and the PR. Nil means the project
// names no reader.
func (s *Service) RequestSignoff(ctx context.Context, p config.Project, number int) *SignoffRequest {
	readers, err := s.signoffReaders(ctx, p)
	if err == nil && len(readers) == 0 {
		return nil
	}
	if err == nil {
		_, err = s.repoDo(ctx, p, ScopePulls, Call{Method: http.MethodPost,
			Path: repoPath(p, "/pulls/"+strconv.Itoa(number)+"/requested_reviewers"),
			Body: map[string]any{"reviewers": readers}}, nil)
	}
	if err != nil {
		who := "the sign-off readers"
		if len(readers) > 0 {
			who = strings.Join(readers, ", ")
		}
		msg := fmt.Sprintf("could not request %s as reviewers on #%d: %v; the PR is open, so request the review by hand",
			who, number, err)
		s.log.WarnContext(ctx, "github sign-off request", "project", p.ID, "pr", number, "err", err)
		return &SignoffRequest{Warning: s.cfg.Redactor.Redact(msg)}
	}
	return &SignoffRequest{Reviewers: readers}
}

// signoffReaders reads signoff.readers from the project config on the
// base branch. A base with no config names no reader.
func (s *Service) signoffReaders(ctx context.Context, p config.Project) ([]string, error) {
	base, err := s.DefaultBranch(ctx, p)
	if err != nil {
		return nil, err
	}
	raw, err := s.Mirror(p).Show(ctx, config.DefaultRef(base), config.ProjectConfigPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cfg, err := config.ParseProjectConfig(raw)
	if err != nil {
		return nil, err
	}
	return cfg.Signoff.Readers, nil
}
