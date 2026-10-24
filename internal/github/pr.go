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
	Body           string     `json:"body"`
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
	// Body is whole, with no omitempty: the implementer records its
	// red-green evidence there, and a reviewer reads it through `pr show`.
	// An empty body is an answer too. Ported from shownFrom in
	// scripts/sdlc/pr.ts (alpha).
	Body string `json:"body"`
}

// View is the CLI form of a PR.
func (pr PR) View() PRView {
	state := pr.State
	if pr.Merged() {
		state = "merged"
	}
	return PRView{Number: pr.Number, URL: pr.URL, State: state, Title: pr.Title, Head: pr.Head.Ref,
		HeadSHA: pr.Head.SHA, Base: pr.Base.Ref, Draft: pr.Draft, MergeableState: pr.MergeableState, Body: pr.Body}
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
// scripts/sdlc/pr-open.ts:165 (alpha). On the PR already open it writes
// title and body when they differ from what the PR holds, so a rework's
// revised body reaches the reviewer (F-13); updated says it wrote. An
// empty title or body leaves that field as it is.
func (s *Service) OpenPR(ctx context.Context, p config.Project, branch, title, body string) (pr PR, created, updated bool, err error) {
	if ItemOf(branch) == "" {
		return PR{}, false, false, fmt.Errorf("github: %s is no item branch", branch)
	}
	base, err := s.DefaultBranch(ctx, p)
	if err != nil {
		return PR{}, false, false, err
	}
	unlock := s.lock(p.ID + "\x00pr\x00" + branch)
	defer unlock()
	if found, err := s.FindPR(ctx, p, branch); err != nil {
		return PR{}, false, false, err
	} else if found != nil && found.State == "open" {
		pr, updated, err = s.editPR(ctx, p, *found, title, body)
		return pr, false, updated, err
	}
	_, err = s.repoDo(ctx, p, ScopePulls, Call{Method: http.MethodPost, Path: repoPath(p, "/pulls"),
		Body: map[string]any{"title": title, "body": body, "head": branch, "base": base}}, &pr)
	var api *APIError
	if errors.As(err, &api) && api.Status == http.StatusUnprocessableEntity && strings.Contains(api.Message, "already exists") {
		// Another call opened it first.
		found, ferr := s.FindPR(ctx, p, branch)
		if ferr == nil && found != nil {
			pr, updated, err = s.editPR(ctx, p, *found, title, body)
			return pr, false, updated, err
		}
	}
	if err != nil {
		return PR{}, false, false, err
	}
	return pr, true, false, nil
}

// editPR writes title and body to an open PR with one PATCH when either
// differs from what the PR holds. An empty title or body is left out, so
// the PR keeps its own. It reports whether it wrote.
func (s *Service) editPR(ctx context.Context, p config.Project, pr PR, title, body string) (PR, bool, error) {
	patch := map[string]any{}
	if title != "" && title != pr.Title {
		patch["title"] = title
	}
	if body != "" && body != pr.Body {
		patch["body"] = body
	}
	if len(patch) == 0 {
		return pr, false, nil
	}
	var out PR
	if _, err := s.repoDo(ctx, p, ScopePulls, Call{Method: http.MethodPatch,
		Path: repoPath(p, "/pulls/"+strconv.Itoa(pr.Number)), Body: patch}, &out); err != nil {
		return PR{}, false, err
	}
	return out, true, nil
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

// maxOpenPages bounds the read of a project's open pull requests: 1000 of
// them.
const maxOpenPages = 10

// OpenPulls lists every open pull request of the project, a page of 100
// at a time. Admission reads it for the `## Depends on` hold, so a list
// that stopped early would admit a ticket that a later page holds.
func (s *Service) OpenPulls(ctx context.Context, project string) ([]PR, error) {
	p, err := s.project(project)
	if err != nil {
		return nil, err
	}
	var out []PR
	for page := 1; page <= maxOpenPages; page++ {
		q := url.Values{"state": {"open"}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		var batch []PR
		resp, err := s.repoDo(ctx, p, ScopeRead, Call{Method: http.MethodGet, Path: repoPath(p, "/pulls?"+q.Encode())}, &batch)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(batch) < 100 && !nextPage(resp.Header) {
			return out, nil
		}
	}
	return nil, fmt.Errorf("github: %s has more than %d open pull requests", p.Repo, maxOpenPages*100)
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
	// Thread is the review thread of a review comment. A conversation
	// comment belongs to no thread.
	Thread *ThreadState `json:"thread,omitempty"`

	// bot marks a comment a program wrote: the loop App, or another.
	bot bool
}

// ThreadState is one review thread as GitHub's GraphQL reads it. Ported
// from threadsFrom in scripts/sdlc/github-pull.ts:154-184 (alpha).
type ThreadState struct {
	// ID is the thread's root review comment, the id `pr reply --thread`
	// takes.
	ID       int64 `json:"id"`
	Resolved bool  `json:"resolved"`
	Outdated bool  `json:"outdated"`
	// AwaitingUs is true while the thread is open and the last word is
	// not the loop App's. It needs no status field: a person comments and
	// the thread is ours, the App replies and it is theirs. An outdated
	// thread still counts, because the comment was about something real.
	AwaitingUs bool `json:"awaiting_us"`
}

// root is the id of the review thread's first comment.
func (c Comment) root() int64 {
	if c.InReplyTo != 0 {
		return c.InReplyTo
	}
	return c.ID
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
	// SubmittedAt is a review's time. A review has no created_at.
	SubmittedAt time.Time `json:"submitted_at"`
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
			if c.CreatedAt.IsZero() {
				c.CreatedAt = c.SubmittedAt
			}
			out = append(out, Comment{ID: c.ID, Kind: kind, Author: c.User.Login, AuthorClass: s.class(c), Body: c.Body,
				Path: c.Path, Line: c.Line, InReplyTo: c.InReplyToID, CreatedAt: c.CreatedAt, URL: c.HTMLURL,
				bot: c.User.Type == "Bot" || c.PerformedVia != nil})
		}
		if len(batch) < 100 && !nextPage(resp.Header) {
			break
		}
	}
	return out, nil
}

// allComments reads a PR's review comments, its conversation comments
// and the summaries of its reviews, by REST alone. A review summary reads
// as a conversation comment: it opens no thread, and a conversation
// comment answers it. A review with no body is an approval click, not
// feedback, so it is left out, as is a pending review nobody submitted.
func (s *Service) allComments(ctx context.Context, p config.Project, number int) ([]Comment, error) {
	review, err := s.comments(ctx, p, fmt.Sprintf("/pulls/%d/comments", number), "review")
	if err != nil {
		return nil, err
	}
	talk, err := s.comments(ctx, p, fmt.Sprintf("/issues/%d/comments", number), "conversation")
	if err != nil {
		return nil, err
	}
	summaries, err := s.comments(ctx, p, fmt.Sprintf("/pulls/%d/reviews", number), "conversation")
	if err != nil {
		return nil, err
	}
	for _, c := range summaries {
		if c.Body != "" && !c.CreatedAt.IsZero() {
			talk = append(talk, c)
		}
	}
	return append(review, talk...), nil
}

// Threads reads a PR's comments as allComments does, and gives each
// review comment the state of its thread.
func (s *Service) Threads(ctx context.Context, p config.Project, number int) ([]Comment, error) {
	cs, err := s.allComments(ctx, p, number)
	if err != nil {
		return nil, err
	}
	// The comments are read first, so GraphQL names the thread of each
	// one that still exists.
	states, err := s.threadStates(ctx, p, number)
	if err != nil {
		return nil, err
	}
	last := map[int64]Comment{}
	for _, c := range cs {
		if c.Kind != "review" {
			continue
		}
		root := c.root()
		if _, ok := states[root]; !ok {
			return nil, &UnavailableError{Err: fmt.Errorf("GitHub names no review thread for comment %d of #%d", root, number)}
		}
		if l, ok := last[root]; !ok || !c.CreatedAt.Before(l.CreatedAt) {
			last[root] = c
		}
	}
	for i, c := range cs {
		if c.Kind != "review" {
			continue
		}
		root := c.root()
		t := states[root]
		cs[i].Thread = &ThreadState{ID: root, Resolved: t.Resolved, Outdated: t.Outdated,
			AwaitingUs: !t.Resolved && last[root].AuthorClass != AuthorApp}
	}
	return cs, nil
}

const threadsQuery = `query LoopReviewThreads($owner: String!, $name: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes { isResolved isOutdated comments(first: 1) { nodes { databaseId } } }
      }
    }
  }
}`

// threadStates reads the state of each review thread of a PR, keyed by
// the database id of the thread's root comment, which REST names.
func (s *Service) threadStates(ctx context.Context, p config.Project, number int) (map[int64]ThreadState, error) {
	own, name, _ := strings.Cut(p.Repo, "/")
	out := map[int64]ThreadState{}
	var after any
	for page := 1; page <= maxCommentPages; page++ {
		var ans struct {
			Repository struct {
				PullRequest *struct {
					ReviewThreads struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []struct {
							IsResolved bool `json:"isResolved"`
							IsOutdated bool `json:"isOutdated"`
							Comments   struct {
								Nodes []struct {
									DatabaseID int64 `json:"databaseId"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		if err := s.GraphQL(ctx, p, ScopeRead, threadsQuery,
			map[string]any{"owner": own, "name": name, "number": number, "after": after}, &ans); err != nil {
			return nil, err
		}
		pr := ans.Repository.PullRequest
		if pr == nil {
			return nil, &UnavailableError{Err: fmt.Errorf("GitHub names no PR #%d", number)}
		}
		for _, t := range pr.ReviewThreads.Nodes {
			if len(t.Comments.Nodes) > 0 {
				out[t.Comments.Nodes[0].DatabaseID] = ThreadState{Resolved: t.IsResolved, Outdated: t.IsOutdated}
			}
		}
		if !pr.ReviewThreads.PageInfo.HasNextPage {
			break
		}
		after = pr.ReviewThreads.PageInfo.EndCursor
	}
	return out, nil
}

// awaitsAnswer reports whether a person's comment on a PR waits for the
// loop's answer: a person has the last word in a review thread, or in the
// conversation. The loop App's comment answers. Another program's comment
// is neither a question nor an answer, so a deployment notice after a
// person's comment answers nobody. A reply answers its thread alone. The
// REST API does not say whether a person resolved a thread, so only a
// reply settles one. Ported from unanswered in scripts/sdlc/pr-queues.ts
// and awaitingUs in scripts/sdlc/github-pull.ts (alpha).
func awaitsAnswer(cs []Comment) bool {
	last := map[string]Comment{}
	for _, c := range cs {
		if c.bot && c.AuthorClass != AuthorApp {
			continue
		}
		key := c.Kind
		if c.Kind == "review" {
			root := c.InReplyTo
			if root == 0 {
				root = c.ID
			}
			key = fmt.Sprintf("review:%d", root)
		}
		if l, ok := last[key]; !ok || c.CreatedAt.After(l.CreatedAt) || c.CreatedAt.Equal(l.CreatedAt) && c.ID > l.ID {
			last[key] = c
		}
	}
	for _, c := range last {
		if c.AuthorClass != AuthorApp {
			return true
		}
	}
	return false
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
	existing, err := s.allComments(ctx, p, number)
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
