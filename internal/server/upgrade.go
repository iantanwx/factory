package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// UpgradeStarter starts `loop upgrade` to version, the latest when empty,
// as a process outside every checkout and outside this server's service.
// It returns once that process started. The upgrade then drains, stops
// and restarts this server.
type UpgradeStarter func(ctx context.Context, version string) (UpgradeStart, error)

// UpgradeStart names the upgrade a starter began.
type UpgradeStart struct {
	// Unit is the service manager's name for the upgrade process, where
	// its output lands.
	Unit    string   `json:"unit"`
	Command []string `json:"command"`
}

// ErrBadVersion is a version that names no release.
var ErrBadVersion = errors.New("not a release version such as v1.2.3")

// coordinatorUpgrade is `loop coordinator upgrade`: the coordinator relays
// the operator's decision to upgrade, and the server starts the upgrade
// (ADR 0010). The coordinator works inside its checkout, so the host verb
// refuses it there; the server's own process starts outside every
// checkout. A run's token never reaches this verb: the matrix admits the
// coordinator alone.
func (s *Server) coordinatorUpgrade(ctx context.Context, call *rpc.Call) cli.Result {
	if s.opts.Upgrade == nil {
		return cli.Reject(cli.CodeNotImplemented, "no_upgrader",
			"this server cannot start an upgrade; the operator runs `loop upgrade` from outside every checkout")
	}
	version, body := call.Inv.Arg("version"), strings.TrimSpace(call.Inv.Flags["body"])
	if body == "" {
		return cli.Reject(cli.CodeUsage, "no_body", "--body holds the operator's decision, in the operator's own words")
	}
	if r := s.Redactor.Refusal("secret_in_answer", "decision", body); r != nil {
		return r.Result()
	}
	st, err := s.opts.Upgrade(ctx, version)
	if errors.Is(err, ErrBadVersion) {
		return cli.Reject(cli.CodeUsage, "bad_version", err.Error())
	}
	if err != nil {
		return cli.Internal(fmt.Errorf("start the upgrade: %w", err))
	}
	actor := call.Principal.Actor()
	if err := s.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO audit (at, actor, action, detail) VALUES (?, ?, 'upgrade_requested',
			json_object('from', ?, 'to', ?, 'unit', ?, 'decision', ?))`, tx.Now().UnixMilli(), actor, cli.Version, version, st.Unit, body)
		return err
	}); err != nil {
		return cli.Internal(err)
	}
	s.log.InfoContext(ctx, "upgrade started", "actor", actor, "from", cli.Version, "to", version, "unit", st.Unit)
	to := version
	if to == "" {
		to = "the latest release"
	}
	return cli.Found(map[string]any{"started": true, "from": cli.Version, "version": version, "unit": st.Unit, "command": st.Command,
		"next": fmt.Sprintf("the upgrade to %s drains this server, stops it and starts it again; after the restart, run `loop version` and report the version it names", to)})
}
