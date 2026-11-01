package github

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/qa"
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

// QAGate is the machine's QA gate. It computes the plan of the QA run at
// head as loop qa plan does in the run's checkout, checks r against it,
// and scans each evidence file for a secret. It then pushes the evidence
// commit, and stores r in qa_reports with that commit. So a push that
// fails stores nothing, and the verdict does not apply. A report stored
// again before its verdict applied replaces the row. The review text is
// the rendered report, with the link of each evidence file.
func (s *Service) QAGate(ctx context.Context, project, item, run, head string, r qa.Report, files map[string][]byte) (string, *cli.Refusal) {
	p, err := s.project(project)
	if err != nil {
		ref := Refusal(err)
		return "", &ref
	}
	plan, ref := s.qaPlan(ctx, p, item, run, head)
	if ref != nil {
		return "", ref
	}
	filed, bug, ref := s.qaBugs(ctx, project, run)
	if ref != nil {
		return "", ref
	}
	if ref := qa.Validate(r, plan, run, files, filed, bug); ref != nil {
		return "", ref
	}
	if ref := s.secretInEvidence(files); ref != nil {
		return "", ref
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", cli.InternalRefusal(err)
	}
	commit, err := s.PushEvidence(ctx, p, item, run, files)
	if err != nil {
		ref := Refusal(err)
		return "", &ref
	}
	err = s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO qa_reports (run_id, project, item, head, conclusion, report, evidence_commit, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (run_id) DO UPDATE SET item = excluded.item, head = excluded.head,
			conclusion = excluded.conclusion, report = excluded.report, evidence_commit = excluded.evidence_commit,
			created_at = excluded.created_at`,
			run, project, item, head, r.Conclusion(), string(raw), commit, tx.Now().UnixMilli())
		return err
	})
	if err != nil {
		return "", cli.InternalRefusal(err)
	}
	title, summary, text := qa.Render(r, evidenceLinks(p, commit, r))
	return "## " + title + "\n\n" + summary + "\n" + text, nil
}

// qaPlan is the plan of QA run run on item at head: the verify skills of
// the mirror's tree at head, the "May change" entries of the ticket, and
// the design skill of the run's row.
func (s *Service) qaPlan(ctx context.Context, p config.Project, item, run, head string) (qa.Plan, *cli.Refusal) {
	var design string
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT design_skill FROM runs WHERE run_id = ?`, run).Scan(&design)
	})
	if err != nil {
		return qa.Plan{}, cli.InternalRefusal(fmt.Errorf("github: the row of run %s: %w", run, err))
	}
	it, ref := s.cfg.Machine.Read(ctx, p.ID, item)
	if ref != nil {
		return qa.Plan{}, ref
	}
	tree, err := s.verifySkills(ctx, p, head)
	if err != nil {
		return qa.Plan{}, cli.InternalRefusal(err)
	}
	apps, err := qa.Apps(tree)
	if err != nil {
		return qa.Plan{}, &cli.Refusal{Code: cli.CodeUsage, Rule: "bad_verify_skill", Hint: err.Error()}
	}
	return qa.MakePlan(apps, admission.MayChangeEntries(it.Body), design), nil
}

// verifySkills holds each .claude/skills/verify-<app>/SKILL.md of the
// mirror's tree at head, at its path, which is all qa.Apps reads.
func (s *Service) verifySkills(ctx context.Context, p config.Project, head string) (fs.FS, error) {
	m := s.Mirror(p)
	if m.Resolve(ctx, head) == "" {
		return nil, fmt.Errorf("github: the mirror of %s holds no %s; run loop branch sync", p.ID, short(head))
	}
	const dir = ".claude/skills"
	entries, err := m.Tree(ctx, head, dir)
	if err != nil {
		return nil, err
	}
	tree := fstest.MapFS{}
	for _, e := range entries {
		if !e.Dir || !strings.HasPrefix(e.Name, "verify-") {
			continue
		}
		path := dir + "/" + e.Name + "/SKILL.md"
		data, err := m.Show(ctx, head, path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		tree[path] = &fstest.MapFile{Data: data}
	}
	return tree, nil
}

// qaBugs lists the bugs QA run run filed, and answers for any bug id. A
// bug with no source fact reads as filed by the operator.
func (s *Service) qaBugs(ctx context.Context, project, run string) ([]string, func(string) (qa.QABug, bool), *cli.Refusal) {
	m := s.cfg.Machine
	items, ref := m.List(ctx, project, machine.ListFilter{Kind: machine.KindBug, Facts: map[string]string{machine.FactQARun: run}})
	if ref != nil {
		return nil, nil, ref
	}
	var filed []string
	for _, it := range items {
		filed = append(filed, it.ID)
	}
	bug := func(id string) (qa.QABug, bool) {
		it, ref := m.Read(ctx, project, id)
		if ref != nil || it.Kind != machine.KindBug {
			return qa.QABug{}, false
		}
		return qa.QABug{Source: cmp.Or(it.Fact(machine.FactSource), machine.SourceOperator), Run: it.Fact(machine.FactQARun)}, true
	}
	return filed, bug, nil
}

// QAReported is the machine's QAReported: it enqueues the loop/qa check
// run of each stored report of project that holds none.
func (s *Service) QAReported(ctx context.Context, project string) error {
	p, err := s.project(project)
	if err != nil {
		return err
	}
	_, err = s.EnqueueQAChecks(ctx, p)
	return err
}

// EnqueueQAChecks enqueues the checkrun effect of each stored report of p
// whose verdict applied and that holds none, and returns how many it
// enqueued. The effect joins the intent of the verdict, and is runnable
// from its insert. Its natural key is checkRunKey(project, head, run), so
// a repeat enqueues nothing. So a server that dies after a report and
// before its effect posts the check run at the next tick.
func (s *Service) EnqueueQAChecks(ctx context.Context, p config.Project) (int, error) {
	type stored struct {
		intent                                   int64
		run, item, head, conclusion, raw, commit string
	}
	var due []stored
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT t.intent_id, r.run_id, r.item, r.head, r.conclusion, r.report, r.evidence_commit
			FROM qa_reports r JOIN transitions t ON t.project = r.project AND t.item = r.item AND t.run_id = r.run_id
				AND t.verb IN (?, ?)
			WHERE r.project = ? AND NOT EXISTS (SELECT 1 FROM outbox o WHERE o.effect_kind = ?
				AND o.natural_key = r.project || ':' || r.head || ':' || ? || ':' || r.run_id)
			ORDER BY r.created_at, r.run_id`, machine.VerbQAPass, machine.VerbQAFail, p.ID, EffectCheckRun, QAContext)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r stored
			if err := rows.Scan(&r.intent, &r.run, &r.item, &r.head, &r.conclusion, &r.raw, &r.commit); err != nil {
				return err
			}
			due = append(due, r)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	added := 0
	for _, r := range due {
		var rep qa.Report
		if err := json.Unmarshal([]byte(r.raw), &rep); err != nil {
			return added, fmt.Errorf("github: the QA report of %s: %w", r.run, err)
		}
		title, summary, text := qa.Render(rep, evidenceLinks(p, r.commit, rep))
		payload, err := json.Marshal(CheckRunPayload{Project: p.ID, Item: r.item, SHA: r.head, Name: QAContext,
			Conclusion: r.conclusion, Title: title, Summary: summary, Text: text})
		if err != nil {
			return added, err
		}
		err = s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
			now := tx.Now().UnixMilli()
			res, err := tx.Exec(`INSERT OR IGNORE INTO outbox (intent_id, effect_kind, natural_key, payload, state, created_at, updated_at)
				VALUES (?, ?, ?, ?, 'runnable', ?, ?)`, r.intent, EffectCheckRun, checkRunKey(p.ID, r.head, r.run), string(payload), now, now)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			added += int(n)
			return nil
		})
		if err != nil {
			return added, err
		}
	}
	return added, nil
}
