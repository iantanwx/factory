package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// LiveLimit bounds a change in proposed or open, from its creation. Land
// fails the change at the limit. A handed change has no limit: the
// mechanism holds it, and ends it with a merge, a dequeue or a close.
const LiveLimit = 2 * time.Hour

// AlertChangeFailed tells the coordinator that a settings change failed.
// Its subject is <project>:<change id>.
const AlertChangeFailed = "settings_change_failed"

// NoticeSource is the source of the notice that a merged change puts.
const NoticeSource = "settings_change"

// GitHub is what Land reads and writes on GitHub: *github.Service.
type GitHub interface {
	GetPR(ctx context.Context, p config.Project, number int) (github.PR, error)
	RequiredChecks(ctx context.Context, p config.Project) ([]string, error)
	Checks(ctx context.Context, p config.Project, head string) (github.CIReport, error)
	PostStatus(ctx context.Context, p config.Project, sha, context, state, description string) error
	ClosePR(ctx context.Context, p config.Project, number int) error
}

// landing is the project's live change as Land reads it.
type landing struct {
	Change
	base    string // the default branch head the edit read
	created time.Time
}

// Land moves the project's live change one step. merge-handoff calls it
// on each tick of the project, before its tickets. It retries a proposed
// change's pull request, hands an open change to m once every required
// check is green at its head, and reads the outcome of a handed one. It
// fails a proposed or open change at LiveLimit.
func (s *Service) Land(ctx context.Context, p config.Project, m merge.Mechanism) error {
	c, err := s.landing(ctx, p.ID)
	if err != nil || c == nil {
		return err
	}
	switch c.State {
	case Proposed:
		return s.propose(ctx, p, c)
	case Open:
		return s.await(ctx, p, m, c)
	}
	return s.poll(ctx, p, m, c)
}

// Eligible reports whether PR n is a handed settings change at its
// recorded head. github-queue's sweep reads it beside the tickets'.
func (s *Service) Eligible(ctx context.Context, p config.Project, n int) (bool, error) {
	var head string
	err := s.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT head_sha FROM settings_changes WHERE project = ? AND pr = ? AND state = ?`,
			p.ID, n, Handed).Scan(&head)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	pr, err := s.GitHub.GetPR(ctx, p, n)
	if err != nil {
		return false, err
	}
	return pr.State == "open" && !pr.Merged() && pr.Head.SHA == head, nil
}

func (s *Service) landing(ctx context.Context, project string) (*landing, error) {
	c := landing{Change: Change{Project: project}}
	var created int64
	err := s.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT id, setting, old_value, old_source, new_value, reason, actor, branch, base_sha,
			coalesce(pr, 0), coalesce(head_sha, ''), state, created_at FROM settings_changes
			WHERE project = ? AND state IN ('proposed', 'open', 'handed')`, project).Scan(&c.ID, &c.Setting, &c.Old, &c.OldSource,
			&c.New, &c.Reason, &c.Actor, &c.Branch, &c.base, &c.PR, &c.Head, &c.State, &created)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("settings: read the live change of %s: %w", project, err)
	}
	c.created = time.Unix(created, 0)
	return &c, nil
}

// expired reports whether c reached LiveLimit.
func (s *Service) expired(c *landing) bool {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return !now().Before(c.created.Add(LiveLimit))
}

// propose runs the last step of the command again: it builds the file
// from the file at the recorded base, so each try writes the same bytes.
// GitHub's refusal fails the change. Any other error, such as a 5xx or a
// rate limit, leaves it proposed for the next tick.
func (s *Service) propose(ctx context.Context, p config.Project, c *landing) error {
	if s.expired(c) {
		return s.fail(ctx, p, c, false, "its pull request did not open before settings.LiveLimit")
	}
	repo, err := s.Repo(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("settings: fetch the default branch of %s: %w", p.ID, err)
	}
	raw, err := Repo{Dir: repo.Dir, Branch: repo.Branch, Head: c.base}.read(ctx, "", config.ProjectConfigPath)
	if err != nil {
		return err
	}
	hc, err := s.Host(ctx)
	if err != nil {
		return fmt.Errorf("settings: load host config: %w", err)
	}
	e, ref := editSetting(hc, raw, c.Setting, c.New, s.Defaults)
	if ref != nil {
		return s.fail(ctx, p, c, false, "the change no longer applies at its base: "+ref.Hint)
	}
	msg := c.message()
	subject, _, _ := strings.Cut(msg, "\n")
	pr, head, err := s.Propose(ctx, p.ID, github.FileChange{Branch: c.Branch, Path: config.ProjectConfigPath,
		BaseSHA: c.base, Content: e.raw, Message: msg, Title: subject, Body: "```\n" + msg + "```\n"})
	var api *github.APIError
	if errors.As(err, &api) && !api.Transient() {
		return s.fail(ctx, p, c, false, "GitHub refused its pull request: "+api.Error())
	}
	if err != nil {
		return fmt.Errorf("settings: propose change %d again: %w", c.ID, err)
	}
	return s.opened(ctx, &c.Change, pr, head)
}

// await hands an open change to m once every required check is green at
// the head the server wrote, and loop/eligible is posted on that head
// first. A pull request that a person merged makes the change merged.
func (s *Service) await(ctx context.Context, p config.Project, m merge.Mechanism, c *landing) error {
	pr, err := s.GitHub.GetPR(ctx, p, c.PR)
	if err != nil {
		return err
	}
	switch {
	case pr.Merged():
		return s.merged(ctx, p, c, pr.MergeCommitSHA)
	case pr.State != "open":
		return s.fail(ctx, p, c, false, fmt.Sprintf("pull request #%d was closed with no merge", c.PR))
	case pr.Head.SHA != c.Head:
		return s.fail(ctx, p, c, true, fmt.Sprintf("the head of #%d moved to %s; the server wrote %s",
			c.PR, github.Short(pr.Head.SHA), github.Short(c.Head)))
	case pr.MergeableState == merge.MergeableDirty:
		return s.fail(ctx, p, c, true, fmt.Sprintf("pull request #%d conflicts with its base", c.PR))
	}
	v, err := s.verdict(ctx, p, c)
	if err != nil {
		return err
	}
	switch {
	case v.State == github.CIRed:
		return s.fail(ctx, p, c, true, "a check failed at its head: "+strings.Join(v.Red, ", "))
	case s.expired(c):
		return s.fail(ctx, p, c, true, "it did not reach the mechanism before settings.LiveLimit; waiting for "+strings.Join(v.Waiting, ", "))
	case v.State != github.CIGreen || pr.MergeableState == merge.MergeableUnknown:
		return nil
	}
	if err := s.GitHub.PostStatus(ctx, p, c.Head, github.EligibleContext, "success",
		"every required check passed at the head the server wrote"); err != nil {
		return fmt.Errorf("settings: post %s on change %d: %w", github.EligibleContext, c.ID, err)
	}
	if err := m.HandOff(ctx, landPR(p, c, pr)); err != nil {
		return fmt.Errorf("settings: hand off #%d to %s: %w", c.PR, m.Name(), err)
	}
	return s.move(ctx, c, Handed, "")
}

// verdict reads the CI of c's head against the checks in force, and each
// check that a proposed merge.required_checks adds.
func (s *Service) verdict(ctx context.Context, p config.Project, c *landing) (github.CIVerdict, error) {
	required, err := s.GitHub.RequiredChecks(ctx, p)
	if err != nil {
		return github.CIVerdict{}, err
	}
	if c.Setting == "merge.required_checks" && c.New != nil {
		for name := range strings.SplitSeq(*c.New, ",") {
			if name != "" && !slices.Contains(required, name) {
				required = append(required, name)
			}
		}
	}
	ci, err := s.GitHub.Checks(ctx, p, c.Head)
	if err != nil {
		return github.CIVerdict{}, err
	}
	return ci.Against(required), nil
}

// poll reads the outcome of a handed change from m. A head that moved
// takes the pull request back from m and fails the change.
func (s *Service) poll(ctx context.Context, p config.Project, m merge.Mechanism, c *landing) error {
	pr, err := s.GitHub.GetPR(ctx, p, c.PR)
	if err != nil {
		return err
	}
	mp := landPR(p, c, pr)
	if !pr.Merged() && pr.State == "open" && pr.Head.SHA != c.Head {
		if err := m.Withdraw(ctx, mp); err != nil {
			return fmt.Errorf("settings: withdraw #%d from %s: %w", c.PR, m.Name(), err)
		}
		return s.fail(ctx, p, c, true, fmt.Sprintf("the head of #%d moved to %s; the server wrote %s",
			c.PR, github.Short(pr.Head.SHA), github.Short(c.Head)))
	}
	r, err := m.Poll(ctx, mp)
	if err != nil {
		return fmt.Errorf("settings: poll #%d in %s: %w", c.PR, m.Name(), err)
	}
	switch r.Outcome {
	case merge.Merged:
		return s.merged(ctx, p, c, r.SHA)
	case merge.Closed:
		return s.fail(ctx, p, c, false, fmt.Sprintf("pull request #%d was closed with no merge", c.PR))
	case merge.Dequeued:
		return s.fail(ctx, p, c, true, fmt.Sprintf("%s dequeued #%d (%s): %s", m.Name(), c.PR, r.Reason, r.Detail))
	}
	return nil
}

func landPR(p config.Project, c *landing, pr github.PR) merge.PR {
	return merge.PR{Project: p, Number: c.PR, Head: c.Head, Base: pr.Base.Ref}
}

// merged fetches the default branch, drops the project's cached config,
// and records the merge with its one notice in one write. A repeat read
// of the same merge writes nothing.
func (s *Service) merged(ctx context.Context, p config.Project, c *landing, sha string) error {
	repo, err := s.Repo(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("settings: fetch the default branch of %s: %w", p.ID, err)
	}
	if s.Forget != nil {
		s.Forget(p.ID)
	}
	n := s.notice(ctx, c, repo, sha)
	return s.Store.Write(ctx, func(tx *store.Tx) error {
		res, err := tx.Exec(`UPDATE settings_changes SET state = ?, merge_sha = ?, settled_at = ?
			WHERE id = ? AND state IN ('open', 'handed')`, Merged, sha, tx.Now().Unix(), c.ID)
		if err != nil {
			return err
		}
		if moved, _ := res.RowsAffected(); moved == 0 {
			return nil
		}
		_, err = inbox.PutNotice(tx, n)
		return err
	})
}

// notice is the notice of c, merged at sha. It names the value in force
// at the fetched head, so an --unset names the value it falls back to.
func (s *Service) notice(ctx context.Context, c *landing, repo Repo, sha string) inbox.Notice {
	after := "<not read>"
	if v, err := s.inForce(ctx, repo, c.Setting); err == nil {
		after = shown(v)
	}
	before := shown(config.Setting{Value: c.Old, Source: c.OldSource})
	return inbox.Notice{Project: c.Project, Source: NoticeSource, Ref: fmt.Sprint(c.ID),
		Subject: fmt.Sprintf("%s: %s %s to %s", c.Project, c.Setting, before, after),
		Body: fmt.Sprintf("Settings change %d of %s merged, and it is in force from the next tick.\n\n"+
			"- Setting: %s\n- Before: %s\n- After: %s\n- Merge commit: %s\n- Pull request: #%d\n- Changed by: %s\n\nThe reason:\n\n%s",
			c.ID, c.Project, c.Setting, before, after, sha, c.PR, c.Actor, inbox.AgentText(c.Actor, c.Reason))}
}

// inForce reads setting key in force at repo's head.
func (s *Service) inForce(ctx context.Context, repo Repo, key string) (config.Setting, error) {
	hc, err := s.Host(ctx)
	if err != nil {
		return config.Setting{}, err
	}
	raw, err := repo.read(ctx, "", config.ProjectConfigPath)
	if err != nil {
		return config.Setting{}, err
	}
	pc, err := config.ParseProjectConfig(raw)
	if err != nil {
		return config.Setting{}, err
	}
	return named(hc, pc, config.KnownStages(hc), s.Defaults, key), nil
}

// shown is a setting's value with its source, as a notice names it.
func shown(st config.Setting) string {
	v := st.Value
	if v == "" {
		v = "none"
	}
	return fmt.Sprintf("%s (%s)", v, st.Source)
}

// fail closes c's pull request when closePR is set, raises the alert for
// the coordinator, and makes the change failed. A close that GitHub does
// not answer leaves the change as it is, and the next tick tries again.
func (s *Service) fail(ctx context.Context, p config.Project, c *landing, closePR bool, detail string) error {
	if closePR {
		if err := s.GitHub.ClosePR(ctx, p, c.PR); err != nil {
			return fmt.Errorf("settings: close #%d of change %d: %w", c.PR, c.ID, err)
		}
	}
	if err := rpc.WriteAuditAlert(ctx, s.Store, s.Redactor, rpc.AuditAlert{Actor: "server:settings", Project: p.ID,
		Kind: AlertChangeFailed, Subject: fmt.Sprintf("%s:%d", p.ID, c.ID),
		Body: fmt.Sprintf("Settings change %d of %s failed in state %s: %s. Nothing changed in force, and the project takes a new change. "+
			"`loop settings set` runs it again once the cause is gone.", c.ID, c.Setting, c.State, detail),
		Detail: map[string]any{"change": c.ID, "setting": c.Setting, "state": c.State, "pr": c.PR}}); err != nil {
		return err
	}
	return s.move(ctx, c, Failed, detail)
}

// move sets the state of c to to, only while c holds the state Land read.
func (s *Service) move(ctx context.Context, c *landing, to State, detail string) error {
	return s.Store.Write(ctx, func(tx *store.Tx) error {
		var settled any
		if to == Failed || to == Merged {
			settled = tx.Now().Unix()
		}
		_, err := tx.Exec(`UPDATE settings_changes SET state = ?, detail = ?, settled_at = ? WHERE id = ? AND state = ?`,
			to, detail, settled, c.ID, c.State)
		return err
	})
}
