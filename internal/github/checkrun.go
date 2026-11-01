package github

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/store"
)

// QAContext is the name of the QA stage's check run (ADR 0014 decision 3).
const QAContext = "loop/qa"

// EffectCheckRun is the outbox effect kind of one post of a loop/qa check
// run. Its natural key is checkRunKey.
const EffectCheckRun = "checkrun"

// checkRunKey is "<project>:<sha>:loop/qa:<post>". post is the QA run id
// for a report, "off" for a stage-off head, or "carried" for a head that
// base_merged made. So each post to one head is an outbox row of its own.
func checkRunKey(project, sha, post string) string {
	return project + ":" + sha + ":" + QAContext + ":" + post
}

// CheckRunPayload is the payload of one checkrun effect.
type CheckRunPayload struct {
	Project    string `json:"project"`
	Item       string `json:"item"`
	SHA        string `json:"sha"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	Text       string `json:"text"`
}

// PostCheckRun updates the one loop/qa check run of c.SHA that
// qa_check_runs names, or that GitHub lists from the loop App. It creates
// one only when neither holds one, and stores its id. So a server that
// dies between the create and the row updates that check run on the
// retry, and each head keeps one check run.
func (s *Service) PostCheckRun(ctx context.Context, p config.Project, c CheckRunPayload) error {
	if !shaRe.MatchString(c.SHA) {
		return fmt.Errorf("github: %q is not a full sha", c.SHA)
	}
	var id int64
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT check_run_id FROM qa_check_runs WHERE project = ? AND sha = ?`, p.ID, c.SHA).Scan(&id)
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	stored := id != 0
	if !stored {
		if id, err = s.loopCheckRun(ctx, p, c.SHA, c.Name); err != nil {
			return err
		}
	}
	body := map[string]any{"status": "completed", "conclusion": c.Conclusion,
		"output": map[string]string{"title": c.Title, "summary": c.Summary, "text": c.Text}}
	if id != 0 {
		_, err = s.repoDo(ctx, p, ScopeChecks, Call{Method: http.MethodPatch, Path: repoPath(p, "/check-runs/"+strconv.FormatInt(id, 10)),
			Body: body, Repeatable: true}, nil)
	} else {
		body["name"], body["head_sha"] = c.Name, c.SHA
		var made struct {
			ID int64 `json:"id"`
		}
		_, err = s.repoDo(ctx, p, ScopeChecks, Call{Method: http.MethodPost, Path: repoPath(p, "/check-runs"), Body: body}, &made)
		if err == nil && made.ID <= 0 {
			err = fmt.Errorf("github: the check run answer for %s names no id", short(c.SHA))
		}
		id = made.ID
	}
	if err != nil || stored {
		return err
	}
	return s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO qa_check_runs (project, sha, check_run_id) VALUES (?, ?, ?)
			ON CONFLICT (project, sha) DO UPDATE SET check_run_id = excluded.check_run_id`, p.ID, c.SHA, id)
		return err
	})
}

// loopCheckRun is the id of the newest check run named name on sha from
// the loop App, or 0.
func (s *Service) loopCheckRun(ctx context.Context, p config.Project, sha, name string) (int64, error) {
	app, err := s.AppID(ctx, p)
	if err != nil {
		return 0, err
	}
	var batch struct {
		Runs []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			App  struct {
				ID int64 `json:"id"`
			} `json:"app"`
		} `json:"check_runs"`
	}
	q := url.Values{"check_name": {name}, "app_id": {strconv.FormatInt(app, 10)}, "filter": {"all"}, "per_page": {"100"}}
	if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/commits/"+sha+"/check-runs?"+q.Encode()), &batch); err != nil {
		return 0, err
	}
	var id int64
	for _, r := range batch.Runs {
		if r.Name == name && r.App.ID == app {
			id = max(id, r.ID)
		}
	}
	return id, nil
}

type checkRunEffect struct {
	id       int64
	attempts int
	due      bool
	// newer is the id of a newer runnable or done effect of the same
	// head, or 0.
	newer   int64
	payload CheckRunPayload
}

// RunCheckRunEffects posts the newest due checkrun effect of each head, by
// outbox id, and marks each older effect of that head done with no post,
// with a note that names the newer one. So the last post to a head wins,
// even when an older effect waits on a retry. A failed post settles as a
// status post does.
func (s *Service) RunCheckRunEffects(ctx context.Context) error {
	var effects []checkRunEffect
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT o.id, o.attempts, o.next_attempt_at <= ?, o.payload,
				coalesce((SELECT max(n.id) FROM outbox n WHERE n.effect_kind = o.effect_kind AND n.id > o.id
					AND n.state IN ('runnable', 'done')
					AND json_extract(n.payload, '$.project') = json_extract(o.payload, '$.project')
					AND json_extract(n.payload, '$.sha') = json_extract(o.payload, '$.sha')), 0)
			FROM outbox o WHERE o.state = 'runnable' AND o.effect_kind = ? ORDER BY o.id LIMIT 100`,
			s.cfg.Now().UnixMilli(), EffectCheckRun)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var e checkRunEffect
			var raw string
			if err := rows.Scan(&e.id, &e.attempts, &e.due, &raw, &e.newer); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(raw), &e.payload); err != nil {
				return fmt.Errorf("github: outbox %d payload: %w", e.id, err)
			}
			effects = append(effects, e)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range effects {
		switch {
		case e.newer != 0:
			err = s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
				_, err := tx.Exec(`UPDATE outbox SET state = 'done', last_error = ?, updated_at = ? WHERE id = ? AND state = 'runnable'`,
					fmt.Sprintf("not posted: outbox %d is a newer post to this head", e.newer), tx.Now().UnixMilli(), e.id)
				return err
			})
		case e.due:
			err = s.runCheckRun(ctx, e)
		default:
			continue
		}
		var rl *RateLimitError
		if errors.As(err, &rl) {
			return nil // every later call waits for the same cooldown
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) runCheckRun(ctx context.Context, e checkRunEffect) error {
	pl := e.payload
	p, err := s.project(pl.Project)
	if err != nil {
		return s.cfg.Store.Write(ctx, func(tx *store.Tx) error { return tx.MarkEffectFailed(e.id, err.Error()) })
	}
	return s.settleEffect(ctx, e.id, e.attempts, pl.Project, pl.Item, pl.SHA, QAContext, s.PostCheckRun(ctx, p, pl))
}
