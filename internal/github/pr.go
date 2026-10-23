package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/config"
)

// PR is a pull request as GitHub's REST API reads it. Only the fields the
// loop reads are named.
type PR struct {
	Number         int        `json:"number"`
	URL            string     `json:"html_url"`
	State          string     `json:"state"` // open or closed
	Title          string     `json:"title"`
	Draft          bool       `json:"draft"`
	MergedAt       *time.Time `json:"merged_at"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	MergeableState string     `json:"mergeable_state"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Head           struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
}

// Merged reports whether GitHub merged the PR.
func (pr PR) Merged() bool { return pr.MergedAt != nil }

// fromRepo reports whether the PR's head branch lives in the project
// repository. A fork can name its branch loop/<item>, so a PR from a fork
// never belongs to an item.
func (pr PR) fromRepo(p config.Project) bool {
	return pr.Head.Repo != nil && strings.EqualFold(pr.Head.Repo.FullName, p.Repo)
}

// PRView is what `pr show` and `pr open` print.
type PRView struct {
	Number         int    `json:"number"`
	URL            string `json:"url"`
	State          string `json:"state"` // open, closed or merged
	Title          string `json:"title"`
	Head           string `json:"head"`
	HeadSHA        string `json:"head_sha"`
	Base           string `json:"base"`
	Draft          bool   `json:"draft"`
	MergeableState string `json:"mergeable_state,omitempty"`
}

// View is the CLI form of a PR.
func (pr PR) View() PRView {
	state := pr.State
	if pr.Merged() {
		state = "merged"
	}
	return PRView{Number: pr.Number, URL: pr.URL, State: state, Title: pr.Title, Head: pr.Head.Ref,
		HeadSHA: pr.Head.SHA, Base: pr.Base.Ref, Draft: pr.Draft, MergeableState: pr.MergeableState}
}

// FindPR returns the PR whose head is branch in the project repository:
// the open one when there is one, else the most recently updated. It
// returns nil when the branch has no PR.
func (s *Service) FindPR(ctx context.Context, p config.Project, branch string) (*PR, error) {
	q := url.Values{"head": {owner(p) + ":" + branch}, "state": {"all"}, "per_page": {"100"}}
	var prs []PR
	if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/pulls?"+q.Encode()), &prs); err != nil {
		return nil, err
	}
	var best *PR
	for i := range prs {
		pr := &prs[i]
		if pr.Head.Ref != branch || !pr.fromRepo(p) {
			continue
		}
		switch {
		case best == nil:
			best = pr
		case pr.State == "open" && best.State != "open":
			best = pr
		case (pr.State == "open") == (best.State == "open") && pr.UpdatedAt.After(best.UpdatedAt):
			best = pr
		}
	}
	return best, nil
}

// GetPR reads one PR by number.
func (s *Service) GetPR(ctx context.Context, p config.Project, number int) (PR, error) {
	var pr PR
	err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/pulls/"+strconv.Itoa(number)), &pr)
	return pr, err
}

// OpenPR opens the PR of an item's branch as the App, or returns the PR
// already open on that head branch, so `pr open` twice on one branch opens
// one PR (KTD6: a PR's natural key is its head branch). branch is the
// item's branch, BranchOf. Ported from createPullRequest in
// scripts/sdlc/pr-open.ts:165 (alpha).
func (s *Service) OpenPR(ctx context.Context, p config.Project, branch, title, body string) (PR, bool, error) {
	if ItemOf(branch) == "" {
		return PR{}, false, fmt.Errorf("github: %s is no item branch", branch)
	}
	base, err := s.DefaultBranch(ctx, p)
	if err != nil {
		return PR{}, false, err
	}
	unlock := s.lock(p.ID + "\x00pr\x00" + branch)
	defer unlock()
	if pr, err := s.FindPR(ctx, p, branch); err != nil {
		return PR{}, false, err
	} else if pr != nil && pr.State == "open" {
		return *pr, false, nil
	}
	var pr PR
	_, err = s.repoDo(ctx, p, ScopePulls, Call{Method: http.MethodPost, Path: repoPath(p, "/pulls"),
		Body: map[string]any{"title": title, "body": body, "head": branch, "base": base}}, &pr)
	var api *APIError
	if errors.As(err, &api) && api.Status == http.StatusUnprocessableEntity && strings.Contains(api.Message, "already exists") {
		// Another call opened it first.
		found, ferr := s.FindPR(ctx, p, branch)
		if ferr == nil && found != nil {
			return *found, false, nil
		}
	}
	if err != nil {
		return PR{}, false, err
	}
	return pr, true, nil
}

// File is one changed file of a PR.
type File struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Previous  string `json:"previous_path,omitempty"`
}

// maxFilePages bounds `pr files`: GitHub lists at most 3000 files.
const maxFilePages = 30

// PRFiles lists every changed file of a PR from REST, a page of 100 at a
// time. Ported from the parity case in scripts/sdlc/premerge-gate.test.ts
// (alpha): a list cut at 100 files hides the rest.
func (s *Service) PRFiles(ctx context.Context, p config.Project, number int) ([]File, error) {
	var out []File
	for page := 1; page <= maxFilePages; page++ {
		var batch []struct {
			Filename         string `json:"filename"`
			Status           string `json:"status"`
			Additions        int    `json:"additions"`
			Deletions        int    `json:"deletions"`
			PreviousFilename string `json:"previous_filename"`
		}
		resp, err := s.repoDo(ctx, p, ScopeRead, Call{Method: http.MethodGet,
			Path: repoPath(p, fmt.Sprintf("/pulls/%d/files?per_page=100&page=%d", number, page))}, &batch)
		if err != nil {
			return nil, err
		}
		for _, f := range batch {
			out = append(out, File{Path: f.Filename, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions,
				Previous: f.PreviousFilename})
		}
		if len(batch) < 100 && !nextPage(resp.Header) {
			break
		}
	}
	return out, nil
}

// maxDiff bounds the diff `pr diff` prints.
const maxDiff = 1 << 20

// Diff reads a PR's unified diff. A diff over maxDiff is cut, and
// truncated says so.
func (s *Service) Diff(ctx context.Context, p config.Project, number int) (string, bool, error) {
	var text string
	_, err := s.repoDo(ctx, p, ScopeRead, Call{Method: http.MethodGet, Path: repoPath(p, "/pulls/"+strconv.Itoa(number)),
		Accept: "application/vnd.github.diff", Raw: &text}, nil)
	if err != nil {
		return "", false, err
	}
	if len(text) > maxDiff {
		return text[:maxDiff], true, nil
	}
	return text, false, nil
}

// Author classes (KTD27). The CLI marks every thread, comment and log
// text with one, and the brief tells the agent to treat the text as data.
const (
	AuthorApp      = "app"
	AuthorOperator = "operator"
	AuthorOther    = "other"
)

// Comment is one review comment or PR conversation comment.
type Comment struct {
	ID          int64     `json:"id"`
	Kind        string    `json:"kind"` // review or conversation
	Author      string    `json:"author"`
	AuthorClass string    `json:"author_class"`
	Body        string    `json:"body"`
	Path        string    `json:"path,omitempty"`
	Line        int       `json:"line,omitempty"`
	InReplyTo   int64     `json:"in_reply_to,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	URL         string    `json:"url"`
}

type rawComment struct {
	ID          int64  `json:"id"`
	Body        string `json:"body"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	InReplyToID int64  `json:"in_reply_to_id"`
	HTMLURL     string `json:"html_url"`
	User        struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	AuthorAssociation string `json:"author_association"`
	PerformedVia      *struct {
		ID int64 `json:"id"`
	} `json:"performed_via_github_app"`
	CreatedAt time.Time `json:"created_at"`
}

// class names who wrote a comment: the loop App, the repository owner,
// or anyone else.
func (s *Service) class(c rawComment) string {
	switch {
	case c.PerformedVia != nil && s.isLoopApp(c.PerformedVia.ID):
		return AuthorApp
	case c.User.Type != "Bot" && c.AuthorAssociation == "OWNER":
		return AuthorOperator
	}
	return AuthorOther
}

// maxCommentPages bounds one thread read.
const maxCommentPages = 10

func (s *Service) comments(ctx context.Context, p config.Project, path, kind string) ([]Comment, error) {
	var out []Comment
	for page := 1; page <= maxCommentPages; page++ {
		var batch []rawComment
		resp, err := s.repoDo(ctx, p, ScopeRead, Call{Method: http.MethodGet,
			Path: repoPath(p, fmt.Sprintf("%s?per_page=100&page=%d", path, page))}, &batch)
		if err != nil {
			return nil, err
		}
		for _, c := range batch {
			out = append(out, Comment{ID: c.ID, Kind: kind, Author: c.User.Login, AuthorClass: s.class(c), Body: c.Body,
				Path: c.Path, Line: c.Line, InReplyTo: c.InReplyToID, CreatedAt: c.CreatedAt, URL: c.HTMLURL})
		}
		if len(batch) < 100 && !nextPage(resp.Header) {
			break
		}
	}
	return out, nil
}

// Threads reads a PR's review comments and its conversation comments.
func (s *Service) Threads(ctx context.Context, p config.Project, number int) ([]Comment, error) {
	review, err := s.comments(ctx, p, fmt.Sprintf("/pulls/%d/comments", number), "review")
	if err != nil {
		return nil, err
	}
	talk, err := s.comments(ctx, p, fmt.Sprintf("/issues/%d/comments", number), "conversation")
	if err != nil {
		return nil, err
	}
	return append(review, talk...), nil
}

// marker is the natural key of one comment the server posts (KTD6). It
// rides in the body as an HTML comment, so a retry finds the first post.
func marker(key string) string { return "<!-- loop:comment " + key + " -->" }

// Reply posts body on a PR: as a reply to the review comment thread when
// thread is not zero, else as a conversation comment. key names the
// write: a comment that already carries its marker is the earlier post,
// and the call answers replayed.
func (s *Service) Reply(ctx context.Context, p config.Project, number int, thread int64, body, key string) (Comment, bool, error) {
	mark := marker(key)
	existing, err := s.Threads(ctx, p, number)
	if err != nil {
		return Comment{}, false, err
	}
	for _, c := range existing {
		if strings.Contains(c.Body, mark) {
			return c, true, nil
		}
	}
	text := body + "\n\n" + mark
	path, kind := fmt.Sprintf("/issues/%d/comments", number), "conversation"
	if thread != 0 {
		path, kind = fmt.Sprintf("/pulls/%d/comments/%d/replies", number, thread), "review"
	}
	var c rawComment
	if _, err := s.repoDo(ctx, p, ScopePulls, Call{Method: http.MethodPost, Path: repoPath(p, path),
		Body: map[string]string{"body": text}}, &c); err != nil {
		return Comment{}, false, err
	}
	return Comment{ID: c.ID, Kind: kind, Author: c.User.Login, AuthorClass: AuthorApp, Body: c.Body, Path: c.Path,
		Line: c.Line, InReplyTo: c.InReplyToID, CreatedAt: c.CreatedAt, URL: c.HTMLURL}, false, nil
}
