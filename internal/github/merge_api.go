package github

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/iantanwx/loop/internal/config"
)

// The REST and GraphQL calls of the merge mechanisms. Each call
// mints a token of the smallest scope it needs (KTD15).

// EligiblePosted reports whether the server posted loop/eligible on sha:
// its status effect for that sha is done. The server is the only poster,
// so its own record is the read, and no GitHub call is needed.
func (s *Service) EligiblePosted(ctx context.Context, project, sha string) (bool, error) {
	var n int
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE effect_kind = ? AND natural_key = ? AND state = 'done'`,
			EffectStatus, statusKey(project, sha, EligibleContext)).Scan(&n)
	})
	return n > 0, err
}

// FromRepo reports whether the PR's head branch lives in the project
// repository, not in a fork.
func (pr PR) FromRepo(p config.Project) bool { return pr.fromRepo(p) }

// Labels reads the label names of PR n.
func (s *Service) Labels(ctx context.Context, p config.Project, n int) ([]string, error) {
	var out []struct {
		Name string `json:"name"`
	}
	if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, fmt.Sprintf("/issues/%d/labels?per_page=100", n)), &out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out))
	for _, l := range out {
		names = append(names, l.Name)
	}
	return names, nil
}

// AddLabel adds one label to PR n. Adding a label it holds changes nothing.
func (s *Service) AddLabel(ctx context.Context, p config.Project, n int, label string) error {
	_, err := s.repoDo(ctx, p, ScopePulls, Call{Method: http.MethodPost, Path: repoPath(p, fmt.Sprintf("/issues/%d/labels", n)),
		Body: map[string]any{"labels": []string{label}}}, nil)
	return err
}

// RemoveLabel removes one label from PR n. A label the PR lacks is no
// fault.
func (s *Service) RemoveLabel(ctx context.Context, p config.Project, n int, label string) error {
	_, err := s.repoDo(ctx, p, ScopePulls, Call{Method: http.MethodDelete,
		Path: repoPath(p, fmt.Sprintf("/issues/%d/labels/%s", n, url.PathEscape(label)))}, nil)
	var api *APIError
	if errors.As(err, &api) && api.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// Comment posts one conversation comment on PR n as the App.
func (s *Service) Comment(ctx context.Context, p config.Project, n int, body string) error {
	_, err := s.repoDo(ctx, p, ScopePulls, Call{Method: http.MethodPost, Path: repoPath(p, fmt.Sprintf("/issues/%d/comments", n)),
		Body: map[string]string{"body": body}}, nil)
	return err
}

// MergePR merges PR n with a merge commit, only while its head is sha.
// GitHub refuses the merge when the head moved, so a push after review
// can never merge. It returns the merge commit. A refusal is an
// *APIError; an unknown answer is never read as a merge.
func (s *Service) MergePR(ctx context.Context, p config.Project, n int, sha string) (string, error) {
	if !shaRe.MatchString(sha) {
		return "", fmt.Errorf("github: %q is not a full sha", sha)
	}
	var out struct {
		SHA    string `json:"sha"`
		Merged bool   `json:"merged"`
	}
	_, err := s.repoDo(ctx, p, ScopeMerge, Call{Method: http.MethodPut, Path: repoPath(p, fmt.Sprintf("/pulls/%d/merge", n)),
		Body: map[string]string{"sha": sha, "merge_method": "merge"}}, &out)
	if err != nil {
		return "", err
	}
	if !out.Merged || !shaRe.MatchString(out.SHA) {
		return "", &UnavailableError{Err: fmt.Errorf("the merge answer of #%d names no merge commit", n)}
	}
	return out.SHA, nil
}

// GraphQLError is an error list in a GraphQL answer.
type GraphQLError struct{ Messages []string }

func (e *GraphQLError) Error() string { return "github: graphql: " + strings.Join(e.Messages, "; ") }

// GraphQL sends one GraphQL document with a token of scope and decodes
// its data into out. A query retries a transient answer. A mutation does
// not, because GitHub may have applied it.
func (s *Service) GraphQL(ctx context.Context, p config.Project, scope Scope, query string, vars map[string]any, out any) error {
	var ans struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	mutation := strings.HasPrefix(strings.TrimSpace(query), "mutation")
	_, err := s.repoDo(ctx, p, scope, Call{Method: http.MethodPost, Path: "/graphql", Repeatable: !mutation,
		Body: map[string]any{"query": query, "variables": vars}}, &ans)
	if err != nil {
		return err
	}
	if len(ans.Errors) > 0 {
		ge := &GraphQLError{}
		for _, e := range ans.Errors {
			ge.Messages = append(ge.Messages, e.Message)
		}
		return ge
	}
	if out == nil || len(ans.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(ans.Data, out); err != nil {
		return &UnavailableError{Err: fmt.Errorf("graphql: decode the answer: %w", err)}
	}
	return nil
}

// RefSHA is one git ref and the commit it names.
type RefSHA struct {
	Ref string // full ref name, refs/heads/...
	SHA string
}

// MatchingRefs lists the branches whose name starts with prefix.
func (s *Service) MatchingRefs(ctx context.Context, p config.Project, prefix string) ([]RefSHA, error) {
	var out []struct {
		Ref    string `json:"ref"`
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/git/matching-refs/heads/"+pathEscapeBranch(prefix)), &out); err != nil {
		return nil, err
	}
	refs := make([]RefSHA, 0, len(out))
	for _, r := range out {
		refs = append(refs, RefSHA{Ref: r.Ref, SHA: r.Object.SHA})
	}
	return refs, nil
}

// BranchSHA reads the commit a branch names on GitHub.
func (s *Service) BranchSHA(ctx context.Context, p config.Project, branch string) (string, error) {
	var out struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/git/ref/heads/"+pathEscapeBranch(branch)), &out); err != nil {
		return "", err
	}
	if !shaRe.MatchString(out.Object.SHA) {
		return "", &UnavailableError{Err: fmt.Errorf("the ref answer of %s names no commit", branch)}
	}
	return out.Object.SHA, nil
}

// BranchRules reads the rules that apply to the default branch: the type
// of each rule, and the required status contexts other than loop/eligible.
// It reads the rule types and the contexts only, never a response layout.
func (s *Service) BranchRules(ctx context.Context, p config.Project) (types, checks []string, err error) {
	base, err := s.DefaultBranch(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	var rules []rule
	if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/rules/branches/"+pathEscapeBranch(base)), &rules); err != nil {
		return nil, nil, err
	}
	for _, r := range rules {
		types = append(types, r.Type)
		if r.Type != "required_status_checks" || len(r.Parameters) == 0 {
			continue
		}
		var params struct {
			Checks []requiredCheck `json:"required_status_checks"`
		}
		if err := json.Unmarshal(r.Parameters, &params); err != nil {
			return nil, nil, fmt.Errorf("github: read the required checks of %s: %w", base, err)
		}
		for _, c := range params.Checks {
			if c.Context != "" && c.Context != EligibleContext {
				checks = append(checks, c.Context)
			}
		}
	}
	return types, checks, nil
}

// FileAt reports whether path exists on branch.
func (s *Service) FileAt(ctx context.Context, p config.Project, branch, path string) (bool, error) {
	err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/contents/"+pathEscapeBranch(path)+"?ref="+url.QueryEscape(branch)), nil)
	var api *APIError
	switch {
	case errors.As(err, &api) && api.Status == http.StatusNotFound:
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// FilePR is one file the server proposes through its own pull request.
type FilePR struct {
	Branch  string // the setup branch; never the base and never loop/<item>
	Path    string
	Content []byte
	Message string // the commit message
	Title   string
	Body    string
}

// OpenFilePR opens a pull request that adds one file on its own branch,
// or returns the PR already open on that branch. It is safe to run again:
// an existing branch and an existing file are no fault. It writes through
// the contents API, so no git runs with the token (KTD14).
func (s *Service) OpenFilePR(ctx context.Context, p config.Project, f FilePR) (PR, bool, error) {
	if ItemOf(f.Branch) != "" {
		return PR{}, false, fmt.Errorf("github: %s is an item branch; a setup PR needs its own branch", f.Branch)
	}
	base, err := s.DefaultBranch(ctx, p)
	if err != nil {
		return PR{}, false, err
	}
	if f.Branch == base {
		return PR{}, false, fmt.Errorf("github: a setup PR never writes the base branch %s", base)
	}
	unlock := s.lock(p.ID + "\x00pr\x00" + f.Branch)
	defer unlock()
	if pr, err := s.FindPR(ctx, p, f.Branch); err != nil {
		return PR{}, false, err
	} else if pr != nil && pr.State == "open" {
		return *pr, false, nil
	}
	tip, err := s.BranchSHA(ctx, p, base)
	if err != nil {
		return PR{}, false, err
	}
	_, err = s.repoDo(ctx, p, ScopeMerge, Call{Method: http.MethodPost, Path: repoPath(p, "/git/refs"),
		Body: map[string]string{"ref": "refs/heads/" + f.Branch, "sha": tip}}, nil)
	var api *APIError
	if err != nil && (!errors.As(err, &api) || api.Status != http.StatusUnprocessableEntity) {
		return PR{}, false, err // 422: the branch exists from an earlier add
	}
	has, err := s.FileAt(ctx, p, f.Branch, f.Path)
	if err != nil {
		return PR{}, false, err
	}
	if !has {
		_, err = s.repoDo(ctx, p, ScopeMerge, Call{Method: http.MethodPut, Path: repoPath(p, "/contents/"+pathEscapeBranch(f.Path)),
			Body: map[string]string{"message": f.Message, "content": base64.StdEncoding.EncodeToString(f.Content), "branch": f.Branch}}, nil)
		if err != nil {
			return PR{}, false, err
		}
	}
	var pr PR
	_, err = s.repoDo(ctx, p, ScopeMerge, Call{Method: http.MethodPost, Path: repoPath(p, "/pulls"),
		Body: map[string]any{"title": f.Title, "body": f.Body, "head": f.Branch, "base": base}}, &pr)
	if err != nil {
		return PR{}, false, err
	}
	return pr, true, nil
}

// AppSlug reads the loop App's slug. Its bot user is "<slug>[bot]".
func (s *Service) AppSlug(ctx context.Context) (string, error) {
	var out struct {
		Slug string `json:"slug"`
	}
	if _, err := s.appCall(ctx, Call{Method: http.MethodGet, Path: "/app"}, &out); err != nil {
		return "", err
	}
	if out.Slug == "" {
		return "", &UnavailableError{Err: errors.New("the App answer names no slug")}
	}
	return out.Slug, nil
}

// Short is the first eight characters of a sha, for messages.
func Short(sha string) string { return short(sha) }
