package overwatch

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
)

// pullsPart reads each project's open pull requests. Two open pull
// requests for one item and one document are work (spec, C23).
var pullsPart = Part{Name: "pulls", Read: readPulls}

// The documents of an item's pull request: an epic's spec on its spec
// branch, and any other work on the item's own branch.
const (
	docSpec = "spec"
	docWork = "work"
)

func readPulls(ctx context.Context, w World) (Reading, error) {
	if w.GitHub() == nil {
		return noGitHub, nil
	}
	projects, err := w.Projects(ctx)
	if err != nil {
		return Reading{}, err
	}
	var r Reading
	for _, p := range projects {
		prs, err := w.GitHub().OpenPulls(ctx, p)
		if err != nil {
			r.Notes = append(r.Notes, unreadLine(p, "the open pull requests", err))
			continue
		}
		r.add(classifyPulls(p, prs))
	}
	return r, nil
}

// classifyPulls maps each open pull request's head branch to its item and
// document with SpecEpicOf, else ItemOf. A pull request from a fork, or on
// a branch that maps to no item, belongs to no item. An item and a
// document with two open pull requests are one finding.
func classifyPulls(p config.Project, prs []github.PR) Reading {
	type slot struct{ item, doc string }
	held := map[slot][]int{}
	mapped := 0
	for _, pr := range prs {
		if pr.State != "open" || pr.Head.Repo == nil || !strings.EqualFold(pr.Head.Repo.FullName, p.Repo) {
			continue
		}
		s := slot{github.SpecEpicOf(pr.Head.Ref), docSpec}
		if s.item == "" {
			s = slot{github.ItemOf(pr.Head.Ref), docWork}
		}
		if s.item == "" {
			continue
		}
		mapped++
		held[s] = append(held[s], pr.Number)
	}
	slots := make([]slot, 0, len(held))
	for s, nums := range held {
		if len(nums) > 1 {
			slots = append(slots, s)
		}
	}
	slices.SortFunc(slots, func(a, b slot) int { return cmp.Or(cmp.Compare(a.item, b.item), cmp.Compare(a.doc, b.doc)) })
	var r Reading
	for _, s := range slots {
		nums := held[s]
		slices.Sort(nums)
		refs := make([]string, len(nums))
		for i, n := range nums {
			refs[i] = fmt.Sprintf("#%d", n)
		}
		key := Key("pulls", p.ID, s.item, s.doc)
		r.Findings = append(r.Findings, Finding{Key: key,
			Line: fmt.Sprintf("%s: %s has %d open pull requests for its %s, %s. Close all but one. %s",
				p.ID, s.item, len(nums), s.doc, joinAnd(refs), key)})
	}
	if len(slots) == 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("%s: %d open pull requests, %d of them on an item's branch, no two for one item and one document",
			p.ID, len(prs), mapped))
	}
	return r
}

// add appends o's findings and notes to r.
func (r *Reading) add(o Reading) {
	r.Findings = append(r.Findings, o.Findings...)
	r.Notes = append(r.Notes, o.Notes...)
}

// noGitHub is the reading of a GitHub part on a server with no GitHub
// App. Such a server reads no repository, so the part holds no work.
var noGitHub = Reading{Notes: []string{"the server holds no GitHub App, so it reads no repository"}}

// unreadLine is the line of one project whose GitHub read failed. It
// costs that project's lines only, and it makes the part code 2.
func unreadLine(p config.Project, what string, err error) string {
	return fmt.Sprintf("UNREAD: %s: read %s of %s: %v", p.ID, what, p.Repo, err)
}

// joinAnd joins a list as prose: "a", "a and b", "a, b and c".
func joinAnd(s []string) string {
	if len(s) < 2 {
		return strings.Join(s, "")
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}
