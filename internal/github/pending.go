package github

import (
	"context"
	"time"

	"github.com/iantanwx/loop/internal/config"
)

// Pending is one open item pull request that owes a reply, a row of `pr
// pending`. A project whose GitHub read failed answers one row with its
// Error and no pull request.
type Pending struct {
	Project string    `json:"project"`
	Item    string    `json:"item,omitempty"`
	Status  string    `json:"status,omitempty"` // the item's status, when it reads
	Number  int       `json:"pr,omitempty"`
	Branch  string    `json:"branch,omitempty"`
	Since   time.Time `json:"since,omitzero"` // the oldest unanswered comment
	Count   int       `json:"threads,omitempty"`
	Link    string    `json:"link,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// Pending lists the project's open pull requests whose head branch maps
// to an item, by SpecEpicOf, then ItemOf, and that hold a person's comment
// the App has not answered. It reads GitHub fresh on every call: a cache
// would be a second store of GitHub state, old when the coordinator acts
// on it.
func (s *Service) Pending(ctx context.Context, p config.Project) ([]Pending, error) {
	prs, err := s.OpenPulls(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	var out []Pending
	for _, pr := range prs {
		item := SpecEpicOf(pr.Head.Ref)
		if item == "" {
			item = ItemOf(pr.Head.Ref)
		}
		if item == "" {
			continue
		}
		cs, err := s.allComments(ctx, p, pr.Number)
		if err != nil {
			return nil, err
		}
		owed := unanswered(cs)
		if len(owed) == 0 {
			continue
		}
		row := Pending{Project: p.ID, Item: item, Number: pr.Number, Branch: pr.Head.Ref, Since: owed[0].CreatedAt,
			Count: len(owed), Link: pr.URL}
		for _, c := range owed[1:] {
			if c.CreatedAt.Before(row.Since) {
				row.Since = c.CreatedAt
			}
		}
		if s.cfg.Machine != nil {
			if it, ref := s.cfg.Machine.Read(ctx, p.ID, item); ref == nil {
				row.Status = it.Status
			}
		}
		out = append(out, row)
	}
	return out, nil
}
