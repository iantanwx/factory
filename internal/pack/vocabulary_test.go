package pack_test

import (
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/config"
	packsrc "github.com/iantanwx/loop/pack"
)

// retiredHits names each file, line and retired word of fsys. A word
// matches whole, in any case.
func retiredHits(t *testing.T, fsys fs.FS, words []string) []string {
	t.Helper()
	var hits []string
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, w := range words {
				if regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(w) + `\b`).MatchString(line) {
					hits = append(hits, fmt.Sprintf("%s:%d: %s", path, i+1, w))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

// C9: no file of the embedded pack holds a retired word.
func TestC9PackHoldsNoRetiredWord(t *testing.T) {
	if hits := retiredHits(t, packsrc.Files, config.RetiredWords()); len(hits) > 0 {
		t.Fatalf("the pack holds a retired word:\n%s", strings.Join(hits, "\n"))
	}
}

// The walk finds a retired word whole, in any case, and names its file and
// line. It ignores the word inside a longer word.
func TestRetiredHitsNameFileLineAndWord(t *testing.T) {
	fsys := fstest.MapFS{"runbooks/a.md": {Data: []byte("# A\nAsk the Old-Stage run.\nThe old-stager is fine.\n")}}
	got := retiredHits(t, fsys, []string{"old-stage"})
	if len(got) != 1 || got[0] != "runbooks/a.md:2: old-stage" {
		t.Fatalf("hits = %q, want one at runbooks/a.md:2", got)
	}
}
