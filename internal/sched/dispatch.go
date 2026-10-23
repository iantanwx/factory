package sched

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/fault"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// Started is one run that dispatch started.
type Started struct {
	RunID    string            `json:"run_id"`
	Project  string            `json:"project"`
	Stage    string            `json:"stage"`
	Item     string            `json:"item,omitempty"`
	Checkout string            `json:"checkout"`
	Attempt  int               `json:"attempt,omitempty"`
	Profile  Profile           `json:"profile"`
	Session  agenthost.Session `json:"session"`
}

// Dispatch starts one run of stage on project, in the order of KTD7 and
// the admission flowchart: a manifest secret check before any claim,
// admission with its claim, the checkout, the project's setup command, the
// pack, brief and .env, the token, then the start. It returns nil and no
// refusal when the stage has no work now.
func (s *Scheduler) Dispatch(ctx context.Context, project, stage string) (*Started, *cli.Refusal) {
	// Intake has a throughput entry, and its cap counts epics (KTD30).
	// Intake itself runs in a room, attended, so no unattended run of it
	// starts.
	if stage == config.StageIntake {
		return nil, &cli.Refusal{Code: cli.CodeUsage, Rule: "intake_runs_in_a_room",
			Hint: "intake runs in a room the coordinator opens with `loop room open --kind intake`; the scheduler starts no intake run"}
	}
	if s.Draining() {
		return nil, &cli.Refusal{Code: cli.CodeDraining, Rule: "draining", Hint: "the server is draining and admits no new run"}
	}
	snap, err := s.cfg.ProjectConfig(ctx, project)
	if err != nil {
		return nil, cli.InternalRefusal(fmt.Errorf("sched: project config of %s: %w", project, err))
	}
	if ref := s.secretsReady(ctx, project, snap.Manifest); ref != nil {
		return nil, ref
	}
	hc, err := s.cfg.Host.Load(ctx)
	if err != nil {
		return nil, cli.InternalRefusal(err)
	}
	prof, why := s.choose(ctx, profiles(stage, snap.Config, hc))
	if prof == nil {
		return nil, &cli.Refusal{Code: cli.CodeCapFull, Rule: "no_profile",
			Hint: "every harness the stage may use is paused or out of quota: " + reasons(why)}
	}
	cand, ref := s.cfg.Admission.Peek(ctx, project, stage)
	if ref != nil || cand == nil {
		return nil, ref
	}
	ad, ref := s.cfg.Admission.Admit(ctx, admission.Request{Project: project, Stage: stage, Item: cand.Item,
		RunID: s.cfg.NewRunID(), Harness: string(prof.Harness), Model: prof.Model})
	if ref != nil {
		return nil, ref
	}
	return s.start(ctx, ad, snap, hc, *prof)
}

// secretsReady refuses a run whose manifest names a secret with no stored
// value, before any claim (R53, AE8). It puts one inbox item that names
// each missing secret, and it counts no failure.
func (s *Scheduler) secretsReady(ctx context.Context, project string, m *config.Manifest) *cli.Refusal {
	missing, err := s.cfg.Secrets.Missing(ctx, project, m)
	if err != nil {
		return cli.InternalRefusal(err)
	}
	if len(missing) == 0 {
		return nil
	}
	names := strings.Join(missing, ", ")
	s.raise(ctx, config.Alert{Project: project, Kind: AlertMissingSecret, DedupeKey: AlertMissingSecret + ":" + project,
		Subject: "project " + project + " starts no run: no stored value for " + names,
		Body: "Set each with `loop secret set NAME --project " + project + "`, or once for every project with `loop secret set NAME --scope global`, " +
			"or import them with `loop secret import --project " + project + "`. Missing: " + names})
	return &cli.Refusal{Code: cli.CodeMissingSecret, Rule: "missing_secret",
		Hint: fmt.Sprintf("project %s has no stored value for %s", project, names)}
}

// reasons joins why in the order of its keys, so one state always reads
// the same.
func reasons(why map[string]string) string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(why)) {
		out = append(out, k+": "+why[k])
	}
	return strings.Join(out, "; ")
}

// start runs one admitted run up to its session. Each failure settles the
// run uncounted, releases its claim and restores the frontier state.
func (s *Scheduler) start(ctx context.Context, ad admission.Admitted, snap *config.ProjectSnapshot, hc *config.HostConfig, prof Profile) (*Started, *cli.Refusal) {
	st := &Started{RunID: ad.RunID, Project: ad.Project, Stage: ad.Stage, Item: ad.Item, Attempt: ad.Attempt, Profile: prof}
	fail := func(ref *cli.Refusal, refused bool, kind string, cause error) (*Started, *cli.Refusal) {
		s.abandon(ctx, runRow{ID: ad.RunID, Project: ad.Project, Stage: ad.Stage, Item: ad.Item, Checkout: st.Checkout}, refused, cause)
		if kind != "" {
			s.raise(ctx, config.Alert{Project: ad.Project, Kind: kind, DedupeKey: kind + ":" + stageKey(ad.Project, ad.Stage),
				Subject: fmt.Sprintf("%s run %s did not start", ad.Stage, ad.RunID), Body: cause.Error()})
		}
		return nil, ref
	}
	s.checkoutMu.Lock()
	co, err := s.cfg.Checkouts(ad.Project).CreateCheckout(ctx, agenthost.CheckoutSpec{Name: checkoutName(ad), BaseBranch: DefaultBaseBranch})
	s.checkoutMu.Unlock()
	if err != nil {
		return fail(cli.InternalRefusal(fmt.Errorf("sched: create the checkout: %w", err)), false, AlertStartFailed, err)
	}
	st.Checkout = co.Path
	if err := s.recordCheckout(ctx, ad, co); err != nil {
		return fail(cli.InternalRefusal(err), false, AlertStartFailed, err)
	}
	if setup := snap.Config.Setup; setup != "" {
		if err := s.runSetup(ctx, co.Path, setup); err != nil {
			return fail(cli.InternalRefusal(err), false, AlertStartFailed, err)
		}
	}
	env, err := s.cfg.Secrets.Values(ctx, ad.Project, snap.Manifest)
	var miss *secrets.MissingSecretsError
	if errors.As(err, &miss) {
		ref := &cli.Refusal{Code: cli.CodeMissingSecret, Rule: "missing_secret",
			Hint: fmt.Sprintf("project %s has no stored value for %s", ad.Project, strings.Join(miss.Names, ", "))}
		return fail(ref, true, AlertMissingSecret, err)
	}
	if err != nil {
		return fail(cli.InternalRefusal(err), false, AlertStartFailed, err)
	}
	b := s.brief(ctx, ad, snap, hc, prof)
	res, err := brief.Inject(ctx, brief.Injection{Checkout: co.Path, Harness: prof.Harness, Pack: s.cfg.Pack, Brief: b, Env: env})
	var conflict *brief.ConflictError
	if errors.As(err, &conflict) {
		ref := &cli.Refusal{Code: cli.CodeConfigConflict, Rule: "injected_path_tracked", Item: ad.Item,
			Hint: "the repository tracks a path the loop injects: " + strings.Join(conflict.Paths, ", ") + "; move or rename it"}
		return fail(ref, true, AlertConfigConflict, err)
	}
	if err != nil {
		return fail(cli.InternalRefusal(err), false, AlertStartFailed, err)
	}
	if _, err := s.cfg.Tokens.IssueToCheckout(ctx, rpc.IssueRequest{RunID: ad.RunID, Kind: cli.PrincipalRun,
		Project: ad.Project, Item: ad.Item, Stage: ad.Stage, Checkout: co.Path, Epoch: ad.Lease.Epoch, Release: s.cfg.Release}); err != nil {
		return fail(cli.InternalRefusal(err), false, AlertStartFailed, err)
	}
	if err := s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE runs SET state = ?, pack_hash = ? WHERE run_id = ? AND ended_at IS NULL`, RunStarting, res.PackHash, ad.RunID)
		return err
	}); err != nil {
		return fail(cli.InternalRefusal(err), false, AlertStartFailed, err)
	}
	sess, err := s.cfg.Agents.Start(ctx, agenthost.StartSpec{Kind: agenthost.KindStage, Checkout: co.Path, Harness: prof.Harness,
		Model: prof.Model, Effort: prof.Effort, PromptFile: res.Prompt,
		Title: strings.TrimSpace(fmt.Sprintf("loop %s %s %s", ad.Project, ad.Stage, ad.Item))})
	if err != nil {
		return fail(cli.InternalRefusal(fmt.Errorf("sched: start the session: %w", err)), false, AlertStartFailed, err)
	}
	if err := s.setRun(ctx, ad.RunID, RunRunning, hostRecord{Session: &sess}); err != nil {
		// The session runs and the record lacks it. Close it, so nothing
		// runs that the server cannot watch.
		_ = s.cfg.Agents.Close(context.WithoutCancel(ctx), sess.ID)
		return fail(cli.InternalRefusal(err), false, AlertStartFailed, err)
	}
	st.Session = sess
	return st, nil
}

// checkoutName names a run's checkout. A run id is unique, so the name is.
func checkoutName(ad admission.Admitted) string {
	name := "loop-" + ad.Project + "-" + ad.Stage + "-" + ad.RunID
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '-'
	}, name)
}

func (s *Scheduler) recordCheckout(ctx context.Context, ad admission.Admitted, co agenthost.Checkout) error {
	return s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		now := tx.Now().UnixMilli()
		_, err := tx.Exec(`INSERT INTO checkouts (id, project, item, path, branch, run_id, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, ad.RunID, ad.Project, ad.Item, co.Path, co.Branch, ad.RunID, CheckoutActive, now, now)
		return err
	})
}

// runSetup runs the project's setup command in the checkout before .env
// exists (KTD18). Its output is not kept: it may echo the environment.
func (s *Scheduler) runSetup(ctx context.Context, dir, setup string) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.SetupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", setup)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sched: the project's setup command failed in %s: %w", dir, err)
	}
	return nil
}

// brief builds the run's brief. It names the manifest secrets and holds no
// value. An agent job's run follows the runbook its host config entry
// names in the repository, not one of the pack.
func (s *Scheduler) brief(ctx context.Context, ad admission.Admitted, snap *config.ProjectSnapshot, hc *config.HostConfig, prof Profile) brief.Brief {
	b := brief.Brief{
		RunID: ad.RunID, Project: ad.Project, Stage: ad.Stage, Role: ad.Stage, Harness: string(prof.Harness), Model: prof.Model,
		BaseBranch: DefaultBaseBranch, Attempt: ad.Attempt,
		Secrets: brief.Secrets{Present: snap.Manifest.Names(), Missing: []string{}},
	}
	if j, ok := hc.AgentJob(ad.Stage); ok {
		b.Runbook = j.Runbook
	}
	// The brief names the loop faults filed from this project and stage
	// (KTD31), so the run learns their status without asking.
	if fs, err := fault.ForBrief(ctx, s.cfg.Store, ad.Project, ad.Stage, s.now()); err != nil {
		s.log.WarnContext(ctx, "brief: read the loop faults", "err", err)
	} else {
		b.Faults = fs
	}
	if ad.Item == "" {
		return b
	}
	it, ref := s.cfg.Machine.Show(ctx, ad.Project, ad.Item)
	if ref != nil {
		s.log.WarnContext(ctx, "brief: read the item", "item", ad.Item, "hint", ref.Hint)
		return b
	}
	bi := &brief.Item{ID: it.ID, Kind: string(it.Kind), Title: it.Title, Status: it.Status, Facts: it.Facts}
	if s.cfg.Beads != nil {
		if bc, err := s.cfg.Beads(ad.Project); err == nil {
			if is, err := bc.Get(ctx, ad.Item); err == nil {
				_, body := machine.TaskText(is)
				bi.Body, bi.Documents = body, admission.DeclaredPaths(body)
			}
		}
	}
	b.Item, b.AllowedNext = bi, it.AllowedNext
	if n := it.Fact(machine.FactNote); n != "" {
		b.Handoff = []string{n}
	}
	b.PriorFailures = s.priorFailures(ctx, ad)
	return b
}

// priorFailures lists the notes of the counted failures with the run's
// inputs.
func (s *Scheduler) priorFailures(ctx context.Context, ad admission.Admitted) []string {
	var out []string
	_ = s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT class, note FROM failures WHERE project = ? AND item = ? AND stage = ? AND inputs_hash = ?
			AND class IN ('failed', 'crashed') ORDER BY recorded_at`, ad.Project, ad.Item, ad.Stage, ad.Inputs)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var class, note string
			if err := rs.Scan(&class, &note); err != nil {
				return err
			}
			out = append(out, strings.TrimSpace(class+": "+note))
		}
		return rs.Err()
	})
	return out
}

// abandon settles a run that never reached running. It never counts: the
// run is not-ready, or refused when refused is set (CONFIG_CONFLICT, a
// secret removed after the check). It releases the claim, revokes the
// token and removes .env.
func (s *Scheduler) abandon(ctx context.Context, r runRow, refused bool, cause error) {
	ctx = context.WithoutCancel(ctx)
	s.settleMu.Lock()
	defer s.settleMu.Unlock()
	note := ""
	if cause != nil {
		note = cause.Error()
	}
	if _, err := s.cfg.Admission.RecordOutcome(ctx, admission.Settlement{RunID: r.ID, NotReady: true, Ended: s.now(), Note: note}); err != nil {
		s.log.ErrorContext(ctx, "settle an unstarted run", "run", r.ID, "err", err)
	}
	if refused {
		if err := s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
			_, err := tx.Exec(`UPDATE runs SET outcome_class = 'refused' WHERE run_id = ?`, r.ID)
			return err
		}); err != nil {
			s.log.ErrorContext(ctx, "mark a refused run", "run", r.ID, "err", err)
		}
	}
	s.afterSettle(ctx, r, "release", CheckoutSettled)
}

// afterSettle frees what a settled run held: the lease with verb, .env,
// the token. The checkout takes state.
func (s *Scheduler) afterSettle(ctx context.Context, r runRow, verb, state string) bool {
	released := false
	if r.Item != "" {
		var err error
		if released, err = s.releaseLease(ctx, r.Project, r.Item, r.ID, verb); err != nil {
			s.log.WarnContext(ctx, "free the lease; the next tick tries again", "run", r.ID, "item", r.Item, "err", err)
		}
	}
	if r.Checkout != "" {
		if err := brief.RemoveEnv(r.Checkout); err != nil {
			s.log.WarnContext(ctx, "remove .env", "run", r.ID, "err", err)
		}
		if err := s.setCheckoutState(ctx, r.ID, state); err != nil {
			s.log.ErrorContext(ctx, "settle the checkout", "run", r.ID, "err", err)
		}
	}
	if _, err := s.cfg.Tokens.Revoke(ctx, r.ID); err != nil {
		s.log.ErrorContext(ctx, "revoke the token", "run", r.ID, "err", err)
	}
	return released
}

// releaseLease applies verb (release or reclaim) when run still holds the
// item. Both restore the stage-correct state, never beads' open (R26).
func (s *Scheduler) releaseLease(ctx context.Context, project, item, run, verb string) (bool, error) {
	l, err := s.cfg.Machine.Lease(ctx, project, item)
	if err != nil {
		return false, err
	}
	if l.Owner != run {
		return false, nil
	}
	res := s.cfg.Machine.Apply(ctx, machine.Request{Project: project, Item: item, Verb: verb, Actor: machine.Server("sched")})
	if res.Refusal != nil {
		return false, fmt.Errorf("%s %s: %s: %s", verb, item, res.Refusal.Code, res.Refusal.Hint)
	}
	return true, nil
}

func (s *Scheduler) now64() int64 { return s.now().UnixMilli() }
