package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing/fstest"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/store"
)

// ReleaseRepo is the loop repository, owner/name, which holds the releases.
const ReleaseRepo = "slothware-labs/factorio"

// Homepage is the loop repository.
const Homepage = "https://github.com/" + ReleaseRepo

// DefaultReleaseBase is where `loop upgrade` reads releases: the GitHub
// releases of the loop repository. The repository is private, so a
// Source with this base reads them through the GitHub REST API with the
// host's gh login. The release's signature, not this location, decides
// whether a release is trusted.
const DefaultReleaseBase = Homepage + "/releases/download"

// ReleaseBaseEnv overrides DefaultReleaseBase with another URL or an
// absolute directory that holds <version>/<asset>. It names a location
// only: a release from anywhere must still verify. A Source never sends
// the gh login to it.
const ReleaseBaseEnv = "LOOP_RELEASE_BASE"

// githubAPIHost is the one host that receives the gh login.
const githubAPIHost = "api.github.com"

// ErrNoGHLogin is a read of the GitHub releases on a host with no gh
// login. It is never fs.ErrNotExist: the release may well exist.
var ErrNoGHLogin = errors.New("release: no gh login to read the private loop repository " + ReleaseRepo)

// GHToken is the host's GitHub login: the token `gh auth token` prints.
// gh honours GH_TOKEN itself.
func GHToken(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	token := strings.TrimSpace(string(out))
	if err == nil && token != "" {
		return token, nil
	}
	detail := "it printed no token"
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
		detail = string(bytes.TrimSpace(ee.Stderr))
	} else if err != nil {
		detail = err.Error()
	}
	return "", fmt.Errorf("%w: `gh auth token` failed (%s); run `gh auth login`, or set GH_TOKEN", ErrNoGHLogin, detail)
}

// Source reads release assets from Base/<version>/<asset>, or from the
// GitHub releases of ReleaseRepo when Base is DefaultReleaseBase.
type Source struct {
	// Base is a URL, or an absolute directory. Empty is DefaultReleaseBase.
	Base string
	// HTTP is the client. http.DefaultClient when nil.
	HTTP *http.Client
	// Token reads the gh login for the GitHub releases. GHToken when nil.
	Token func(context.Context) (string, error)
}

func (s Source) base() string {
	if s.Base == "" {
		return DefaultReleaseBase
	}
	return s.Base
}

// github reports whether s reads the GitHub releases of ReleaseRepo.
func (s Source) github() bool {
	return strings.TrimSuffix(s.base(), "/") == DefaultReleaseBase
}

// assets returns a reader of the assets of version.
func (s Source) assets(ctx context.Context, version string) (func(asset string) ([]byte, error), error) {
	if !s.github() {
		return func(asset string) ([]byte, error) { return s.get(ctx, version, asset) }, nil
	}
	rel, token, err := s.githubRelease(ctx, "tags/"+url.PathEscape(version))
	if err != nil {
		return nil, err
	}
	urls := map[string]string{}
	for _, a := range rel.Assets {
		urls[a.Name] = a.URL
	}
	return func(asset string) ([]byte, error) {
		u, ok := urls[asset]
		if !ok {
			return nil, fmt.Errorf("%w: the GitHub release %s lists no %s", fs.ErrNotExist, version, asset)
		}
		return s.githubGet(ctx, token, u, "application/octet-stream")
	}, nil
}

func (s Source) get(ctx context.Context, version, asset string) ([]byte, error) {
	base := s.base()
	if filepath.IsAbs(base) {
		return httpGet(ctx, nil, filepath.Join(base, version, asset), maxArchive)
	}
	return httpGet(ctx, s.HTTP, strings.TrimSuffix(base, "/")+"/"+version+"/"+asset, maxArchive)
}

// githubRelease is a GitHub release as the REST API serves it.
type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"assets"`
}

// githubRelease reads /repos/<ReleaseRepo>/releases/<path> with the gh
// login, and returns the release and the login.
func (s Source) githubRelease(ctx context.Context, path string) (githubRelease, string, error) {
	var rel githubRelease
	read := GHToken
	if s.Token != nil {
		read = s.Token
	}
	token, err := read(ctx)
	if err != nil {
		return rel, "", err
	}
	u := "https://" + githubAPIHost + "/repos/" + ReleaseRepo + "/releases/" + path
	data, err := s.githubGet(ctx, token, u, "application/vnd.github+json")
	if err != nil {
		return rel, "", err
	}
	if err := json.Unmarshal(data, &rel); err != nil {
		return rel, "", fmt.Errorf("release: %s: %w", u, err)
	}
	return rel, token, nil
}

// githubGet reads u, a GitHub API URL, with the gh login. Go's client
// drops the login on the redirect of an asset read to its storage host.
func (s Source) githubGet(ctx context.Context, token, u, accept string) ([]byte, error) {
	if p, err := url.Parse(u); err != nil || p.Scheme != "https" || p.Host != githubAPIHost {
		return nil, fmt.Errorf("release: %q is not a GitHub API URL, so it gets no gh login", u)
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	h.Set("Accept", accept)
	h.Set("X-GitHub-Api-Version", "2022-11-28")
	return httpGetHeader(ctx, s.HTTP, u, h, maxArchive)
}

// LatestVersion reads the version of the newest published release: the
// tag of the latest GitHub release, or the version its release.json
// names. The version only names what to fetch: Run verifies the release
// as it would any named version.
func (s Source) LatestVersion(ctx context.Context) (string, error) {
	base := s.base()
	var data []byte
	var err error
	switch {
	case s.github():
		rel, _, err := s.githubRelease(ctx, "latest")
		if err != nil {
			return "", err
		}
		if !ValidVersion(rel.TagName) {
			return "", fmt.Errorf("release: the latest release names %q", rel.TagName)
		}
		return rel.TagName, nil
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
	// Fetcher fetches the target release's third-party skills.
	Fetcher SkillFetcher
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
	// Skills names each third-party skill the upgrade fetched.
	Skills []string `json:"skills,omitempty"`
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
	// pinsRaw is the release's third-party.json, and skills its pins.
	pinsRaw []byte
	skills  []pack.ThirdParty
}

// Run upgrades to version. Every check runs before the first state
// change, so a refused release leaves the instance and the service as
// they were. Then it drains, stops the service, takes a snapshot, installs
// the release and its third-party skills into versions/<v>/, repoints
// current and starts the service.
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
	var skills map[string]fs.FS
	if len(CheckSkills(rel.skills, u.Layout.Skills(version))) > 0 {
		if skills, err = u.Fetcher.Fetch(ctx, rel.skills); errors.Is(err, ErrPinMismatch) {
			return rep, refuse(CodeChecksumMismatch, "skill_mismatch", "%v", err)
		} else if err != nil {
			return rep, refuse(CodeReleaseUnavailable, "skill_fetch_failed", "%v", err)
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
	if skills != nil {
		if err := PlaceSkills(skills, u.Layout.Skills(version)); err != nil {
			return rep, u.abort(ctx, lock, err)
		}
		for _, p := range rel.skills {
			rep.Skills = append(rep.Skills, p.Name)
		}
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
// comment, every checksum, its metadata and its third-party pins.
func (u *Upgrader) fetch(ctx context.Context, version string) (verified, error) {
	var rel verified
	get, err := u.Source.verifiable(ctx, version)
	if err != nil {
		return rel, err
	}
	var sums Checksums
	if rel.sums, rel.sig, sums, err = verifiedChecksums(u.Keys, version, get); err != nil {
		return rel, err
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
	if rel.pinsRaw, err = get(ThirdPartyAsset); err != nil {
		return rel, err
	}
	if err := sums.Check(ThirdPartyAsset, rel.pinsRaw); err != nil {
		return rel, refuse(CodeChecksumMismatch, "third_party_checksum", "%v", err)
	}
	if rel.skills, err = pack.ReadThirdParty(fstest.MapFS{pack.ThirdPartyFile: {Data: rel.pinsRaw}}); err != nil {
		return rel, refuse(CodeChecksumMismatch, "bad_third_party", "%v", err)
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
		BinaryName:      {rel.bin, 0o755},
		MetadataFile:    {rel.metaRaw, 0o600},
		ChecksumsFile:   {rel.sums, 0o600},
		SignatureFile:   {rel.sig, 0o600},
		ThirdPartyAsset: {rel.pinsRaw, 0o600},
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

// verifiable returns a reader of the assets of version that refuses each
// failed read with its rule.
func (s Source) verifiable(ctx context.Context, version string) (func(asset string) ([]byte, error), error) {
	refuseFetch := func(asset string, err error) error {
		switch {
		case errors.Is(err, ErrNoGHLogin):
			return refuse(CodeReleaseUnavailable, "no_gh_login", "fetch %s: %v", version, err)
		case errors.Is(err, fs.ErrNotExist):
			return refuse(cli.CodeNotFound, "no_release", "release %s has no %s: %v", version, asset, err)
		}
		return refuse(CodeReleaseUnavailable, "fetch_failed", "fetch %s of %s: %v", asset, version, err)
	}
	read, err := s.assets(ctx, version)
	if err != nil {
		return nil, refuseFetch("release", err)
	}
	return func(asset string) ([]byte, error) {
		data, err := read(asset)
		if err != nil {
			return nil, refuseFetch(asset, err)
		}
		return data, nil
	}, nil
}

// verifiedChecksums reads the checksum file of version and its signature,
// and verifies the signature with keys and its trusted comment. Every
// asset of the release is then checked against the returned sums.
func verifiedChecksums(keys []PublicKey, version string, get func(string) ([]byte, error)) (raw, sig []byte, sums Checksums, err error) {
	if raw, err = get(ChecksumsFile); err != nil {
		return nil, nil, nil, err
	}
	if sig, err = get(SignatureFile); err != nil {
		return nil, nil, nil, err
	}
	if len(keys) == 0 {
		return nil, nil, nil, refuse(CodeSignatureInvalid, "no_trusted_key", "this binary embeds no release key; build it with the operator's public key in internal/release/trusted_keys.pub")
	}
	trusted, err := Verify(keys, raw, sig)
	if err != nil {
		return nil, nil, nil, refuse(CodeSignatureInvalid, "bad_signature", "%v", err)
	}
	signed, err := TrustedVersion(trusted)
	if err != nil || signed != version {
		return nil, nil, nil, refuse(CodeVersionRefused, "trusted_version_mismatch",
			"the signed trusted comment names %q, not %s; a validly signed release is being served as another version", signed, version)
	}
	if sums, err = ParseChecksums(raw); err != nil {
		return nil, nil, nil, refuse(CodeChecksumMismatch, "bad_checksums", "%v", err)
	}
	return raw, sig, sums, nil
}

// The orca lane binaries inside versions/<v>/ (ADR 0008).
const (
	LaneContractFile   = "orca-contract.test"
	LaneAcceptanceFile = "orca-acceptance.test"
)

// FetchLane puts the orca lane binaries of version for platform into dir,
// each verified as Upgrader.Run verifies a release: the signature, its
// trusted comment, then each checksum. It writes nothing unless both
// verify. A checksum file that names neither binary is a release that
// predates the lane: shipped is false, and the error nil.
func FetchLane(ctx context.Context, src Source, keys []PublicKey, version, platform, dir string) (shipped bool, err error) {
	get, err := src.verifiable(ctx, version)
	if err != nil {
		return false, err
	}
	_, _, sums, err := verifiedChecksums(keys, version, get)
	if err != nil {
		return false, err
	}
	goos, goarch, _ := strings.Cut(platform, "/")
	assets := map[string]string{
		LaneContractFile:   OrcaContractAssetName(goos, goarch),
		LaneAcceptanceFile: OrcaAcceptanceAssetName(goos, goarch),
	}
	_, contract := sums[assets[LaneContractFile]]
	_, acceptance := sums[assets[LaneAcceptanceFile]]
	if !contract && !acceptance {
		return false, nil
	}
	bins := map[string][]byte{}
	for name, asset := range assets {
		data, err := get(asset)
		if err != nil {
			return true, err
		}
		if err := sums.Check(asset, data); err != nil {
			return true, refuse(CodeChecksumMismatch, "lane_checksum", "%v", err)
		}
		bins[name] = data
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return true, err
	}
	for name, data := range bins {
		if err := writeFileAtomic(filepath.Join(dir, name), data, 0o755); err != nil {
			return true, err
		}
	}
	return true, nil
}
