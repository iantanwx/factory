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
	"strings"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/pack"
)

// maxSkillTree bounds the extracted files of one third-party skill.
const maxSkillTree = 32 << 20

// SkillFetcher fetches pinned third-party skills (ADR 0007, decision 7).
type SkillFetcher struct {
	// HTTP is the client. http.DefaultClient when nil.
	HTTP *http.Client
	// URL rewrites a pin's archive address. A test points it at a local server
	// or file. The tree hash, not the address, decides.
	URL func(pack.ThirdParty) string
}

// skillURL is the GitHub tarball of the pinned commit.
func skillURL(p pack.ThirdParty) string {
	return "https://codeload.github.com/" + p.Repo + "/tar.gz/" + p.Commit
}

// Fetch reads and verifies each pin, and returns one tree for each, keyed by
// Name. It writes nothing. A tree that fails its hash, or whose SKILL.md does
// not name the pin, is ErrPinMismatch. Any refusal returns no tree.
func (f SkillFetcher) Fetch(ctx context.Context, pins []pack.ThirdParty) (map[string]fs.FS, error) {
	out := map[string]fs.FS{}
	for _, pin := range pins {
		u := skillURL(pin)
		if f.URL != nil {
			u = f.URL(pin)
		}
		archive, err := httpGet(ctx, f.HTTP, u, maxArchive)
		if err != nil {
			return nil, fmt.Errorf("release: fetch skill %s at %s: %w", pin.Name, pin.Commit, err)
		}
		tree, err := extractSkill(archive, pin.Path)
		if err != nil {
			return nil, fmt.Errorf("release: skill %s at %s: %w", pin.Name, pin.Commit, err)
		}
		got, err := pack.TreeHash(tree)
		if err != nil {
			return nil, fmt.Errorf("release: skill %s at %s: %w", pin.Name, pin.Commit, err)
		}
		if got != pin.SHA256 {
			return nil, fmt.Errorf("%w: the skill %s at %s has tree hash %s, and the pin holds %s", ErrPinMismatch, pin.Name, pin.Commit, got, pin.SHA256)
		}
		if !namesSkill(tree["SKILL.md"], pin.Name) {
			return nil, fmt.Errorf("%w: the front matter of the skill %s's SKILL.md holds no line name: %s", ErrPinMismatch, pin.Name, pin.Name)
		}
		out[pin.Name] = tree
	}
	return out, nil
}

// extractSkill reads the regular files under <top>/<dir>/ of a GitHub
// tarball, where <top> is the archive's one top directory. It refuses a
// member under dir that is not a regular file or a directory, a member
// path that leaves the archive, and files over maxSkillTree bytes.
func extractSkill(archive []byte, dir string) (fstest.MapFS, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("the archive is not gzip: %w", err)
	}
	tr := tar.NewReader(zr)
	tree := fstest.MapFS{}
	prefix := ""
	var size int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil && !errors.Is(err, tar.ErrInsecurePath) {
			return nil, fmt.Errorf("read the archive: %w", err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name := strings.TrimSuffix(h.Name, "/")
		if !fs.ValidPath(name) {
			return nil, fmt.Errorf("the member %s leaves the archive", h.Name)
		}
		if prefix == "" {
			top, _, _ := strings.Cut(name, "/")
			prefix = top + "/"
			if dir != "" {
				prefix += dir + "/"
			}
		}
		rel, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return nil, fmt.Errorf("the member %s is not a regular file or a directory", name)
		}
		if size += h.Size; size > maxSkillTree {
			return nil, fmt.Errorf("the files under %s exceed 32 MiB", dir)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		mode := fs.FileMode(0o644)
		if h.Mode&0o111 != 0 {
			mode = 0o755
		}
		tree[rel] = &fstest.MapFile{Data: data, Mode: mode}
	}
	if len(tree) == 0 {
		return nil, fmt.Errorf("the archive holds no files under %q", dir)
	}
	return tree, nil
}

// namesSkill reports whether a SKILL.md opens with a front matter that
// holds the line "name: <name>", the one line the pack writer renames.
func namesSkill(f *fstest.MapFile, name string) bool {
	if f == nil {
		return false
	}
	for i, line := range strings.Split(string(f.Data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case i == 0 && line != "---":
			return false
		case i > 0 && line == "---":
			return false
		case line == "name: "+name:
			return true
		}
	}
	return false
}
