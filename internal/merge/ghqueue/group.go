package ghqueue

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
)

// GroupPrefix starts the branch of every merge group. GitHub names a
// group's branch gh-readonly-queue/<base>/pr-<number>-<sha>, where sha is
// the commit the group builds on: the base tip, or the commit of the
// group ahead of it. GitHub may change this format. A name the sweep
// cannot read gets no status, so a change fails closed, never open.
// docs/evidence/github-queue.md records the format a live run saw.
const GroupPrefix = "gh-readonly-queue/"

var groupName = regexp.MustCompile(`^pr-(\d+)-([0-9a-f]{40})$`)

// group is one merge-group branch.
type group struct {
	pr     int
	parent string // the commit the group builds on
	sha    string // the group's commit
}

// maxChain bounds the walk through groups ahead of one group.
const maxChain = 100

// Sweep reads the merge groups of the project's base branch and posts
// loop/eligible on each group commit whose every PR is eligible at its
// queued head. A group's PRs are its own PR and the PRs of the groups it
// builds on, back to the base tip. A group of unknown composition gets
// nothing.
func (q *Queue) Sweep(ctx context.Context, p config.Project, eligible func(context.Context, int) (bool, error)) error {
	base, err := q.gh.DefaultBranch(ctx, p)
	if err != nil {
		return err
	}
	refs, err := q.gh.MatchingRefs(ctx, p, GroupPrefix+base+"/")
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		q.prune(p.ID, nil)
		return nil
	}
	tip, err := q.gh.BranchSHA(ctx, p, base)
	if err != nil {
		return err
	}
	bySHA := map[string]group{}
	var groups []group
	for _, r := range refs {
		m := groupName.FindStringSubmatch(strings.TrimPrefix(r.Ref, "refs/heads/"+GroupPrefix+base+"/"))
		if m == nil {
			continue // an unreadable name: no status
		}
		n, _ := strconv.Atoi(m[1])
		g := group{pr: n, parent: m[2], sha: r.SHA}
		groups = append(groups, g)
		bySHA[g.sha] = g
	}
	live := map[string]bool{}
	memo := map[int]bool{}
	var errs []error
	for _, g := range groups {
		live[g.sha] = true
		if q.isPosted(p.ID, g.sha) {
			continue
		}
		ok, err := q.groupEligible(ctx, g, tip, bySHA, memo, eligible)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			continue
		}
		if err := q.gh.PostStatus(ctx, p, g.sha, github.EligibleContext, "success",
			"every PR in this merge group passed the loop's review at its queued head"); err != nil {
			errs = append(errs, fmt.Errorf("post %s on group %s: %w", github.EligibleContext, github.Short(g.sha), err))
			continue
		}
		q.markPosted(p.ID, g.sha)
	}
	q.prune(p.ID, live)
	return errors.Join(errs...)
}

// groupEligible walks from g to the base tip and asks about each PR once.
func (q *Queue) groupEligible(ctx context.Context, g group, tip string, bySHA map[string]group, memo map[int]bool,
	eligible func(context.Context, int) (bool, error)) (bool, error) {
	cur := g
	for range maxChain {
		ok, seen := memo[cur.pr]
		if !seen {
			var err error
			if ok, err = eligible(ctx, cur.pr); err != nil {
				return false, err
			}
			memo[cur.pr] = ok
		}
		if !ok {
			return false, nil
		}
		if cur.parent == tip {
			return true, nil
		}
		next, found := bySHA[cur.parent]
		if !found {
			return false, nil // built on a commit the sweep cannot place
		}
		cur = next
	}
	return false, nil
}

func (q *Queue) isPosted(project, sha string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.posted[project][sha]
}

func (q *Queue) markPosted(project, sha string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.posted[project] == nil {
		q.posted[project] = map[string]bool{}
	}
	q.posted[project][sha] = true
}

// prune forgets the groups that left the queue.
func (q *Queue) prune(project string, live map[string]bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for sha := range q.posted[project] {
		if !live[sha] {
			delete(q.posted[project], sha)
		}
	}
}
