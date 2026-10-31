package merge_test

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/merge/direct"
)

// origin serves .loop/config.yaml as origin/main.
type origin map[string]string

func (o origin) read(_ context.Context, ref, path string) ([]byte, error) {
	if ref != "origin/main" {
		return nil, fs.ErrNotExist
	}
	b, ok := o[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(b), nil
}

func openAlerts(e *env, project string) int {
	var n int
	if err := e.st.Read(e.ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(e.ctx, `SELECT count(*) FROM alerts WHERE project = ? AND kind = ? AND state = 'open'`,
			project, config.AlertProjectConfigInvalid).Scan(&n)
	}); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// A project config that names an unknown mechanism keeps the last known
// good config, so merges go on through the old mechanism, and raises an
// alert (KTD20). A project with no last known good config cannot be added.
func TestUnknownMechanismKeepsTheLastKnownGoodConfigAndAlerts(t *testing.T) {
	e := newEnv(t, func(e *env) merge.Mechanism { return direct.New(e.svc) })
	loader := &config.Loader{LKG: config.StoreLKG(e.st), Alerts: config.StoreAlerter(e.st)}
	files := origin{config.ProjectConfigPath: "merge:\n  mechanism: direct\n"}
	e.source = func(ctx context.Context, p config.Project) (*config.ProjectSnapshot, error) {
		l, err := loader.Load(ctx, p.ID, config.Refs{Read: files.read}, "main")
		if err != nil {
			return nil, err
		}
		return &l.ProjectSnapshot, nil
	}

	id, _ := e.reviewed(sha('1'), true)
	e.tick()
	e.wantStatus(id, machine.Queued)

	files[config.ProjectConfigPath] = "merge:\n  mechanism: bors\n"
	e.tick()
	e.wantStatus(id, machine.Done) // direct, from the last known good config
	if n := openAlerts(e, testProject); n != 1 {
		t.Fatalf("%d open config alerts; want 1", n)
	}

	beta := config.Project{ID: "beta", Repo: "acme/gadgets", Path: e.project.Path + "-beta"}
	if err := config.StoreProjectRow(e.st)(e.ctx, beta); err != nil {
		t.Fatal(err)
	}
	if err := e.job.AddHook(e.ctx, beta); !errors.Is(err, config.ErrInvalid) {
		t.Fatalf("add with an unknown mechanism = %v; want ErrInvalid", err)
	}
	if n := openAlerts(e, "beta"); n != 1 {
		t.Fatalf("%d open config alerts for beta; want 1", n)
	}
}

// A project with no config yet, as before loop-init, can be added, and a
// tick leaves it alone until the config lands.
func TestAProjectWithNoConfigYetIsAddedAndWaits(t *testing.T) {
	e := newEnv(t, func(e *env) merge.Mechanism { return direct.New(e.svc) })
	loader := &config.Loader{LKG: config.StoreLKG(e.st), Alerts: config.StoreAlerter(e.st)}
	files := origin{}
	e.source = func(ctx context.Context, p config.Project) (*config.ProjectSnapshot, error) {
		l, err := loader.Load(ctx, p.ID, config.Refs{Read: files.read}, "main")
		if err != nil {
			return nil, err
		}
		return &l.ProjectSnapshot, nil
	}
	if err := e.job.AddHook(e.ctx, e.project); err != nil {
		t.Fatalf("add with no config = %v", err)
	}
	id, _ := e.reviewed(sha('1'), true)
	if err := e.job.TickProject(e.ctx, testProject); err != nil {
		t.Fatalf("tick with no config = %v", err)
	}
	e.wantStatus(id, machine.ReadyToMerge)
	if n := openAlerts(e, testProject); n != 0 {
		t.Fatalf("%d open config alerts; want none", n)
	}

	files[config.ProjectConfigPath] = "merge:\n  mechanism: direct\n"
	e.tick()
	e.wantStatus(id, machine.Queued)
}
