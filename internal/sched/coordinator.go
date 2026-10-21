package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// CoordinatorConfig starts the coordinator session (R24, U12's resident
// agent).
type CoordinatorConfig struct {
	// Project is the project whose repository the coordinator's checkout
	// comes from. The coordinator itself serves every project.
	Project string
	Harness agenthost.Harness
	Model   string
	Effort  string
}

// CoordinatorStage names the coordinator in its brief and runbook.
const CoordinatorStage = "coordinator"

// coordinatorCheckout names the coordinator's one checkout.
const coordinatorCheckout = "loop-coordinator"

// CoordinatorState is the coordinator session the server keeps.
type CoordinatorState struct {
	RunID    string             `json:"run_id,omitempty"`
	Checkout string             `json:"checkout,omitempty"`
	Session  *agenthost.Session `json:"session,omitempty"`
}

// Coordinator returns the coordinator session as the store holds it.
func (s *Scheduler) Coordinator() CoordinatorState {
	cs, _ := s.loadCoordinator(context.Background())
	return cs
}

func (s *Scheduler) loadCoordinator(ctx context.Context) (CoordinatorState, error) {
	var cs CoordinatorState
	var raw string
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT payload FROM schedule WHERE kind = 'coordinator'`).Scan(&raw)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cs, nil
	}
	if err != nil {
		return cs, err
	}
	return cs, json.Unmarshal([]byte(raw), &cs)
}

func (s *Scheduler) saveCoordinator(ctx context.Context, cs CoordinatorState) error {
	raw, err := json.Marshal(cs)
	if err != nil {
		return err
	}
	return s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		ms := tx.Now().UnixMilli()
		res, err := tx.Exec(`UPDATE schedule SET payload = ?, updated_at = ? WHERE kind = 'coordinator'`, string(raw), ms)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
		_, err = tx.Exec(`INSERT INTO schedule (project, kind, payload, created_at, updated_at) VALUES ('', 'coordinator', ?, ?, ?)`,
			string(raw), ms, ms)
		return err
	})
}

// ensureCoordinator starts the coordinator session when none runs: at the
// first tick after the service starts, and after the session's process
// exits. A session whose liveness reads unknown is left alone, unless a
// complete inventory proves it gone.
func (s *Scheduler) ensureCoordinator(ctx context.Context) {
	cc := s.cfg.Coordinator
	if cc == nil || s.Draining() {
		return
	}
	cs, err := s.loadCoordinator(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "read the coordinator", "err", err)
		return
	}
	if cs.Session != nil {
		st, err := s.cfg.Agents.Settle(ctx, cs.Session.ID)
		if err == nil && st.Liveness == agenthost.Live {
			return
		}
		if err != nil || st.Liveness != agenthost.Exited {
			inv, ierr := s.cfg.Agents.Inventory(ctx)
			if ierr != nil || inv.Lookup(*cs.Session) != agenthost.Absent {
				return
			}
		}
		_ = s.cfg.Agents.Close(ctx, cs.Session.ID)
		if _, err := s.cfg.Tokens.Revoke(ctx, cs.RunID); err != nil {
			s.log.ErrorContext(ctx, "revoke the coordinator token", "err", err)
		}
		cs.Session = nil
		s.log.WarnContext(ctx, "the coordinator session ended; starting another", "run", cs.RunID)
	}
	if err := s.startCoordinator(ctx, cc, &cs); err != nil {
		s.raise(ctx, config.Alert{Kind: AlertCoordinator, DedupeKey: AlertCoordinator,
			Subject: "the coordinator session did not start", Body: err.Error()})
	}
	if err := s.saveCoordinator(ctx, cs); err != nil {
		s.log.ErrorContext(ctx, "store the coordinator", "err", err)
	}
}

func (s *Scheduler) startCoordinator(ctx context.Context, cc *CoordinatorConfig, cs *CoordinatorState) error {
	if cs.Checkout == "" {
		s.checkoutMu.Lock()
		co, err := s.cfg.Checkouts(cc.Project).CreateCheckout(ctx, agenthost.CheckoutSpec{Name: coordinatorCheckout, BaseBranch: DefaultBaseBranch})
		s.checkoutMu.Unlock()
		if err != nil {
			return fmt.Errorf("sched: the coordinator's checkout: %w", err)
		}
		cs.Checkout = co.Path
		if err := s.saveCoordinator(ctx, *cs); err != nil {
			return err
		}
	}
	model := cc.Model
	if model == "" {
		model = DefaultModels[cc.Harness]
	}
	run := "coordinator-" + s.cfg.NewRunID()
	b := brief.Brief{RunID: run, Stage: CoordinatorStage, Role: CoordinatorStage, Harness: string(cc.Harness), Model: model,
		BaseBranch: DefaultBaseBranch, Secrets: brief.Secrets{Present: []string{}, Missing: []string{}}}
	res, err := brief.Inject(ctx, brief.Injection{Checkout: cs.Checkout, Harness: cc.Harness, Pack: s.cfg.Pack, Brief: b})
	if err != nil {
		return err
	}
	if _, err := s.cfg.Tokens.IssueToCheckout(ctx, rpc.IssueRequest{RunID: run, Kind: cli.PrincipalCoordinator,
		Checkout: cs.Checkout, Release: s.cfg.Release}); err != nil {
		return err
	}
	sess, err := s.cfg.Agents.Start(ctx, agenthost.StartSpec{Kind: agenthost.KindCoordinator, Checkout: cs.Checkout,
		Harness: cc.Harness, Model: model, Effort: cc.Effort, PromptFile: res.Prompt, Title: "loop coordinator"})
	if err != nil {
		_, _ = s.cfg.Tokens.Revoke(ctx, run)
		return fmt.Errorf("sched: start the coordinator: %w", err)
	}
	cs.RunID, cs.Session = run, &sess
	return nil
}
