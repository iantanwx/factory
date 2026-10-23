package brief

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/secrets"
)

// EnvRecord names each .env the server wrote into the checkout, one path
// per line, so RemoveEnv finds every one after the manifest changed.
const EnvRecord = cli.EnvRecordFile

// EnvFiles maps each directory, relative to the checkout root with
// slashes, to the secrets its .env holds. The root is "".
type EnvFiles map[string]map[string]string

// GroupEnv puts each value of an entry of m into the .env of the entry's
// directory (parity 9.3). A name with no value is left out.
func GroupEnv(m *config.Manifest, values map[string]string) EnvFiles {
	out := EnvFiles{}
	for _, e := range m.Secrets {
		v, ok := values[e.Name]
		if !ok {
			continue
		}
		if out[e.Dir] == nil {
			out[e.Dir] = map[string]string{}
		}
		out[e.Dir][e.Name] = v
	}
	return out
}

// Paths returns the path of each file, relative to the checkout root, in
// order.
func (f EnvFiles) Paths() []string {
	var out []string
	for _, dir := range slices.Sorted(maps.Keys(f)) {
		out = append(out, envPath(dir))
	}
	return out
}

func envPath(dir string) string {
	if dir == "" {
		return EnvFile
	}
	return dir + "/" + EnvFile
}

// WriteEnv writes each file of files at mode 0600 (R34, KTD17), and each
// file holds only its own names. It refuses a tracked path, lists each
// path in .git/info/exclude, and removes every .env an earlier write left
// first. It makes a directory the checkout lacks. Every write goes through
// an os.Root, so a symlink cannot carry a value out of the checkout.
func WriteEnv(ctx context.Context, checkout string, files EnvFiles) error {
	paths := files.Paths()
	injected := append(slices.Clone(paths), EnvRecord)
	if err := CheckTracked(ctx, checkout, injected, nil); err != nil {
		return err
	}
	if err := Exclude(ctx, checkout, injected, nil); err != nil {
		return err
	}
	if err := RemoveEnv(checkout); err != nil {
		return err
	}
	root, err := os.OpenRoot(checkout)
	if err != nil {
		return fmt.Errorf("brief: open the checkout: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(cli.LoopDir, 0o700); err != nil {
		return fmt.Errorf("brief: make %s: %w", cli.LoopDir, err)
	}
	// The record goes first, so a write that stops halfway leaves each file
	// named for RemoveEnv.
	if err := writeIn(root, EnvRecord, []byte(strings.Join(paths, "\n")+"\n")); err != nil {
		return err
	}
	for _, dir := range slices.Sorted(maps.Keys(files)) {
		if dir != "" {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("brief: make %s: %w", dir, err)
			}
		}
		data, err := secrets.FormatEnv(files[dir])
		if err != nil {
			return err
		}
		if err := writeIn(root, envPath(dir), data); err != nil {
			return err
		}
	}
	return nil
}

// writeIn writes name under root at mode 0600 through a temporary file and
// a rename. It refuses a name that is a symlink or no regular file.
func writeIn(root *os.Root, name string, data []byte) (err error) {
	if fi, lerr := root.Lstat(name); lerr == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("brief: refuse to write %s: it is not a regular file", name)
	} else if lerr != nil && !errors.Is(lerr, fs.ErrNotExist) {
		return fmt.Errorf("brief: stat %s: %w", name, lerr)
	}
	tmp := path.Join(path.Dir(name), "."+path.Base(name)+".tmp-"+rand.Text())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("brief: create a temporary file for %s: %w", name, err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = root.Remove(tmp)
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return fmt.Errorf("brief: chmod %s: %w", tmp, err)
	}
	if _, err = f.Write(data); err != nil {
		return fmt.Errorf("brief: write %s: %w", tmp, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("brief: sync %s: %w", tmp, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("brief: close %s: %w", tmp, err)
	}
	if err = root.Rename(tmp, name); err != nil {
		return fmt.Errorf("brief: write %s: %w", name, err)
	}
	return nil
}

// RemoveEnv deletes the checkout's root .env and each .env its record
// names, then the record. Every settlement calls it, a crashed run's kept
// checkout included. A missing file is no error. A symlink is removed
// itself, and a path that leaves the checkout is refused and deletes
// nothing.
func RemoveEnv(checkout string) error {
	root, err := os.OpenRoot(checkout)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("brief: open the checkout: %w", err)
	}
	defer func() { _ = root.Close() }()
	names, err := envNames(root)
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range append(names, EnvRecord) {
		errs = append(errs, removeIn(root, n))
	}
	return errors.Join(errs...)
}

// PresentEnv returns each .env that RemoveEnv would remove from the
// checkout and that is on disk, relative to the checkout with slashes. A
// file it cannot stat is returned too, so the caller can name it. A
// missing checkout holds none.
func PresentEnv(checkout string) ([]string, error) {
	root, err := os.OpenRoot(checkout)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("brief: open the checkout: %w", err)
	}
	defer func() { _ = root.Close() }()
	names, err := envNames(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if _, err := root.Lstat(n); !errors.Is(err, fs.ErrNotExist) {
			out = append(out, n)
		}
	}
	return out, nil
}

// envNames returns the root .env and each .env the checkout's record
// names.
func envNames(root *os.Root) ([]string, error) {
	names := []string{EnvFile}
	data, err := root.ReadFile(EnvRecord)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("brief: read %s: %w", EnvRecord, err)
	}
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" && l != EnvFile && path.Base(l) == EnvFile {
			names = append(names, l)
		}
	}
	return names, nil
}

func removeIn(root *os.Root, name string) error {
	fi, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("brief: stat %s: %w", name, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("brief: refuse to remove %s: it is a directory", name)
	}
	if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("brief: remove %s: %w", name, err)
	}
	return nil
}
