package release

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	_ "modernc.org/sqlite" // the driver the store uses

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/hexid"
	"github.com/iantanwx/loop/internal/store"
)

// Release asset names (KTD26). A release holds one binary for each
// platform, the metadata file, the checksum file and its signature.
const (
	ChecksumsFile = "checksums.txt"
	SignatureFile = ChecksumsFile + ".minisig"
	MetadataFile  = "release.json"
	// BinaryName is the binary's name inside versions/<v>/.
	BinaryName = "loop"
)

// Platforms are the release targets.
var Platforms = []string{"linux/amd64", "linux/arm64", "darwin/arm64"}

// AssetName is the release asset of one platform's binary.
func AssetName(goos, goarch string) string { return "loop-" + goos + "-" + goarch }

// Metadata describes one release. The checksum file covers it, so the
// signature binds it.
type Metadata struct {
	Version string `json:"version"`
	// Schema is the highest migration the release embeds.
	Schema int `json:"schema"`
	// MinReaderSchema is the highest minimum reader schema its migrations
	// record (KTD5). A binary whose Schema is below it cannot read a
	// database this release migrated.
	MinReaderSchema int `json:"min_reader_schema"`
	// MinFrom is the lowest installed version this release upgrades from.
	MinFrom string `json:"min_from,omitempty"`
	// NoRollback marks the release by hand. A beads pin bump or a
	// migration that an older binary cannot read marks it too.
	NoRollback bool `json:"no_rollback,omitempty"`
	// Pins are the pinned bd and dolt of every platform.
	Pins []Pin `json:"pins"`
}

// ThisRelease is the metadata of the running binary.
func ThisRelease(ctx context.Context) (Metadata, error) {
	schema, minReader, err := embeddedSchema(ctx)
	if err != nil {
		return Metadata{}, err
	}
	return Metadata{Version: cli.Version, Schema: schema, MinReaderSchema: minReader, Pins: slices.Clone(pinTable)}, nil
}

// embeddedSchema opens a new database in a temporary directory with this
// binary's migrations and reads its schema and minimum reader schema.
func embeddedSchema(ctx context.Context) (schema, minReader int, err error) {
	dir, err := os.MkdirTemp("", "loop-schema-")
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	st, err := store.Open(ctx, filepath.Join(dir, "schema.db"))
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = st.Close() }()
	minReader, err = DatabaseMinReader(ctx, st.Path())
	return st.SchemaVersion(), minReader, err
}

// DatabaseMinReader reads the minimum reader schema of a database file
// without migrating it. A missing file reads 0.
func DatabaseMinReader(ctx context.Context, path string) (int, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	// A query-only read-write connection, not mode=ro: a read-only
	// connection to a WAL database leaves -wal and -shm files behind.
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()
	var n int
	err = db.QueryRowContext(ctx, `SELECT min_reader_schema FROM schema_meta WHERE id = 1`).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("release: read the minimum reader schema of %s: %w", path, err)
	}
	return n, nil
}

// RollbackBlockers lists why a database that release from wrote cannot go
// back to release to. An empty list allows the rollback (KTD26).
func RollbackBlockers(from, to Metadata, dbMinReader int) []string {
	var why []string
	if from.NoRollback {
		why = append(why, from.Version+" is marked no-rollback")
	}
	if pinsDiffer(from.Pins, to.Pins) {
		why = append(why, fmt.Sprintf("%s pins other bd or dolt versions than %s", from.Version, to.Version))
	}
	if from.MinReaderSchema > to.Schema {
		why = append(why, fmt.Sprintf("%s needs schema %d to read its database, and %s has schema %d",
			from.Version, from.MinReaderSchema, to.Version, to.Schema))
	}
	if dbMinReader > to.Schema {
		why = append(why, fmt.Sprintf("the database needs schema %d, and %s has schema %d", dbMinReader, to.Version, to.Schema))
	}
	return why
}

// pinsDiffer compares the pinned bd and dolt versions.
func pinsDiffer(a, b []Pin) bool {
	versions := func(ps []Pin) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Name+"@"+p.Version)
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return !slices.Equal(versions(a), versions(b))
}

// ReadMetadata reads a release.json file.
func ReadMetadata(path string) (Metadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Metadata{}, err
	}
	return ParseMetadata(data)
}

// ParseMetadata parses release.json.
func ParseMetadata(data []byte) (Metadata, error) {
	var m Metadata
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Metadata{}, fmt.Errorf("release: %s: %w", MetadataFile, err)
	}
	if err := checkVersionName(m.Version); err != nil {
		return Metadata{}, fmt.Errorf("release: %s: %w", MetadataFile, err)
	}
	return m, nil
}

// Encode is the release.json form.
func (m Metadata) Encode() []byte {
	data, _ := json.MarshalIndent(m, "", "  ")
	return append(data, '\n')
}

// Checksums maps an asset name to its sha256 in hex.
type Checksums map[string]string

// ParseChecksums reads "<sha256>  <name>" lines, the sha256sum format.
func ParseChecksums(data []byte) (Checksums, error) {
	c := Checksums{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, "  ")
		name = strings.TrimPrefix(name, "*")
		if !ok || len(sum) != 64 || name == "" || strings.ContainsAny(name, "/\\") {
			return nil, fmt.Errorf("release: %s holds a malformed line", ChecksumsFile)
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return nil, fmt.Errorf("release: %s holds a malformed sha256", ChecksumsFile)
		}
		if _, dup := c[name]; dup {
			return nil, fmt.Errorf("release: %s names %s twice", ChecksumsFile, name)
		}
		c[name] = strings.ToLower(sum)
	}
	return c, sc.Err()
}

// Bytes is the checksum file, sorted by name.
func (c Checksums) Bytes() []byte {
	var b bytes.Buffer
	for _, name := range slices.Sorted(maps.Keys(c)) {
		fmt.Fprintf(&b, "%s  %s\n", c[name], name)
	}
	return b.Bytes()
}

// Check reports whether data has the checksum the file names for name.
func (c Checksums) Check(name string, data []byte) error {
	want, ok := c[name]
	if !ok {
		return fmt.Errorf("release: %s names no %s", ChecksumsFile, name)
	}
	if got := hexid.SHA256(data); got != want {
		return fmt.Errorf("release: %s has sha256 %s, and %s names %s", name, got, ChecksumsFile, want)
	}
	return nil
}
