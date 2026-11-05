// Package coord names the coordinators of one instance: the loop project,
// the destination of each inbox entry and overwatch finding, and the reach
// of each coordinator token (spec factorio-1edo, Domain model).
package coord

import (
	"context"
	"strings"

	"github.com/iantanwx/loop/internal/config"
)

// LoopRepo is release.ReleaseRepo. coord cannot import release, which
// imports the server, so TestC1LoopRepoIsTheReleaseRepo holds the two equal.
const LoopRepo = "slothware-labs/factorio"

// LoopProject names the loop project: the one registered project whose
// repository is LoopRepo, matched without case. With no match, or with
// two, the host has no loop project, and ok is false.
func LoopProject(ps []config.Project) (id string, ok bool) {
	for _, p := range ps {
		if !strings.EqualFold(p.Repo, LoopRepo) {
			continue
		}
		if ok {
			return "", false
		}
		id, ok = p.ID, true
	}
	return id, ok
}

// Topology is the coordinators of one instance, at one tick.
type Topology struct {
	// Loop is the loop project, "" when the host has none.
	Loop string
	// Homes holds each project with a home_coordinator row.
	Homes map[string]bool
	// Old is the project of the session from before this release, "" once
	// it ended. Old is not in Homes.
	Old string
}

// Dest names the project whose coordinator gets an entry or a finding of
// project. Instance-wide work, and the work of a project with no
// coordinator, go to the loop project. "" means the host has no loop
// project.
func (t Topology) Dest(project string, instanceWide bool) string {
	switch {
	case instanceWide:
		return t.Loop
	case t.Homes[project], t.Old != "" && project == t.Old:
		return project
	}
	return t.Loop
}

// Reach is what one coordinator token may act on.
type Reach int

const (
	// ReachProject is its project only.
	ReachProject Reach = iota
	// ReachLoop is the loop project's: its project, each record that an
	// entry routed to it names, every project's registration, and every
	// instance-wide verb.
	ReachLoop
	// ReachUnscoped is the old session's: every record and every verb, as
	// before this release.
	ReachUnscoped
)

// Kind is the kind of a record that a call names.
type Kind string

// The kinds of record. KindProject is the --project flag of a call.
const (
	KindItem         Kind = "item"
	KindEntry        Kind = "entry"
	KindDecision     Kind = "decision"
	KindRoom         Kind = "room"
	KindAlert        Kind = "alert"
	KindSetting      Kind = "setting"
	KindRun          Kind = "run"
	KindFault        Kind = "fault"
	KindLearning     Kind = "learning"
	KindRegistration Kind = "registration"
	KindProject      Kind = "project"
)

// Record is one record that a call names, with the project that holds it.
type Record struct {
	Kind    Kind
	ID      string
	Project string
}

// Caller is one coordinator token, read against the topology of one call.
type Caller struct {
	Reach Reach
	// Project is the token's project, "" for the old session.
	Project string
	// Routed reports whether an open entry that routes to the loop project
	// names r.
	Routed func(ctx context.Context, r Record) (bool, error)
}

// CallerOf reads a coordinator token's project against the topology. An
// empty project is the old session.
func CallerOf(tokenProject string, t Topology, routed func(context.Context, Record) (bool, error)) Caller {
	c := Caller{Reach: ReachProject, Project: tokenProject, Routed: routed}
	switch tokenProject {
	case "":
		c.Reach = ReachUnscoped
	case t.Loop:
		c.Reach = ReachLoop
	}
	return c
}

// Reaches reports whether the caller may act on r.
func (c Caller) Reaches(ctx context.Context, r Record) (bool, error) {
	switch {
	case c.Reach == ReachUnscoped, r.Project == c.Project:
		return true, nil
	case c.Reach != ReachLoop:
		return false, nil
	case r.Kind == KindRegistration:
		return true, nil
	case c.Routed == nil:
		return false, nil
	}
	return c.Routed(ctx, r)
}
