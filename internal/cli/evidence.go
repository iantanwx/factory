package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// MaxEvidenceBytes bounds the evidence the CLI reads for one call. It sits
// above the 20 MiB a QA report may hold, so the server names the rule a
// report over that bound breaks.
const MaxEvidenceBytes = 24 << 20

// MaxRequestBytes bounds one call on the socket: the evidence and the
// --file file in base64, and room for the rest.
const MaxRequestBytes = (MaxEvidenceBytes+MaxFileBytes)*4/3 + 1<<20

// ReadEvidence reads each regular file under dir, relative to cwd, by its
// slash path from dir, up to MaxEvidenceBytes in all.
func ReadEvidence(dir, cwd string) (map[string][]byte, error) {
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	files := map[string][]byte{}
	total := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is no regular file", p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if total += len(data); total > MaxEvidenceBytes {
			return fmt.Errorf("the evidence under %s is larger than %d bytes", dir, MaxEvidenceBytes)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read --evidence: %w", err)
	}
	return files, nil
}

// EncodeEvidence is the ExtraEvidence value of files.
func EncodeEvidence(files map[string][]byte) string {
	raw, err := json.Marshal(files)
	if err != nil {
		panic(err) // bytes by string keys; Marshal cannot fail
	}
	return string(raw)
}

// EvidenceFiles returns the evidence files a call sent by path, or none.
// It refuses a path that is not a clean path inside the directory.
func EvidenceFiles(inv Invocation) (map[string][]byte, error) {
	raw, ok := inv.Extra[ExtraEvidence]
	if !ok {
		return nil, nil
	}
	var files map[string][]byte
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		return nil, fmt.Errorf("the evidence did not arrive intact: %w", err)
	}
	for p := range files {
		if !fs.ValidPath(p) || p == "." || strings.ContainsAny(p, "\\\x00") || slices.Contains(strings.Split(p, "/"), ".git") {
			return nil, fmt.Errorf("the evidence path %q is not a clean path inside the directory", p)
		}
	}
	return files, nil
}

// evidenceDigest is the hash of each path and its bytes.
func evidenceDigest(files map[string][]byte) string {
	h := sha256.New()
	for _, p := range slices.Sorted(maps.Keys(files)) {
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", p, len(files[p]))
		h.Write(files[p])
	}
	return hex.EncodeToString(h.Sum(nil))
}
