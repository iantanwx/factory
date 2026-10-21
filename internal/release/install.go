package release

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
)

// Installer sets up one instance (R40): the running binary in
// versions/<v>/, the current symlink, the pinned bd and dolt, the default
// host config, the key file, the host-admin credential and the service.
type Installer struct {
	Layout  Layout
	Service ServiceManager
	Prov    Provisioner
	// Pins are this platform's bd and dolt pins.
	Pins []Pin
	// Self is the path of the running binary.
	Self string
	// Meta is the running binary's release metadata.
	Meta Metadata
}

// InstallReport is the JSON of `loop install`.
type InstallReport struct {
	Instance   string        `json:"instance"`
	Version    string        `json:"version"`
	Current    string        `json:"current"`
	Unit       string        `json:"unit"`
	Binaries   []Provisioned `json:"binaries,omitempty"`
	Credential string        `json:"credential_file"`
	HostConfig string        `json:"host_config"`
	// Drift lists each difference from what install writes. Install
	// reports what remains after it ran. --check changes nothing.
	Drift []string `json:"drift"`
}

// Install sets the instance up and registers the service. It never
// replaces an existing host config, key file or credential, and it never
// moves current away from a version an upgrade installed.
func (in *Installer) Install(ctx context.Context) (InstallReport, error) {
	dir := in.Layout.Dir
	if err := checkInstancePath(dir); err != nil {
		return InstallReport{}, err
	}
	if err := checkVersionName(in.Meta.Version); err != nil {
		return InstallReport{}, err
	}
	for _, d := range []string{dir, in.Layout.join(VersionsDir), in.Layout.join(BinDir), in.Layout.Snapshots(), in.Layout.join("log")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return InstallReport{}, err
		}
	}
	if err := in.placeSelf(); err != nil {
		return InstallReport{}, err
	}
	if _, err := in.Layout.CurrentVersion(); errors.Is(err, fs.ErrNotExist) {
		if err := in.Layout.SetCurrent(in.Meta.Version); err != nil {
			return InstallReport{}, err
		}
	} else if err != nil {
		return InstallReport{}, err
	}
	if ValidVersion(in.Meta.Version) {
		if err := in.Layout.Record(in.Meta.Version); err != nil {
			return InstallReport{}, err
		}
	}
	bins, err := in.Prov.Provision(ctx, in.Pins, in.Layout.join(BinDir))
	if err != nil {
		return InstallReport{}, err
	}
	if _, err := os.Stat(in.Layout.HostConfig()); errors.Is(err, fs.ErrNotExist) {
		cfg := config.DefaultHost(dir)
		cfg.Beads = config.BeadsConfig{BD: in.Layout.Bin("bd"), Dolt: in.Layout.Bin("dolt")}
		if err := config.SaveHost(in.Layout.HostConfig(), cfg); err != nil {
			return InstallReport{}, err
		}
	}
	if err := secrets.EnsureKeyFile(in.Layout.KeyFile()); err != nil {
		return InstallReport{}, err
	}
	if _, err := rpc.EnsureAdminCredential(dir); err != nil {
		return InstallReport{}, err
	}
	unit, err := in.Service.Unit(dir)
	if err != nil {
		return InstallReport{}, err
	}
	if err := in.Service.Register(ctx, unit); err != nil {
		return InstallReport{}, fmt.Errorf("release: register the service: %w", err)
	}
	rep, err := in.Check(ctx)
	rep.Binaries = bins
	return rep, err
}

// placeSelf copies the running binary into versions/<v>/ with its
// release.json, unless the same bytes are there already.
func (in *Installer) placeSelf() error {
	v := in.Meta.Version
	dst := in.Layout.Binary(v)
	self, err := os.ReadFile(in.Self)
	if err != nil {
		return fmt.Errorf("release: read the running binary: %w", err)
	}
	if err := os.MkdirAll(in.Layout.VersionDir(v), 0o700); err != nil {
		return err
	}
	if have, err := os.ReadFile(dst); err != nil || !bytes.Equal(have, self) {
		if err := writeFileAtomic(dst, self, 0o755); err != nil {
			return err
		}
	}
	return writeFileAtomic(filepath.Join(in.Layout.VersionDir(v), MetadataFile), in.Meta.Encode(), 0o600)
}

// Check reports how the instance differs from what Install writes. It
// changes nothing.
func (in *Installer) Check(_ context.Context) (InstallReport, error) {
	dir := in.Layout.Dir
	rep := InstallReport{Instance: dir, Version: in.Meta.Version, Unit: in.Service.UnitPath(),
		Credential: cli.AdminCredentialPath(dir), HostConfig: in.Layout.HostConfig(), Drift: []string{}}
	drift := func(format string, args ...any) { rep.Drift = append(rep.Drift, fmt.Sprintf(format, args...)) }

	want, err := in.Service.Unit(dir)
	if err != nil {
		return rep, err
	}
	have, err := os.ReadFile(in.Service.UnitPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		drift("the service unit %s is not installed", in.Service.UnitPath())
	case err != nil:
		drift("the service unit %s cannot be read: %v", in.Service.UnitPath(), err)
	case !bytes.Equal(have, want):
		drift("the service unit %s differs from the one loop install writes", in.Service.UnitPath())
	}

	cur, err := in.Layout.CurrentVersion()
	if err != nil {
		drift("the current symlink is missing or wrong: %v", err)
	} else if _, err := os.Stat(in.Layout.Binary(cur)); err != nil {
		drift("current names %s, and %s is missing", cur, in.Layout.Binary(cur))
	}
	rep.Current = cur

	rep.Drift = append(rep.Drift, CheckPins(in.Pins, in.Layout.join(BinDir))...)

	cfg, err := config.LoadHost(in.Layout.HostConfig())
	if err != nil {
		drift("the host config cannot be used: %v", err)
	} else if cfg.Beads.BD != in.Layout.Bin("bd") || cfg.Beads.Dolt != in.Layout.Bin("dolt") {
		drift("the host config's beads.bd and beads.dolt do not name %s and %s", in.Layout.Bin("bd"), in.Layout.Bin("dolt"))
	}

	if _, err := secrets.NewFileKeyProvider(in.Layout.KeyFile()); err != nil {
		drift("the key file cannot be used: %v", err)
	}
	cred, err := cli.ReadAdminCredential(dir)
	if err != nil {
		drift("the host-admin credential file cannot be used: %v", err)
	} else if len(have) > 0 && strings.Contains(string(have), cred) {
		drift("the service unit holds the host-admin credential; remove it")
	}
	return rep, nil
}
