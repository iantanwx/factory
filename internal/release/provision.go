package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/hexid"
)

// Pin is one pinned bd or dolt binary for one platform (R40, KTD26). The
// URL names the place the archive is read from. The vendor may move it,
// so the hashes, not the URL, decide whether the binary is the pinned one.
type Pin struct {
	Name     string `json:"name"` // "bd" or "dolt"
	Version  string `json:"version"`
	Platform string `json:"platform"` // "linux/amd64"
	URL      string `json:"url"`
	// ArchiveSHA256 is the sha256 of the downloaded archive.
	ArchiveSHA256 string `json:"archive_sha256"`
	// Member is the binary's path inside the archive.
	Member string `json:"member"`
	// BinarySHA256 is the sha256 of the extracted binary.
	BinarySHA256 string `json:"binary_sha256"`
}

func beadsURL(goos, goarch string) string {
	return fmt.Sprintf("https://github.com/gastownhall/beads/releases/download/v%[1]s/beads_%[1]s_%[2]s_%[3]s.tar.gz", beadsx.PinnedBD, goos, goarch)
}

func doltURL(goos, goarch string) string {
	return fmt.Sprintf("https://github.com/dolthub/dolt/releases/download/v%s/dolt-%s-%s.tar.gz", beadsx.PinnedDolt, goos, goarch)
}

// pinTable holds the pinned binaries of every release platform. An empty
// hash is a pin nobody measured yet: provisioning refuses that platform
// until both hashes are recorded. The bd archive hashes come from beads'
// own checksums.txt for 1.3.1. The linux/amd64 binary hashes are the
// binaries docs/evidence/beads-contract.md records. The arm64 hashes were
// measured from the vendors' archives on 2026-10-22.
var pinTable = []Pin{
	{Name: "bd", Version: beadsx.PinnedBD, Platform: "linux/amd64", URL: beadsURL("linux", "amd64"), Member: "bd",
		ArchiveSHA256: "3219443a9734b89b93fb16ee8d65844759fa1b3cd3cf139c606b7353cfb0715c",
		BinarySHA256:  "a8f48d771b9e11eccfced4aed72923b59eda746f7a9b425e7ca1af7a51251653"},
	{Name: "bd", Version: beadsx.PinnedBD, Platform: "linux/arm64", URL: beadsURL("linux", "arm64"), Member: "bd",
		ArchiveSHA256: "c3b32c71a6c0cd6358a12c28272b17e6818db991da192f87df52339c222e423a",
		BinarySHA256:  "213518569a3648c1a520bced38733a08c66b693c53aa8801f49c644de0a44330"},
	{Name: "bd", Version: beadsx.PinnedBD, Platform: "darwin/arm64", URL: beadsURL("darwin", "arm64"), Member: "bd",
		ArchiveSHA256: "508ba807996e2b9c992c538a6f405f2fc7d2e16cf8b4e5b0d1bf9fe860367007",
		BinarySHA256:  "c06a59fda589d1fc00e394027ee90992d5083aea487c301c9e9eaf592e22e8b4"},
	{Name: "dolt", Version: beadsx.PinnedDolt, Platform: "linux/amd64", URL: doltURL("linux", "amd64"), Member: "dolt-linux-amd64/bin/dolt",
		ArchiveSHA256: "1f7ad8c2622995789420a3fb0f2d16b4aa7430000a825dd91d5938f36480cbf6",
		BinarySHA256:  "72352f1d882472a7a8d6124e9c4dc12f80e2c2359ea95f1424c032e9250e35e6"},
	{Name: "dolt", Version: beadsx.PinnedDolt, Platform: "linux/arm64", URL: doltURL("linux", "arm64"), Member: "dolt-linux-arm64/bin/dolt",
		ArchiveSHA256: "a49a566d7c1ee9fdff553644855737f9d87047557c1ce1951aa7e7053277f4af",
		BinarySHA256:  "54fc2bb6cf02fef77ccc4ba61298ae41c5f31eed58df34d885330a394e8da028"},
	{Name: "dolt", Version: beadsx.PinnedDolt, Platform: "darwin/arm64", URL: doltURL("darwin", "arm64"), Member: "dolt-darwin-arm64/bin/dolt",
		ArchiveSHA256: "c6737dc2c5806e2eeef4839ad76c28167c861f878af3071df1242a6589d81267",
		BinarySHA256:  "2c775333f7085821e95136d2066fa3d63e0926087b4b6878f68d0a4d78343690"},
}

// PinsFor returns the pins of one platform from pins: bd, then dolt. It
// refuses a platform whose pin lacks a hash.
func PinsFor(pins []Pin, platform string) ([]Pin, error) {
	var out []Pin
	for _, name := range []string{"bd", "dolt"} {
		i := -1
		for j, p := range pins {
			if p.Name == name && p.Platform == platform {
				i = j
			}
		}
		if i < 0 {
			return nil, fmt.Errorf("release: no %s pin for %s", name, platform)
		}
		p := pins[i]
		if len(p.ArchiveSHA256) != 64 || len(p.BinarySHA256) != 64 {
			return nil, fmt.Errorf("release: the %s %s pin for %s holds no archive and binary sha256; record both before this platform can provision", p.Name, p.Version, platform)
		}
		out = append(out, p)
	}
	return out, nil
}

// maxArchive bounds one downloaded archive or binary.
const maxArchive = 512 << 20

// Provisioner fetches pinned binaries.
type Provisioner struct {
	// HTTP is the client. http.DefaultClient when nil.
	HTTP *http.Client
	// URL rewrites a pin's URL. A test points it at a local server.
	URL func(Pin) string
}

// Provisioned is one binary provisioning placed or found in place.
type Provisioned struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path"`
	Fetched bool   `json:"fetched"`
}

// Fetch downloads and verifies the archive of each pin, and returns each
// extracted binary keyed by name. It writes nothing.
func (p Provisioner) Fetch(ctx context.Context, pins []Pin) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, pin := range pins {
		u := pin.URL
		if p.URL != nil {
			u = p.URL(pin)
		}
		archive, err := httpGet(ctx, p.HTTP, u, maxArchive)
		if err != nil {
			return nil, fmt.Errorf("release: fetch %s %s: %w", pin.Name, pin.Version, err)
		}
		if got := hexid.SHA256(archive); got != pin.ArchiveSHA256 {
			return nil, fmt.Errorf("%w: the %s %s archive has sha256 %s, and the binary pins %s", ErrPinMismatch, pin.Name, pin.Version, got, pin.ArchiveSHA256)
		}
		bin, err := extractMember(archive, pin.Member)
		if err != nil {
			return nil, fmt.Errorf("release: %s %s: %w", pin.Name, pin.Version, err)
		}
		if got := hexid.SHA256(bin); got != pin.BinarySHA256 {
			return nil, fmt.Errorf("%w: the %s %s binary has sha256 %s, and the binary pins %s", ErrPinMismatch, pin.Name, pin.Version, got, pin.BinarySHA256)
		}
		out[pin.Name] = bin
	}
	return out, nil
}

// ErrPinMismatch is a fetched or installed bd or dolt whose hash differs
// from the pinned one.
var ErrPinMismatch = errors.New("release: the binary differs from its pin")

// Provision places each pinned binary in binDir. A binary already in
// place with the pinned hash is kept. Every fetch is verified before any
// file is written.
func (p Provisioner) Provision(ctx context.Context, pins []Pin, binDir string) ([]Provisioned, error) {
	var need []Pin
	var out []Provisioned
	for _, pin := range pins {
		dst := filepath.Join(binDir, pin.Name)
		if ok, _ := fileHasHash(dst, pin.BinarySHA256); ok {
			out = append(out, Provisioned{Name: pin.Name, Version: pin.Version, Path: dst})
			continue
		}
		need = append(need, pin)
	}
	if len(need) == 0 {
		return out, nil
	}
	bins, err := p.Fetch(ctx, need)
	if err != nil {
		return nil, err
	}
	if err := Place(bins, binDir); err != nil {
		return nil, err
	}
	for _, pin := range need {
		out = append(out, Provisioned{Name: pin.Name, Version: pin.Version, Path: filepath.Join(binDir, pin.Name), Fetched: true})
	}
	return out, nil
}

// Place writes each binary into binDir at mode 0755 through a rename.
func Place(bins map[string][]byte, binDir string) error {
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		return err
	}
	for name, data := range bins {
		if err := writeFileAtomic(filepath.Join(binDir, name), data, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// CheckPins reports each pinned binary in binDir that is missing or whose
// hash differs from its pin.
func CheckPins(pins []Pin, binDir string) []string {
	var drift []string
	for _, pin := range pins {
		dst := filepath.Join(binDir, pin.Name)
		ok, err := fileHasHash(dst, pin.BinarySHA256)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			drift = append(drift, fmt.Sprintf("%s is missing", dst))
		case err != nil:
			drift = append(drift, fmt.Sprintf("%s cannot be read: %v", dst, err))
		case !ok:
			drift = append(drift, fmt.Sprintf("%s is not the pinned %s %s", dst, pin.Name, pin.Version))
		}
	}
	return drift
}

func fileHasHash(path, want string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return hexid.SHA256(data) == want, nil
}

// extractMember reads one regular file from a gzip tar archive.
func extractMember(archive []byte, member string) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("the archive is not gzip: %w", err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the archive holds no %s", member)
		}
		if err != nil {
			return nil, fmt.Errorf("read the archive: %w", err)
		}
		if path.Clean(strings.TrimPrefix(h.Name, "./")) != member {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("%s in the archive is not a regular file", member)
		}
		return io.ReadAll(io.LimitReader(tr, maxArchive))
	}
}

// httpGet reads one URL, or one local file when u is an absolute path.
func httpGet(ctx context.Context, client *http.Client, u string, limit int64) ([]byte, error) {
	return httpGetHeader(ctx, client, u, nil, limit)
}

// httpGetHeader is httpGet with header on the request.
func httpGetHeader(ctx context.Context, client *http.Client, u string, header http.Header, limit int64) ([]byte, error) {
	if filepath.IsAbs(u) {
		f, err := os.Open(u)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		return readLimited(f, limit)
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s answered 404", fs.ErrNotExist, u)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", u, resp.Status)
	}
	return readLimited(resp.Body, limit)
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("the download exceeds %d bytes", limit)
	}
	return data, nil
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory, a sync and a rename.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Chmod(perm); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Pins returns the pin table this binary embeds.
func Pins() []Pin { return append([]Pin(nil), pinTable...) }
