// Package config loads the two configuration scopes (R35). Host config lives
// with the instance and names every project, the key file and each platform
// credential by its secret-store name. Project config and the secret manifest
// are committed in each repository and are read from the default branch only
// (KTD20). Every parser refuses an unknown field.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrInvalid matches every refusal of a configuration file or value.
var ErrInvalid = errors.New("config: invalid")

// InvalidError names the file and the field that a parser refused. It never
// holds the refused value, because a misplaced value can be a secret.
type InvalidError struct {
	Source string // the file or document, for example "host.yaml"
	Field  string // a dotted field path, empty for the whole document
	Reason string
}

func (e *InvalidError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("config: %s: %s", e.Source, e.Reason)
	}
	return fmt.Sprintf("config: %s: %s: %s", e.Source, e.Field, e.Reason)
}

// Is makes errors.Is(err, ErrInvalid) hold.
func (e *InvalidError) Is(target error) bool { return target == ErrInvalid }

// checker collects every refusal in one document, so one edit can fix all.
type checker struct {
	source string
	errs   []error
}

func (c *checker) fail(field, format string, args ...any) {
	c.errs = append(c.errs, &InvalidError{Source: c.source, Field: field, Reason: fmt.Sprintf(format, args...)})
}

func (c *checker) err() error { return errors.Join(c.errs...) }

// backticked matches the value excerpt that yaml.v3 puts in a type error.
var backticked = regexp.MustCompile("`[^`]*`")

// decodeStrict decodes exactly one YAML document into out and refuses an
// unknown field, a duplicate key and a second document. The error never
// holds a value from the document: yaml.v3 quotes up to ten characters of a
// mistyped value, and that excerpt is removed.
func decodeStrict(source string, data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return &InvalidError{Source: source, Reason: "the document is empty"}
		}
		return &InvalidError{Source: source, Reason: backticked.ReplaceAllString(err.Error(), "<value>")}
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		return &InvalidError{Source: source, Reason: backticked.ReplaceAllString(err.Error(), "<value>")}
	default:
		return &InvalidError{Source: source, Reason: "the file holds more than one YAML document"}
	}
}

// WriteFileAtomic replaces path with data at mode perm. It writes a temporary
// file in the same directory, syncs it, renames it over path and syncs the
// directory, so a crash leaves the old file or the new file and never a part.
// It refuses a path that is a symlink, so a planted link cannot redirect the
// write.
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) (err error) {
	if fi, lerr := os.Lstat(path); lerr == nil {
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("config: refuse to write %s: it is a symlink", path)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("config: refuse to write %s: it is not a regular file", path)
		}
	} else if !errors.Is(lerr, fs.ErrNotExist) {
		return fmt.Errorf("config: stat %s: %w", path, lerr)
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("config: create temporary file for %s: %w", path, err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(perm); err != nil {
		return fmt.Errorf("config: chmod %s: %w", tmp, err)
	}
	if _, err = f.Write(data); err != nil {
		return fmt.Errorf("config: write %s: %w", tmp, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("config: sync %s: %w", tmp, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("config: close %s: %w", tmp, err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("config: rename %s: %w", path, err)
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("config: open directory %s: %w", dir, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("config: sync directory %s: %w", dir, err)
	}
	return nil
}

var (
	// secretNameRe is an environment-variable-safe name. A manifest entry
	// becomes a .env line, so its name must be a valid variable name.
	secretNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	// credentialRefRe is the name of a platform credential in the store. It
	// is narrower than a secret name on purpose: upper case, digits and
	// underscores only, so a token, a PEM block or an op:// reference pasted
	// into host config fails it.
	credentialRefRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	identRe         = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
)

// ValidSecretName reports whether name can be a manifest entry and a .env
// variable.
func ValidSecretName(name string) bool { return secretNameRe.MatchString(name) }

// ValidCredentialRef reports whether name can name a platform credential in
// host config.
func ValidCredentialRef(name string) bool { return credentialRefRe.MatchString(name) }

func validIdent(s string) bool { return identRe.MatchString(s) }

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
