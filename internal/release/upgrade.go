package release

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/store"
)

// DefaultReleaseBase is where `loop upgrade` reads releases: the GitHub
// release download path of the loop repository. The release's signature,
// not this location, decides whether a release is trusted.
const DefaultReleaseBase = "https://github.com/iantanwx/loop/releases/download"

// ReleaseBaseEnv overrides DefaultReleaseBase with another URL or an
// absolute directory that holds <version>/<asset>. It names a location
// only: a release from anywhere must still verify.
const ReleaseBaseEnv = "LOOP_RELEASE_BASE"

// Source reads release assets from Base/<version>/<asset>.
type Source struct {
	// Base is a URL, or an absolute directory.
	Base string
	// HTTP is the client. http.DefaultClient when nil.
	HTTP *http.Client
}

func (s Source) get(ctx context.Context, version, asset string) ([]byte, error) {
	base := s.Base
	if base == "" {
		base = DefaultReleaseBase
	}
	if filepath.IsAbs(base) {
		return httpGet(ctx, nil, filepath.Join(base, version, asset), maxArchive)
	}
	return httpGet(ctx, s.HTTP, strings.TrimSuffix(base, "/")+"/"+version+"/"+asset, maxArchive)
}

// LatestVersion reads the version of the newest published release from
// its release.json. The version only names what to fetch: Run verifies
// the release as it would any named version.
func (s Source) LatestVersion(ctx context.Context) (string, error) {
	base := s.Base
	if base == "" {
		base = DefaultReleaseBase
	}
	var data []byte
	var err error
	switch {
	case filepath.IsAbs(base):
		data, err = httpGet(ctx, nil, filepath.Join(base, "latest", MetadataFile), maxArchive)
	case strings.HasSuffix(strings.TrimSuffix(base, "/"), "/releases/download"):
		// GitHub serves the newest published release under latest/download.
		root := strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/download")
		data, err = httpGet(ctx, s.HTTP, root+"/latest/download/"+MetadataFile, maxArchive)
	default:
		data, err = httpGet(ctx, s.HTTP, strings.TrimSuffix(base, "/")+"/latest/"+MetadataFile, maxArchive)
	}
	if err != nil {
		return "", err
	}
	m, err := ParseMetadata(data)
	if err != nil {
		return "", err
	}
	if !ValidVersion(m.Version) {
		return "", fmt.Errorf("release: the latest release names %q", m.Version)
	}
	return m.Version, nil
}

// Upgrader runs `loop upgrade <version>` (KTD26). It never prompts.
type Upgrader struct {
	Host
	Source Source
	// Keys are the trusted release keys this binary embeds.
	Keys []PublicKey
	// Platform is "<GOOS>/<GOARCH>" of this host.
	Platform string
	Prov     Provisioner
	// Self is the running binary's metadata. It stands in for the current
	// release when versions/<current>/release.json is missing.
	Self Metadata
}

// UpgradeReport is the JSON of `loop upgrade`.
type UpgradeReport struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Changed is false when the version was already current.
	Changed  bool          `json:"changed"`
	Drain    DrainReport   `json:"drain"`
	Snapshot string        `json:"snapshot,omitempty"`
	Pins     []Provisioned `json:"pins,omitempty"`
	// NoRollback lists why `loop rollback` cannot return from this
	// release. Empty means a rollback stays possible.
	NoRollback []string `json:"no_rollback"`
	Started    bool     `json:"started"`
}

// verified is a fetched release that passed every check.
type verified struct {
	meta    Metadata
	bin     []byte
	sums    []byte
	sig     []byte
	metaRaw []byte
}

// Run upgrades to version. Every check runs before the first state
// change, so a refused release leaves the instance and the service as
// they were. Then it drains, stops the service, takes a snapshot, installs
// the release into versions/<v>/, repoints current and starts the service.
func (u *Upgrader) Run(ctx context.Context, version string) (UpgradeReport, error) {
	if !ValidVersion(version) {
		return UpgradeReport{}, refuse(cli.CodeUsage, "bad_version", "%q is not a release version such as v1.2.3", version)
	}
	from, err := u.Layout.CurrentVersion()
	if err != nil {
		return UpgradeReport{}, refuse(cli.CodeNotFound, "not_installed", "the instance has no current release; run loop install first: %v", err)
	}
	rep := UpgradeReport{From: from, To: version, NoRollback: []string{}}
	if from == version {
		return rep, nil
	}
	rel, err := u.fetch(ctx, version)
	if err != nil {
		return rep, err
	}
	fromMeta, err := u.Layout.Metadata(from)
	if err != nil {
		fromMeta = u.Self
	}
	dbMin, err := DatabaseMinReader(ctx, u.Layout.DB())
	if err != nil {
		return rep, err
	}
	if err := u.checkRules(from, rel.meta, dbMin); err != nil {
		return rep, err
	}
	pins, err := PinsFor(rel.meta.Pins, u.Platform)
	if err != nil {
		return rep, refuse(CodeVersionRefused, "platform_unpinned", "%v", err)
	}
	var bins map[string][]byte
	if len(CheckPins(pins, u.Layout.join(BinDir))) > 0 {
		if bins, err = u.Prov.Fetch(ctx, pins); errors.Is(err, ErrPinMismatch) {
			return rep, refuse(CodeChecksumMismatch, "pin_mismatch", "%v", err)
		} else if err != nil {
			return rep, refuse(CodeReleaseUnavailable, "pin_fetch_failed", "%v", err)
		}
	}
	rep.NoRollback = append(rep.NoRollback, RollbackBlockers(rel.meta, fromMeta, dbMin)...)

	// Every check passed. From here the upgrade changes state.
	if rep.Drain, err = u.drain(ctx); err != nil {
		return rep, err
	}
	lock, err := u.stop(ctx)
	if err != nil {
		return rep, err
	}
	if rep.Snapshot, err = snapshot(ctx, u.Layout, from, u.now()); err != nil {
		return rep, u.abort(ctx, lock, err)
	}
	if err := u.install(rel); err != nil {
		return rep, u.abort(ctx, lock, err)
	}
	if err := Place(bins, u.Layout.join(BinDir)); err != nil {
		return rep, u.abort(ctx, lock, err)
	}
	for name := range bins {
		rep.Pins = append(rep.Pins, Provisioned{Name: name, Path: u.Layout.Bin(name), Fetched: true})
	}
	if err := u.Layout.WritePrevious(from); err != nil {
		return rep, u.abort(ctx, lock, err)
	}
	if err := u.Layout.SetCurrent(version); err != nil {
		return rep, u.abort(ctx, lock, err)
	}
	rep.Changed = true
	if err := u.Layout.Record(version); err != nil {
		return rep, u.abort(ctx, lock, err)
	}
	if err := u.start(ctx, lock); err != nil {
		return rep, err
	}
	rep.Started = true
	return rep, nil
}

// fetch reads the release and verifies its signature, its trusted
// comment, every checksum and its metadata.
func (u *Upgrader) fetch(ctx context.Context, version string) (verified, error) {
	var rel verified
	get := func(asset string) ([]byte, error) {
		data, err := u.Source.get(ctx, version, asset)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, refuse(cli.CodeNotFound, "no_release", "release %s has no %s: %v", version, asset, err)
		}
		if err != nil {
			return nil, refuse(CodeReleaseUnavailable, "fetch_failed", "fetch %s of %s: %v", asset, version, err)
		}
		return data, nil
	}
	var err error
	if rel.sums, err = get(ChecksumsFile); err != nil {
		return rel, err
	}
	if rel.sig, err = get(SignatureFile); err != nil {
		return rel, err
	}
	if len(u.Keys) == 0 {
		return rel, refuse(CodeSignatureInvalid, "no_trusted_key", "this binary embeds no release key; build it with the operator's public key in internal/release/trusted_keys.pub")
	}
	trusted, err := Verify(u.Keys, rel.sums, rel.sig)
	if err != nil {
		return rel, refuse(CodeSignatureInvalid, "bad_signature", "%v", err)
	}
	signed, err := TrustedVersion(trusted)
	if err != nil || signed != version {
		return rel, refuse(CodeVersionRefused, "trusted_version_mismatch",
			"the signed trusted comment names %q, not %s; a validly signed release is being served as another version", signed, version)
	}
	sums, err := ParseChecksums(rel.sums)
	if err != nil {
		return rel, refuse(CodeChecksumMismatch, "bad_checksums", "%v", err)
	}
	if rel.metaRaw, err = get(MetadataFile); err != nil {
		return rel, err
	}
	if err := sums.Check(MetadataFile, rel.metaRaw); err != nil {
		return rel, refuse(CodeChecksumMismatch, "metadata_checksum", "%v", err)
	}
	goos, goarch, _ := strings.Cut(u.Platform, "/")
	asset := AssetName(goos, goarch)
	if rel.bin, err = get(asset); err != nil {
		return rel, err
	}
	if err := sums.Check(asset, rel.bin); err != nil {
		return rel, refuse(CodeChecksumMismatch, "binary_checksum", "%v", err)
	}
	if rel.meta, err = ParseMetadata(rel.metaRaw); err != nil {
		return rel, refuse(CodeChecksumMismatch, "bad_metadata", "%v", err)
	}
	if rel.meta.Version != version {
		return rel, refuse(CodeVersionRefused, "metadata_version_mismatch", "release.json names %s, not %s", rel.meta.Version, version)
	}
	return rel, nil
}

// checkRules applies the version rules of KTD26 step 1 and step 2.
func (u *Upgrader) checkRules(from string, m Metadata, dbMin int) error {
	st, err := u.Layout.ReadState()
	if err != nil {
		return err
	}
	if st.Highest != "" && CompareVersions(m.Version, st.Highest) < 0 {
		return refuse(CodeVersionRefused, "below_highest",
			"%s is below %s, the highest version this instance installed; a downgrade runs only through loop rollback", m.Version, st.Highest)
	}
	if m.MinFrom != "" && CompareVersions(from, m.MinFrom) < 0 {
		return refuse(CodeVersionRefused, "below_min_from", "%s upgrades only from %s or later, and this instance runs %s", m.Version, m.MinFrom, from)
	}
	if dbMin > m.Schema {
		return refuse(CodeVersionRefused, "schema_too_new", "the database needs schema %d, and %s has schema %d", dbMin, m.Version, m.Schema)
	}
	return nil
}

// install writes the release into versions/<v>/.
func (u *Upgrader) install(rel verified) error {
	dir := u.Layout.VersionDir(rel.meta.Version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for name, f := range map[string]struct {
		data []byte
		perm fs.FileMode
	}{
		BinaryName:    {rel.bin, 0o755},
		MetadataFile:  {rel.metaRaw, 0o600},
		ChecksumsFile: {rel.sums, 0o600},
		SignatureFile: {rel.sig, 0o600},
	} {
		if err := writeFileAtomic(filepath.Join(dir, name), f.data, f.perm); err != nil {
			return err
		}
	}
	return syncDir(dir)
}

// snapshot copies the database into snapshots/ while no server holds it
// (KTD5, KTD26 step 4). A missing database takes no snapshot.
func snapshot(ctx context.Context, l Layout, from string, now time.Time) (string, error) {
	db := l.DB()
	if _, err := os.Stat(db); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err := os.MkdirAll(l.Snapshots(), 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(l.Snapshots(), fmt.Sprintf("loop-%s-%s.db", now.UTC().Format("20060102T150405.000Z"), from))
	st, err := store.Open(ctx, db)
	if err != nil {
		return "", fmt.Errorf("release: open the database for a snapshot: %w", err)
	}
	if err := st.Snapshot(ctx, dst); err != nil {
		_ = st.Close()
		return "", err
	}
	return dst, st.Close()
}
