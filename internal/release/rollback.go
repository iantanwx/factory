package release

import (
	"context"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
)

// Rollback runs `loop rollback` (KTD26): it repoints current at the
// installed version below the current one and restarts the service. It
// restores nothing, so every write made after the upgrade survives. It
// refuses a rollback that the release metadata or the database forbids.
type Rollback struct {
	Host
}

// RollbackReport is the JSON of `loop rollback`.
type RollbackReport struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Started bool   `json:"started"`
}

// Run rolls back one version.
func (r *Rollback) Run(ctx context.Context) (RollbackReport, error) {
	from, err := r.Layout.CurrentVersion()
	if err != nil {
		return RollbackReport{}, refuse(cli.CodeNotFound, "not_installed", "the instance has no current release: %v", err)
	}
	installed, err := r.Layout.Installed()
	if err != nil {
		return RollbackReport{}, err
	}
	to := ""
	for _, v := range installed {
		if CompareVersions(v, from) < 0 {
			to = v
		}
	}
	if to == "" {
		return RollbackReport{}, refuse(cli.CodeNotFound, "no_earlier_version", "no version below %s is installed in %s", from, r.Layout.join(VersionsDir))
	}
	rep := RollbackReport{From: from, To: to}
	fromMeta, ferr := r.Layout.Metadata(from)
	toMeta, terr := r.Layout.Metadata(to)
	if ferr != nil || terr != nil {
		return rep, refuse(CodeNoRollback, "metadata_missing",
			"the release metadata of %s or %s cannot be read, so no rule proves the rollback safe; no verb repoints current at an older release, and loop restore recovers the database but keeps %s current",
			from, to, from)
	}
	dbMin, err := DatabaseMinReader(ctx, r.Layout.DB())
	if err != nil {
		return rep, err
	}
	if why := RollbackBlockers(fromMeta, toMeta, dbMin); len(why) > 0 {
		return rep, refuse(CodeNoRollback, "no_rollback",
			"%s; no rollback past %s is possible, and no verb repoints current at an older release; loop restore recovers the database but keeps %s current",
			strings.Join(why, "; "), from, from)
	}

	lock, err := r.stop(ctx)
	if err != nil {
		return rep, err
	}
	if err := r.Layout.SetCurrent(to); err != nil {
		return rep, r.abort(ctx, lock, err)
	}
	// A run started under the release rolled back from may keep calling.
	if err := r.Layout.WritePrevious(from); err != nil {
		return rep, r.abort(ctx, lock, err)
	}
	if err := r.start(ctx, lock); err != nil {
		return rep, err
	}
	rep.Started = true
	return rep, nil
}
