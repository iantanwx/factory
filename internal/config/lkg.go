package config

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/iantanwx/loop/internal/store"
)

// AlertProjectConfigInvalid is the alert kind for a project whose committed
// config or manifest does not load (KTD20).
const AlertProjectConfigInvalid = "project_config_invalid"

// Alert is one alert a config load raises.
type Alert struct {
	Project   string
	Kind      string
	Subject   string
	DedupeKey string // one open alert exists for each key
	Body      string // holds no value: parse errors never quote one
}

// Alerter raises and clears alerts.
type Alerter interface {
	Raise(ctx context.Context, a Alert) error
	Clear(ctx context.Context, dedupeKey string) error
}

// LKGStore keeps each project's last known good config and manifest bytes.
type LKGStore interface {
	// LoadLKG returns the stored snapshot bytes, or nil when none is stored.
	LoadLKG(ctx context.Context, project string) (config, manifest []byte, err error)
	SaveLKG(ctx context.Context, project string, config, manifest []byte) error
}

// Loaded is the config a run uses.
type Loaded struct {
	ProjectSnapshot
	Stale   bool  // true when the default branch is invalid and this is the last known good config
	Problem error // why the default branch did not load, when Stale
}

// Loader reads project config from the default branch. When the file is
// invalid it keeps the last known good config and raises an alert (KTD20).
type Loader struct {
	LKG    LKGStore
	Alerts Alerter
}

func configAlertKey(project string) string { return AlertProjectConfigInvalid + ":" + project }

// Load reads project's config and manifest from defaultBranch. A valid read
// becomes the new last known good config and clears an open alert. An
// invalid read raises an alert and returns the last known good config with
// Stale set. With no last known good config it returns the read's error.
// Only a refusal of the file (ErrInvalid) falls back: an error that reading
// the ref returns, such as a missing mirror, goes to the caller.
func (l *Loader) Load(ctx context.Context, project string, read RefReader, defaultBranch string) (*Loaded, error) {
	snap, err := ReadProjectSnapshot(ctx, read, defaultBranch)
	if err == nil {
		if err := l.LKG.SaveLKG(ctx, project, snap.ConfigRaw, snap.ManifestRaw); err != nil {
			return nil, err
		}
		if err := l.Alerts.Clear(ctx, configAlertKey(project)); err != nil {
			return nil, err
		}
		return &Loaded{ProjectSnapshot: *snap}, nil
	}
	if !errors.Is(err, ErrInvalid) {
		return nil, err
	}
	problem := err
	craw, mraw, err := l.LKG.LoadLKG(ctx, project)
	if err != nil {
		return nil, errors.Join(problem, err)
	}
	if craw == nil && errors.Is(problem, ErrConfigMissing) {
		// A project that never had config is not set up yet; nothing broke.
		return nil, fmt.Errorf("config: project %s has no config yet: %w", project, problem)
	}
	if aerr := l.Alerts.Raise(ctx, Alert{
		Project:   project,
		Kind:      AlertProjectConfigInvalid,
		Subject:   "project config on " + DefaultRef(defaultBranch) + " does not load",
		DedupeKey: configAlertKey(project),
		Body:      problem.Error(),
	}); aerr != nil {
		return nil, errors.Join(problem, aerr)
	}
	if craw == nil {
		return nil, fmt.Errorf("config: project %s has no last known good config: %w", project, problem)
	}
	lkg, err := parseSnapshot(craw, mraw)
	if err != nil {
		// A stored snapshot that no longer parses means this binary's rules
		// changed. Report both problems.
		return nil, fmt.Errorf("config: project %s last known good config no longer loads: %w", project, errors.Join(problem, err))
	}
	return &Loaded{ProjectSnapshot: *lkg, Stale: true, Problem: problem}, nil
}

func parseSnapshot(craw, mraw []byte) (*ProjectSnapshot, error) {
	cfg, err := ParseProjectConfig(craw)
	if err != nil {
		return nil, err
	}
	snap := &ProjectSnapshot{Config: cfg, ConfigRaw: craw, ManifestRaw: mraw, Manifest: &Manifest{}}
	if mraw != nil {
		if snap.Manifest, err = ParseManifest(mraw); err != nil {
			return nil, err
		}
	}
	return snap, nil
}

// storeLKG keeps the last known good snapshot in the projects.config column.
// The store is transactional and is in every snapshot of the instance, and
// the column already exists for the project, so no second file needs its own
// atomic write and backup. The snapshot holds the raw bytes, and a load
// parses them again with the same strict parser.
type storeLKG struct{ st *store.Store }

// StoreLKG returns the LKGStore over the store's projects table. The
// project's row must exist (StoreProjectRow creates it).
func StoreLKG(st *store.Store) LKGStore { return storeLKG{st} }

type lkgDoc struct {
	Config   *string `json:"config,omitempty"`
	Manifest *string `json:"manifest,omitempty"`
}

func (s storeLKG) LoadLKG(ctx context.Context, project string) ([]byte, []byte, error) {
	var raw string
	err := s.st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT config FROM projects WHERE id = ?`, project).Scan(&raw)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("config: project %s has no store row: %w", project, ErrProjectNotFound)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("config: read last known good config: %w", err)
	}
	var doc lkgDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, nil, fmt.Errorf("config: decode last known good config: %w", err)
	}
	if doc.Config == nil {
		return nil, nil, nil
	}
	var mraw []byte
	if doc.Manifest != nil {
		mraw = []byte(*doc.Manifest)
	}
	return []byte(*doc.Config), mraw, nil
}

func (s storeLKG) SaveLKG(ctx context.Context, project string, config, manifest []byte) error {
	c := string(config)
	doc := lkgDoc{Config: &c}
	if manifest != nil {
		m := string(manifest)
		doc.Manifest = &m
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return s.st.Write(ctx, func(tx *store.Tx) error {
		var cur string
		if err := tx.QueryRow(`SELECT config FROM projects WHERE id = ?`, project).Scan(&cur); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("config: project %s has no store row: %w", project, ErrProjectNotFound)
			}
			return err
		}
		if bytes.Equal([]byte(cur), raw) {
			return nil
		}
		_, err := tx.Exec(`UPDATE projects SET config = ?, updated_at = ? WHERE id = ?`, string(raw), tx.Now().UnixMilli(), project)
		return err
	})
}

// storeAlerter writes the alerts table. One open row exists for each dedupe
// key, and a repeat raise counts up on it. The inbox reads it.
type storeAlerter struct{ st *store.Store }

// StoreAlerter returns an Alerter over the store's alerts table.
func StoreAlerter(st *store.Store) Alerter { return storeAlerter{st} }

func (s storeAlerter) Raise(ctx context.Context, a Alert) error {
	return s.st.Write(ctx, func(tx *store.Tx) error {
		now := tx.Now().UnixMilli()
		_, err := tx.Exec(`INSERT INTO alerts (project, kind, subject, dedupe_key, body, first_at, last_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (dedupe_key) WHERE state = 'open'
			DO UPDATE SET count = count + 1, last_at = excluded.last_at, body = excluded.body, subject = excluded.subject`,
			a.Project, a.Kind, a.Subject, a.DedupeKey, a.Body, now, now)
		if err != nil {
			return fmt.Errorf("config: raise alert: %w", err)
		}
		return nil
	})
}

func (s storeAlerter) Clear(ctx context.Context, dedupeKey string) error {
	return s.st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE alerts SET state = 'closed', closed_at = ? WHERE dedupe_key = ? AND state = 'open'`,
			tx.Now().UnixMilli(), dedupeKey)
		if err != nil {
			return fmt.Errorf("config: clear alert: %w", err)
		}
		return nil
	})
}
