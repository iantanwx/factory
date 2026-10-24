package overwatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/fault"
	"github.com/iantanwx/loop/internal/machine"
)

// loopRepo is release.ReleaseRepo, the repository of the loop's own
// project (spec, Decisions). overwatch cannot import release, which imports
// the server, so TestC21TheLoopRepositoryIsTheReleaseRepository holds this
// copy equal to it.
const loopRepo = "slothware-labs/factorio"

// bugsPart finds every filed issue that nobody filed: a loop fault that no
// bug of the loop's project names, and a reported bug in a project whose
// reproducer stage does not run (C20, C21, C22).
func bugsPart() Part {
	return Part{Name: "bugs", Command: "loop fault list", Read: readBugs}
}

// bugReport is one bug in reported, in a project whose config disables the
// reproducer stage.
type bugReport struct {
	project, id string
}

// faultsRead is what the bugs classifier reads: the pending faults, the
// loop's project ("" when no registered project holds loopRepo), each fault
// id a bug of that project names with the bug that names it, and the
// reports that nothing takes.
type faultsRead struct {
	faults  []fault.Fault
	project string
	filed   map[string]string // fault id -> bug id
	reports []bugReport
}

func readBugs(ctx context.Context, w World) (Reading, error) {
	var in faultsRead
	st := w.Store()
	if st == nil {
		return Reading{}, errors.New("no store to read the faults from")
	}
	err := st.Read(ctx, func(tx *sql.Tx) error {
		var err error
		in.faults, err = fault.Pending(ctx, tx)
		return err
	})
	if err != nil {
		return Reading{}, fmt.Errorf("read the faults: %w", err)
	}
	projects, err := w.Projects(ctx)
	if err != nil {
		return Reading{}, fmt.Errorf("read the projects: %w", err)
	}
	for _, p := range projects {
		if strings.EqualFold(p.Repo, loopRepo) {
			in.project = p.ID
		}
	}
	if in.project != "" {
		in.filed, err = faultsNamed(ctx, w, in.project)
		if err != nil {
			return Reading{}, err
		}
	}
	for _, p := range projects {
		reports, err := untakenReports(ctx, w, p.ID)
		if err != nil {
			return Reading{}, err
		}
		in.reports = append(in.reports, reports...)
	}
	return classifyBugs(in), nil
}

// faultsNamed reads the body of every bug of project, in any state, and
// returns each fault id a body holds as a token, with the first bug that
// holds it.
func faultsNamed(ctx context.Context, w World, project string) (map[string]string, error) {
	bugs, err := w.Items(ctx, project, machine.ListFilter{Kind: machine.KindBug})
	if err != nil {
		return nil, fmt.Errorf("read the bugs of %s: %w", project, err)
	}
	filed := map[string]string{}
	for _, b := range bugs {
		body, err := w.Body(ctx, project, b.ID)
		if err != nil {
			return nil, fmt.Errorf("read the body of %s: %w", b.ID, err)
		}
		for _, t := range Tokens(body) {
			if _, ok := fault.ParseID(t); ok && filed[t] == "" {
				filed[t] = b.ID
			}
		}
	}
	return filed, nil
}

// untakenReports lists the bugs in reported of project when its config
// disables the reproducer stage. With the stage on, stalls owns them.
func untakenReports(ctx context.Context, w World, project string) ([]bugReport, error) {
	bugs, err := w.Items(ctx, project, machine.ListFilter{Kind: machine.KindBug, Status: machine.Reported})
	if err != nil {
		return nil, fmt.Errorf("read the reported bugs of %s: %w", project, err)
	}
	if len(bugs) == 0 {
		return nil, nil
	}
	snap, err := w.ProjectConfig(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("read the config of %s: %w", project, err)
	}
	if snap == nil || snap.Config == nil {
		return nil, fmt.Errorf("read the config of %s: the project holds no config", project)
	}
	if snap.Config.StageEnabled(config.StageReproducer) {
		return nil, nil
	}
	out := make([]bugReport, 0, len(bugs))
	for _, b := range bugs {
		out = append(out, bugReport{project: project, id: b.ID})
	}
	return out, nil
}

// classifyBugs is the bugs part's rule. A pending fault that no bug of the
// loop's project names is work, keyed by its id. With no loop project, an
// open fault is work until its ack, and one note says why. A report that
// nothing takes is work, keyed overwatch:bugs:<bug id>.
func classifyBugs(in faultsRead) Reading {
	var r Reading
	if in.project == "" {
		r.Notes = append(r.Notes, fmt.Sprintf("no registered project holds the loop's repository %s, so no bug can file a fault", loopRepo))
	}
	for _, f := range in.faults {
		switch {
		case in.project == "" && f.Status == fault.Open:
			r.Findings = append(r.Findings, Finding{Key: f.ID,
				Line: fmt.Sprintf("%s is open and has no project to file it in: read it, then run `loop fault ack %s`", f.ID, f.ID)})
		case in.project == "":
		case in.filed[f.ID] != "":
			r.Notes = append(r.Notes, fmt.Sprintf("%s is filed in %s", f.ID, in.filed[f.ID]))
		default:
			r.Findings = append(r.Findings, Finding{Key: f.ID,
				Line: fmt.Sprintf("%s is %s and no bug names it: file a bug in %s whose body names %s", f.ID, f.Status, in.project, f.ID)})
		}
	}
	for _, b := range in.reports {
		key := Key("bugs", b.id)
		r.Findings = append(r.Findings, Finding{Key: key,
			Line: fmt.Sprintf("%s waits in reported, and %s disables the reproducer stage, so no stage takes it (%s)", b.id, b.project, key)})
	}
	return r
}
