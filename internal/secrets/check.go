package secrets

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/config"
)

// ErrMissingSecret matches a MissingSecretsError.
var ErrMissingSecret = errors.New("secrets: a manifest secret has no stored value")

// MissingSecretsError names each manifest secret with no stored value (R53).
// The scheduler refuses the run with MISSING_SECRET and puts one inbox
// item. The refusal never counts toward the failure limit.
type MissingSecretsError struct {
	Project string
	Names   []string
}

func (e *MissingSecretsError) Error() string {
	return fmt.Sprintf("secrets: project %s has no stored value for %s", e.Project, strings.Join(e.Names, ", "))
}

// Is makes errors.Is(err, ErrMissingSecret) hold.
func (e *MissingSecretsError) Is(target error) bool { return target == ErrMissingSecret }

// Missing returns the manifest names with no stored value for project, in
// manifest order. It decrypts nothing.
func (s *Store) Missing(ctx context.Context, project string, m *config.Manifest) ([]string, error) {
	stored, err := s.Names(ctx, project)
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(stored))
	for _, n := range stored {
		have[n] = true
	}
	var missing []string
	for _, e := range m.Secrets {
		if !have[e.Name] {
			missing = append(missing, e.Name)
		}
	}
	return missing, nil
}

// Values decrypts every manifest secret for project, for the run's .env. It
// returns a MissingSecretsError when any is unset, and then no value.
func (s *Store) Values(ctx context.Context, project string, m *config.Manifest) (map[string]string, error) {
	missing, err := s.Missing(ctx, project, m)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, &MissingSecretsError{Project: project, Names: missing}
	}
	out := make(map[string]string, len(m.Secrets))
	for _, e := range m.Secrets {
		v, err := s.Get(ctx, project, e.Name)
		if errors.Is(err, ErrSecretNotFound) {
			// Removed between Missing and Get.
			return nil, &MissingSecretsError{Project: project, Names: []string{e.Name}}
		}
		if err != nil {
			return nil, err
		}
		out[e.Name] = string(v)
	}
	return out, nil
}

// WriteEnvFile writes values to path at mode 0600, sorted by name, through a
// temporary file and a rename. It refuses a path that is a symlink, so an
// agent cannot point .env at another file. The brief lists the file in
// .git/info/exclude.
//
// Quoting: a value with no single quote, CR or LF is written in single
// quotes, which bash, dotenv and python-dotenv all read literally, $ and
// backslash included. Any other value is written in double quotes with the
// escapes \\ \" \n \r, which dotenv and python-dotenv read back. A value in
// double quotes that holds $ may be expanded by a reader that interpolates,
// such as bash source. That case needs a quote and a newline in one value.
func WriteEnvFile(path string, values map[string]string) error {
	names := slices.Sorted(maps.Keys(values))
	var b strings.Builder
	for _, n := range names {
		if !config.ValidSecretName(n) {
			return fmt.Errorf("secrets: %q is not an environment variable name", n)
		}
		v := values[n]
		if strings.ContainsRune(v, 0) {
			return fmt.Errorf("secrets: the value of %s holds a NUL byte, which .env cannot carry", n)
		}
		b.WriteString(n)
		b.WriteByte('=')
		b.WriteString(quoteEnv(v))
		b.WriteByte('\n')
	}
	if err := config.WriteFileAtomic(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("secrets: write .env: %w", err)
	}
	return nil
}

func quoteEnv(v string) string {
	if !strings.ContainsAny(v, "'\r\n") {
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`)
	return `"` + r.Replace(v) + `"`
}

// RemoveEnvFile deletes the .env file at path. It is not an error when none
// exists. A symlink is removed itself, and its target is never touched.
func RemoveEnvFile(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("secrets: stat .env: %w", err)
	}
	if fi.IsDir() {
		return fmt.Errorf("secrets: refuse to remove %s: it is a directory", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("secrets: remove .env: %w", err)
	}
	return nil
}

// ParseEnv reads a file that WriteEnvFile wrote. It accepts exactly that
// format, so loop doctor can read back a .env it finds. Errors name a line
// number and never a value.
func ParseEnv(data []byte) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		name, raw, ok := strings.Cut(line, "=")
		if !ok || !config.ValidSecretName(name) {
			return nil, fmt.Errorf("secrets: .env line %d has no NAME=", i+1)
		}
		v, err := unquoteEnv(raw)
		if err != nil {
			return nil, fmt.Errorf("secrets: .env line %d: %w", i+1, err)
		}
		out[name] = v
	}
	return out, nil
}

func unquoteEnv(raw string) (string, error) {
	switch {
	case len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'':
		return raw[1 : len(raw)-1], nil
	case len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"':
		var b strings.Builder
		body := raw[1 : len(raw)-1]
		for i := 0; i < len(body); i++ {
			c := body[i]
			if c != '\\' {
				b.WriteByte(c)
				continue
			}
			i++
			if i == len(body) {
				return "", errors.New("the value ends in a lone backslash")
			}
			switch body[i] {
			case '\\', '"':
				b.WriteByte(body[i])
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			default:
				return "", errors.New("the value holds an unknown escape")
			}
		}
		return b.String(), nil
	default:
		return "", errors.New("the value is not quoted")
	}
}
