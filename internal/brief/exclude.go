package brief

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/agenthost"
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

// ReservedError is a tracked directory in the loop- namespace that the
// pack lacks. The run is refused with CONFIG_CONFLICT, which does not
// count.
type ReservedError struct {
	Dirs []string
}

func (e *ReservedError) Error() string {
	return "brief: the repository tracks " + SkillPrefix + " skill directories the pack lacks: " + strings.Join(e.Dirs, ", ")
}

// skillRoots are the skill directories of every harness. One committed
// directory serves each harness of a project.
var skillRoots = []string{SkillRoot(agenthost.Claude), SkillRoot(agenthost.Codex)}

// CheckReserved returns a *ReservedError when git tracks a file under a
// directory of either skill root whose name starts with loop- and that p
// lacks.
func CheckReserved(ctx context.Context, checkout string, p Pack) error {
	skills, err := p.Skills()
	if err != nil {
		return err
	}
	tracked, err := trackedSkillDirs(ctx, checkout)
	if err != nil {
		return err
	}
	var dirs []string
	for _, d := range tracked {
		name := path.Base(d)
		if strings.HasPrefix(name, SkillPrefix) && !slices.Contains(skills, name) {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) > 0 {
		return &ReservedError{Dirs: dirs}
	}
	return nil
}

// Prune removes each untracked <root>/loop-* directory of the harness that
// p lacks. A directory that holds a tracked file stays.
func Prune(ctx context.Context, checkout string, h agenthost.Harness, p Pack) error {
	skills, err := p.Skills()
	if err != nil {
		return err
	}
	tracked, err := trackedSkillDirs(ctx, checkout)
	if err != nil {
		return err
	}
	root := SkillRoot(h)
	entries, err := os.ReadDir(filepath.Join(checkout, filepath.FromSlash(root)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("brief: read %s: %w", root, err)
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, SkillPrefix) || slices.Contains(skills, name) || slices.Contains(tracked, root+"/"+name) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(checkout, filepath.FromSlash(root), name)); err != nil {
			return fmt.Errorf("brief: clear %s/%s: %w", root, name, err)
		}
	}
	return nil
}

// trackedSkillDirs lists each <root>/<name> of both skill roots that holds
// a tracked file, sorted, each once.
func trackedSkillDirs(ctx context.Context, checkout string) ([]string, error) {
	out, err := gitx.Run(ctx, checkout, append([]string{"ls-files", "-z", "--full-name", "--"}, skillRoots...)...)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, f := range strings.Split(string(out), "\x00") {
		for _, root := range skillRoots {
			rest, ok := strings.CutPrefix(f, root+"/")
			if !ok {
				continue
			}
			name, _, _ := strings.Cut(rest, "/")
			if d := root + "/" + name; !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
	}
	slices.Sort(dirs)
	return dirs, nil
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
