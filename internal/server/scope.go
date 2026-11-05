package server

import (
	"context"
	"database/sql"
	"errors"
	"regexp"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/fault"
	"github.com/iantanwx/loop/internal/inbox"
)

// The scope of a coordinator's verbs (spec factorio-1edo, Approach,
// "Scope"): rpc.Server.authorize reads the topology of each call, the
// project of each record the call names, and the records that an entry
// routed to the loop project names.

// topology is the coordinators of the instance now. A server with no
// scheduler has no coordinator session, and only its loop project.
func (s *Server) topology() coord.Topology {
	if s.Sched != nil {
		return s.Sched.Topology()
	}
	t := coord.Topology{Homes: map[string]bool{}}
	if ps, err := s.Projects.List(); err == nil {
		t.Loop, _ = coord.LoopProject(ps)
	}
	return t
}

// faultRef finds each fault id that a bug's body names.
var faultRef = regexp.MustCompile(`\bF-[1-9][0-9]*\b`)

// records names the project of each record inv names: its item, entry,
// decision, room, run, fault, setting or registration argument, its
// --entry and its --project. An item or a decision is read in the call's
// project, so it names --project, and none without it. A record that does
// not exist names none, and the verb's handler refuses it. A bug's body
// names its faults, the evidence of the loop project's coordinator's bug
// in another project.
func (s *Server) records(ctx context.Context, inv cli.Invocation) ([]coord.Record, error) {
	var out []coord.Record
	add := func(kind coord.Kind, id, project string) {
		out = append(out, coord.Record{Kind: kind, ID: id, Project: project})
	}
	flag := inv.Flags["project"]
	for _, a := range inv.Verb.Args {
		id := inv.Arg(a.Name)
		if id == "" {
			continue
		}
		switch a.Name {
		case "item", "decision":
			if flag != "" {
				add(coord.Kind(a.Name), id, flag)
			}
		case "entry":
			if err := s.addEntry(ctx, id, add); err != nil {
				return nil, err
			}
		case "room", "run":
			project, ok, err := s.projectOf(ctx, a.Name, id)
			if err != nil {
				return nil, err
			}
			if ok {
				add(coord.Kind(a.Name), id, project)
			}
		case "fault":
			if err := s.addFault(ctx, id, add); err != nil {
				return nil, err
			}
		case "project":
			kind := coord.KindRegistration
			if inv.Verb.Noun == "settings" {
				kind = coord.KindSetting
			}
			add(kind, id, id)
		}
	}
	if id := inv.Flags["entry"]; id != "" {
		if err := s.addEntry(ctx, id, add); err != nil {
			return nil, err
		}
	}
	if inv.Verb.Path == "work create" && inv.Flags["kind"] == "bug" {
		for _, id := range faultRef.FindAllString(inv.Flags["body"], -1) {
			if err := s.addFault(ctx, id, add); err != nil {
				return nil, err
			}
		}
	}
	if flag != "" {
		add(coord.KindProject, flag, flag)
	}
	return out, nil
}

func (s *Server) addEntry(ctx context.Context, id string, add func(coord.Kind, string, string)) error {
	if s.Inbox == nil {
		return nil
	}
	project, ok, err := s.Inbox.Owner(ctx, id)
	if ok {
		add(coord.KindEntry, id, project)
	}
	return err
}

func (s *Server) addFault(ctx context.Context, id string, add func(coord.Kind, string, string)) error {
	f, err := s.Faults.Get(ctx, id)
	switch {
	case errors.Is(err, fault.ErrNotFound):
		return nil
	case err != nil:
		return err
	}
	add(coord.KindFault, id, f.Project)
	return nil
}

// projectOf reads the project of a room or a run.
func (s *Server) projectOf(ctx context.Context, kind, id string) (project string, ok bool, err error) {
	q := `SELECT project FROM rooms WHERE id = ?`
	if kind == "run" {
		q = `SELECT project FROM runs WHERE run_id = ?`
	}
	err = s.Store.Read(ctx, func(tx *sql.Tx) error { return tx.QueryRowContext(ctx, q, id).Scan(&project) })
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return project, err == nil, err
}

// routed reports whether an open entry that routes to the loop project
// names r: by the entry's own id, its item or its run. Only a fault entry
// holds a run.
func (s *Server) routed(ctx context.Context, r coord.Record) (bool, error) {
	t := s.topology()
	if s.Inbox == nil || t.Loop == "" {
		return false, nil
	}
	es, err := s.Inbox.Entries(ctx)
	if err != nil {
		return false, err
	}
	for _, e := range es {
		if inbox.Route(e, t) == t.Loop && names(e, r) {
			return true, nil
		}
	}
	return false, nil
}

func names(e inbox.Entry, r coord.Record) bool {
	switch r.Kind {
	case coord.KindEntry:
		return e.ID == r.ID
	case coord.KindFault:
		return e.ID == inbox.KindFault+"/"+r.ID
	case coord.KindRoom:
		return e.ID == inbox.KindRoom+"/"+r.ID
	case coord.KindRun:
		return e.Run == r.ID
	case coord.KindItem, coord.KindDecision:
		return e.Item == r.ID && e.Project == r.Project
	}
	return false
}
