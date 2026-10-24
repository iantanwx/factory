package release

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/hexid"
	"github.com/iantanwx/loop/internal/pack"
)

// ThirdPartyAsset is the release asset that holds the release's
// third-party skill pins, byte for byte pack/third-party.json. It travels
// beside release.json and not in it, because an installed binary refuses
// a release.json with a field it does not know. checksums.txt names it, so
// the signature binds it.
const ThirdPartyAsset = "third-party.json"

// DistOptions describe one release build.
type DistOptions struct {
	Version    string
	MinFrom    string
	NoRollback bool
}

// WriteDist writes release.json, third-party.json and checksums.txt into
// dir, which holds the binary of every platform. CI runs it. It signs
// nothing.
func WriteDist(ctx context.Context, dir string, o DistOptions) error {
	if !ValidVersion(o.Version) {
		return fmt.Errorf("release: %q is not a release version such as v1.2.3", o.Version)
	}
	if o.MinFrom != "" && !ValidVersion(o.MinFrom) {
		return fmt.Errorf("release: the minimum from-version %q is not a release version", o.MinFrom)
	}
	meta, err := ThisRelease(ctx)
	if err != nil {
		return err
	}
	meta.Version, meta.MinFrom, meta.NoRollback = o.Version, o.MinFrom, o.NoRollback
	if err := writeFileAtomic(filepath.Join(dir, MetadataFile), meta.Encode(), 0o644); err != nil {
		return err
	}
	pins, err := fs.ReadFile(pack.Source(), pack.ThirdPartyFile)
	if err != nil {
		return fmt.Errorf("release: read the embedded %s: %w", pack.ThirdPartyFile, err)
	}
	if err := writeFileAtomic(filepath.Join(dir, ThirdPartyAsset), pins, 0o644); err != nil {
		return err
	}
	return WriteDistChecksums(dir)
}

// WriteDistChecksums writes checksums.txt over release.json,
// third-party.json and every platform's binary in dir.
func WriteDistChecksums(dir string) error {
	sums := Checksums{}
	for _, name := range []string{MetadataFile, ThirdPartyAsset} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		sums[name] = hexid.SHA256(data)
	}
	for _, p := range Platforms {
		goos, goarch, _ := strings.Cut(p, "/")
		name := AssetName(goos, goarch)
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("release: the release lacks the %s binary: %w", p, err)
		}
		sums[name] = hexid.SHA256(data)
	}
	return writeFileAtomic(filepath.Join(dir, ChecksumsFile), sums.Bytes(), 0o644)
}

// SignDist signs dir's checksum file with a trusted comment that names
// the release's version. The operator runs it offline (make release-sign).
func SignDist(dir string, key SecretKey) error {
	meta, err := ReadMetadata(filepath.Join(dir, MetadataFile))
	if err != nil {
		return err
	}
	sums, err := os.ReadFile(filepath.Join(dir, ChecksumsFile))
	if err != nil {
		return err
	}
	if _, err := VerifyAssets(dir, sums); err != nil {
		return err
	}
	sig, err := Sign(key, sums, TrustedComment(meta.Version))
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, SignatureFile), sig, 0o644)
}

// VerifyDist checks the signature of dir against keys, the trusted
// comment's version against release.json, and every asset's checksum. It
// returns the verified version.
func VerifyDist(dir string, keys []PublicKey) (string, error) {
	sums, err := os.ReadFile(filepath.Join(dir, ChecksumsFile))
	if err != nil {
		return "", err
	}
	sig, err := os.ReadFile(filepath.Join(dir, SignatureFile))
	if err != nil {
		return "", err
	}
	trusted, err := Verify(keys, sums, sig)
	if err != nil {
		return "", err
	}
	signed, err := TrustedVersion(trusted)
	if err != nil {
		return "", err
	}
	meta, err := VerifyAssets(dir, sums)
	if err != nil {
		return "", err
	}
	if meta.Version != signed {
		return "", fmt.Errorf("release: the trusted comment names %s and release.json names %s", signed, meta.Version)
	}
	return signed, nil
}

// VerifyAssets checks every asset the checksum file names, and that it
// names release.json, third-party.json and every platform's binary.
func VerifyAssets(dir string, sumsRaw []byte) (Metadata, error) {
	sums, err := ParseChecksums(sumsRaw)
	if err != nil {
		return Metadata{}, err
	}
	want := []string{MetadataFile, ThirdPartyAsset}
	for _, p := range Platforms {
		goos, goarch, _ := strings.Cut(p, "/")
		want = append(want, AssetName(goos, goarch))
	}
	for _, name := range want {
		if _, ok := sums[name]; !ok {
			return Metadata{}, fmt.Errorf("release: %s names no %s", ChecksumsFile, name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(sums)) {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return Metadata{}, err
		}
		if err := sums.Check(name, data); err != nil {
			return Metadata{}, err
		}
	}
	return ReadMetadata(filepath.Join(dir, MetadataFile))
}
