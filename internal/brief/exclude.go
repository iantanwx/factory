package brief

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/gitx"
)

// ConflictError is a tracked file at an injected path (KTD19). The run is
// refused with CONFIG_CONFLICT, which does not count.
type ConflictError struct {
	Paths []string
}

func (e *ConflictError) Error() string {
	return "brief: the repository tracks injected paths: " + strings.Join(e.Paths, ", ")
}

// CheckTracked returns a *ConflictError when git tracks a file at one of
// files, or any file under one of dirs. Paths are relative and use
// slashes.
func CheckTracked(ctx context.Context, checkout string, files, dirs []string) error {
	out, err := gitx.Run(ctx, checkout, "ls-files", "-z", "--full-name")
	if err != nil {
		return err
	}
	var hits []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		hit := slices.Contains(files, p)
		for _, d := range dirs {
			hit = hit || p == d || strings.HasPrefix(p, d+"/")
		}
		if hit {
			hits = append(hits, p)
		}
	}
	if len(hits) > 0 {
		return &ConflictError{Paths: hits}
	}
	return nil
}

// excludeMarker heads the block the server owns in info/exclude.
const excludeMarker = "# loop: injected run files (KTD19)"

// Exclude lists each file and directory in the repository's info/exclude,
// anchored at the root. A line already present is not written again. A
// linked worktree shares its common directory's exclude file.
func Exclude(ctx context.Context, checkout string, files, dirs []string) error {
	out, err := gitx.Run(ctx, checkout, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(checkout, path)
	}
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("brief: read %s: %w", path, err)
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(old), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var add []string
	want := make([]string, 0, len(files)+len(dirs))
	for _, f := range files {
		want = append(want, "/"+f)
	}
	for _, d := range dirs {
		want = append(want, "/"+d+"/")
	}
	for _, w := range want {
		if !have[w] {
			add = append(add, w)
		}
	}
	if len(add) == 0 {
		return nil
	}
	var b strings.Builder
	b.Write(old)
	if len(old) > 0 && !bytes.HasSuffix(old, []byte("\n")) {
		b.WriteByte('\n')
	}
	if !have[excludeMarker] {
		b.WriteString(excludeMarker + "\n")
	}
	for _, a := range add {
		b.WriteString(a + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("brief: write %s: %w", path, err)
	}
	return nil
}
