package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/store"
)

// State is where one settings change stands.
type State string

// The states of a change: proposed -> open -> handed -> merged, and
// proposed | open | handed -> failed.
const (
	Proposed State = "proposed" // recorded; its pull request is not open yet
	Open     State = "open"     // its pull request is open at Head
	Handed   State = "handed"   // the merge mechanism holds it
	Merged   State = "merged"
	Failed   State = "failed"
)

// Change is one settings change by `loop settings set`. At most one change
// of a project is proposed, open or handed.
type Change struct {
	ID        int64         `json:"change"`
	Project   string        `json:"project"`
	Setting   string        `json:"setting"`
	Old       string        `json:"old"` // the value in force when the command read the file
	OldSource config.Source `json:"old_source"`
	New       *string       `json:"new"` // nil: --unset
	Reason    string        `json:"reason"`
	Actor     string        `json:"actor"`
	Branch    string        `json:"branch"`
	PR        int           `json:"pr,omitempty"`
	Head      string        `json:"head,omitempty"` // the commit the server wrote
	State     State         `json:"state"`
	Detail    string        `json:"detail,omitempty"`
}

// errLive is a write that found the project's live change.
var errLive = errors.New("settings: the project holds a live change")

// liveQuery reads the project's change that is proposed, open or handed.
const liveQuery = `SELECT id, setting, branch, coalesce(pr, 0), state FROM settings_changes
	WHERE project = ? AND state IN ('proposed', 'open', 'handed')`

// live returns the project's live change, or nil.
func (s *Service) live(ctx context.Context, project string) (*Change, error) {
	c := Change{Project: project}
	err := s.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, liveQuery, project).Scan(&c.ID, &c.Setting, &c.Branch, &c.PR, &c.State)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("settings: read the live change of %s: %w", project, err)
	}
	return &c, nil
}

// pending is the SETTINGS_PENDING refusal of a change while c is live.
func pending(c Change) *cli.Refusal {
	where := "branch " + c.Branch
	if c.PR != 0 {
		where = "pull request #" + strconv.Itoa(c.PR)
	}
	return &cli.Refusal{Code: cli.CodeSettingsPending, Rule: "settings_pending",
		Hint: fmt.Sprintf("change %d of %s is %s on %s; a new change waits until it merges or fails", c.ID, c.Setting, c.State, where)}
}

// record stores c as proposed over base, the default branch head the edit
// read, and names its branch. It returns errLive when the project holds a
// live change.
func (s *Service) record(ctx context.Context, c *Change, base string) error {
	return s.Store.Write(ctx, func(tx *store.Tx) error {
		var id int64
		switch err := tx.QueryRow(liveQuery, c.Project).Scan(&id, new(string), new(string), new(int), new(string)); {
		case err == nil:
			return errLive
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		res, err := tx.Exec(`INSERT INTO settings_changes (project, setting, old_value, old_source, new_value, reason, actor,
			branch, base_sha, state, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?)`,
			c.Project, c.Setting, c.Old, c.OldSource, c.New, c.Reason, c.Actor, base, Proposed, tx.Now().Unix())
		if err != nil {
			return err
		}
		if c.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		c.Branch, c.State = "loop/settings/"+strconv.FormatInt(c.ID, 10), Proposed
		_, err = tx.Exec(`UPDATE settings_changes SET branch = ? WHERE id = ?`, c.Branch, c.ID)
		return err
	})
}

// opened records that c's pull request pr opened at head.
func (s *Service) opened(ctx context.Context, c *Change, pr int, head string) error {
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE settings_changes SET state = ?, pr = ?, head_sha = ? WHERE id = ? AND state = ?`,
			Open, pr, head, c.ID, Proposed)
		return err
	})
	if err == nil {
		c.State, c.PR, c.Head = Open, pr, head
	}
	return err
}
