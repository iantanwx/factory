package userspace

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/config"
)

// Fetch returns one pin's verified tree, or an error that names the pin.
type Fetch func(ctx context.Context, pin config.SkillPin) (fs.FS, error)

// fetchTimeout bounds one fetch. A fetch outlives the config read that
// starts it.
const fetchTimeout = 5 * time.Minute

// outcome is how the last ended fetch of a pin came out.
type outcome int

const (
	none outcome = iota
	passed
	failed
)

// fetchRecord is all the syncer keeps of one pin. It holds no "held"
// value: Store.Held is the one answer, so a run and the syncer never
// disagree. running guards one fetch for each pin. last decides only the
// alert, and only for a pin that the store does not hold.
type fetchRecord struct {
	running bool
	last    outcome
	reason  string
}

type pinKey struct{ project, name, sha256 string }

// Syncer fetches each project's pins that the store lacks, and keeps one
// project_skills_missing alert for each project (ADR 0013 decision 2).
type Syncer struct {
	store  Store
	fetch  Fetch
	alerts config.Alerter
	log    *slog.Logger

	mu      sync.Mutex
	lists   map[string][]config.SkillPin // the list each project's last Observe saw
	records map[pinKey]*fetchRecord
	// settling orders the alert writes of each settle.
	settling sync.Mutex
	// ended, when set, runs after each fetch ends and its project settles.
	ended func(name string)
}

// NewSyncer returns a syncer that fetches with fetch into st, and raises
// and clears alerts through alerts.
func NewSyncer(st Store, fetch Fetch, alerts config.Alerter, log *slog.Logger) *Syncer {
	return &Syncer{store: st, fetch: fetch, alerts: alerts, log: log,
		lists: map[string][]config.SkillPin{}, records: map[pinKey]*fetchRecord{}}
}

// Observe records pins as project's list, and starts a fetch of each pin
// that the store does not hold and that no fetch runs for. It settles the
// project's alert, and returns before any fetch ends.
func (s *Syncer) Observe(ctx context.Context, project string, pins []config.SkillPin) {
	held := s.heldOf(project, pins)
	s.mu.Lock()
	s.lists[project] = append([]config.SkillPin(nil), pins...)
	for i, pin := range pins {
		if held[i] {
			continue
		}
		rec := s.record(project, pin)
		if rec.running {
			continue
		}
		rec.running = true
		go s.run(context.WithoutCancel(ctx), project, pin)
	}
	s.mu.Unlock()
	s.settle(ctx, project)
}

// record returns pin's record, and makes it. The caller holds mu.
func (s *Syncer) record(project string, pin config.SkillPin) *fetchRecord {
	k := pinKey{project, pin.Name, pin.SHA256}
	rec := s.records[k]
	if rec == nil {
		rec = &fetchRecord{}
		s.records[k] = rec
	}
	return rec
}

// run fetches and stores one pin, records the outcome and settles the
// project.
func (s *Syncer) run(ctx context.Context, project string, pin config.SkillPin) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	tree, err := s.fetch(ctx, pin)
	if err == nil {
		err = s.store.Put(project, pin, tree)
	}
	s.mu.Lock()
	rec := s.record(project, pin)
	rec.running, rec.last, rec.reason = false, passed, ""
	if err != nil {
		rec.last, rec.reason = failed, err.Error()
	}
	s.mu.Unlock()
	if err != nil {
		s.log.Warn("userspace: fetch a skill pin", "project", project, "skill", pin.Name, "err", err)
	}
	s.settle(ctx, project)
	if s.ended != nil {
		s.ended(pin.Name)
	}
}

// heldOf reports, for each pin, whether the store holds it.
func (s *Syncer) heldOf(project string, pins []config.SkillPin) []bool {
	held := make([]bool, len(pins))
	for i, pin := range pins {
		_, held[i] = s.store.Held(project, pin)
	}
	return held
}

// settle writes project's alert from the list its last Observe saw. When
// the store holds each pin, the alert clears. Otherwise each pin that the
// store does not hold and whose last ended fetch failed raises it. With
// none such, a first fetch still runs, and the alert stays as it is.
func (s *Syncer) settle(ctx context.Context, project string) {
	s.settling.Lock()
	defer s.settling.Unlock()
	s.mu.Lock()
	pins := s.lists[project]
	s.mu.Unlock()
	held := s.heldOf(project, pins)
	var names, reasons []string
	all := true
	s.mu.Lock()
	for i, pin := range pins {
		if held[i] {
			continue
		}
		all = false
		if rec := s.record(project, pin); rec.last == failed {
			names = append(names, pin.Name)
			reasons = append(reasons, pin.Name+": "+rec.reason)
		}
	}
	s.mu.Unlock()
	key := config.AlertProjectSkills + ":" + project
	var err error
	switch {
	case all:
		err = s.alerts.Clear(ctx, key)
	case len(names) > 0:
		err = s.alerts.Raise(ctx, config.Alert{
			Project: project, Kind: config.AlertProjectSkills, DedupeKey: key,
			Subject: fmt.Sprintf("runs of %s start without the userspace skills %s: each failed its fetch or its hash",
				project, strings.Join(names, ", ")),
			Body: strings.Join(reasons, "\n") + "\nThe server fetches each of them again at its next read of the project config.",
		})
	}
	if err != nil {
		s.log.Warn("userspace: settle the skills alert", "project", project, "err", err)
	}
}
