package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/hexid"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/server"
)

// Layout is the instance directory (R40, KTD26):
//
//	versions/<v>/loop       each installed release, with its release.json
//	versions/<v>/skills/    the third-party skills that release pins
//	current -> versions/<v> the release the service runs
//	bin/bd, bin/dolt        the pinned binaries
//	loop.db                 the state database
//	keys/kek                the key file
//	admin.credential        the host-admin credential, mode 0600
//	snapshots/              database snapshots
//	release-state.json      the highest version ever installed
type Layout struct{ Dir string }

// Names inside the instance directory.
const (
	VersionsDir   = "versions"
	SkillsDir     = "skills"
	CurrentLink   = "current"
	BinDir        = "bin"
	SnapshotsDir  = "snapshots"
	StateFile     = "release-state.json"
	beforeRestore = ".before-restore"
)

func (l Layout) join(parts ...string) string {
	return filepath.Join(append([]string{l.Dir}, parts...)...)
}

// VersionDir is versions/<v>.
func (l Layout) VersionDir(v string) string { return l.join(VersionsDir, v) }

// Binary is versions/<v>/loop.
func (l Layout) Binary(v string) string { return l.join(VersionsDir, v, BinaryName) }

// Skills is versions/<v>/skills.
func (l Layout) Skills(v string) string { return l.join(VersionsDir, v, SkillsDir) }

// Bin is bin/<name>.
func (l Layout) Bin(name string) string { return l.join(BinDir, name) }

// Snapshots is the snapshot directory.
func (l Layout) Snapshots() string { return l.join(SnapshotsDir) }

// HostConfig is host.yaml.
func (l Layout) HostConfig() string { return l.join(config.HostConfigFile) }

// DB is the state database: host config's store.path, or loop.db.
func (l Layout) DB() string {
	if cfg, err := config.LoadHost(l.HostConfig()); err == nil && cfg.Store.Path != "" {
		return cfg.Store.Path
	}
	return l.join(server.DatabaseFile)
}

// KeyFile is host config's key_file, or keys/kek.
func (l Layout) KeyFile() string {
	if cfg, err := config.LoadHost(l.HostConfig()); err == nil && cfg.KeyFile != "" {
		return cfg.KeyFile
	}
	return l.join(server.KeyFile)
}

var dirName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// checkVersionName refuses a version that cannot name a directory.
func checkVersionName(v string) error {
	if !dirName.MatchString(v) || strings.Contains(v, "..") {
		return fmt.Errorf("release: %q cannot name a version directory", v)
	}
	return nil
}

// CurrentVersion is the version the current symlink names.
func (l Layout) CurrentVersion() (string, error) {
	target, err := os.Readlink(l.join(CurrentLink))
	if err != nil {
		return "", err
	}
	dir, v := filepath.Split(filepath.Clean(target))
	if filepath.Clean(dir) != VersionsDir || checkVersionName(v) != nil {
		return "", fmt.Errorf("release: %s points at %q, not at versions/<v>", l.join(CurrentLink), target)
	}
	return v, nil
}

// SetCurrent repoints current at versions/<v> atomically: a new symlink,
// then a rename over the old one.
func (l Layout) SetCurrent(v string) error {
	if err := checkVersionName(v); err != nil {
		return err
	}
	if _, err := os.Stat(l.Binary(v)); err != nil {
		return fmt.Errorf("release: version %s is not installed: %w", v, err)
	}
	tmp := l.join(".current-" + randomSuffix())
	if err := os.Symlink(filepath.Join(VersionsDir, v), tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.join(CurrentLink)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(l.Dir)
}

// Installed lists the installed versions, lowest first.
func (l Layout) Installed() ([]string, error) {
	entries, err := os.ReadDir(l.join(VersionsDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || checkVersionName(e.Name()) != nil {
			continue
		}
		if _, err := os.Stat(l.Binary(e.Name())); err == nil {
			out = append(out, e.Name())
		}
	}
	slices.SortFunc(out, CompareVersions)
	return out, nil
}

// Metadata reads versions/<v>/release.json.
func (l Layout) Metadata(v string) (Metadata, error) {
	return ReadMetadata(filepath.Join(l.VersionDir(v), MetadataFile))
}

// State is release-state.json.
type State struct {
	// Highest is the highest version ever installed. An upgrade below it
	// is refused (KTD26).
	Highest string `json:"highest"`
}

// ReadState reads release-state.json. A missing file is the zero State.
func (l Layout) ReadState() (State, error) {
	data, err := os.ReadFile(l.join(StateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("release: %s: %w", StateFile, err)
	}
	return s, nil
}

// Record raises the highest installed version to v.
func (l Layout) Record(v string) error {
	s, err := l.ReadState()
	if err != nil {
		return err
	}
	if s.Highest != "" && CompareVersions(v, s.Highest) <= 0 {
		return nil
	}
	s.Highest = v
	data, _ := json.Marshal(s)
	return writeFileAtomic(l.join(StateFile), append(data, '\n'), 0o600)
}

// WritePrevious records the release a run may still call from across a
// swap (the server's previous_version file, KTD13).
func (l Layout) WritePrevious(v string) error {
	return writeFileAtomic(l.join(server.PreviousVersionFile), []byte(v+"\n"), 0o600)
}

// lockInstance waits until no server holds the instance lock, then holds
// it, so nothing writes the database while a swap runs.
func (l Layout) lockInstance(ctx context.Context, timeout time.Duration) (*rpc.InstanceLock, error) {
	deadline := time.Now().Add(timeout)
	for {
		lock, err := rpc.LockInstance(l.Dir)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, rpc.ErrInstanceLocked) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("release: the server still holds %s after %v", l.Dir, timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func randomSuffix() string { return hexid.Random(6) }

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
