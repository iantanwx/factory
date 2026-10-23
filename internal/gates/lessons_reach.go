package gates

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A check script holds a rule only while a gate runs it. A row that names
// a script nothing runs claims rung 4 and holds rung 7: the file is there,
// and nothing fails when it would (lessons.ts reachable, alpha). So the
// file of a check script must be reached from the command of a gate of
// .loop/config.yaml, which holds the test gate too.
//
// A project's commands are in any language, so the walk reads names, not
// imports. A reached file reaches each path its text names that is a file
// or a directory of the checkout, from the root or from its own directory.
// A directory reaches the files directly in it, as `go run ./cmd/x` does.
// The word make reaches the Makefile. The walk can over-reach, through a
// path in a comment, and then it passes a row. It never fails one that a
// gate runs through a path it names.

// scriptMechanism is the rung of a check script. Its home in a project is
// a script that a gate runs.
const scriptMechanism = "A check script with an exit code"

// maxReadBytes bounds the text the walk reads of one file.
const maxReadBytes = 1 << 20

var (
	pathWord = regexp.MustCompile(`[\w./-]+`)
	makeWord = regexp.MustCompile(`\bmake\b`)
	makefile = []string{"GNUmakefile", "makefile", "Makefile"}
)

// reached is each file of the checkout, as a slash path from the root,
// that a gate of the project config reaches.
func reached(repo *Repo) map[string]bool {
	seen := map[string]bool{}
	if repo.Config == nil {
		return seen
	}
	var queue []string
	visit := func(dir, text string) {
		for _, w := range pathWord.FindAllString(text, -1) {
			if !strings.ContainsFunc(w, isAlnum) {
				continue
			}
			w = strings.TrimLeft(w, "/")
			for _, p := range []string{w, filepath.Join(dir, w)} {
				queue = append(queue, inside(repo.Root, p)...)
			}
		}
		if makeWord.MatchString(text) {
			for _, m := range makefile {
				queue = append(queue, inside(repo.Root, m)...)
			}
		}
	}
	for _, g := range repo.Config.Gates {
		visit(".", g.Run)
	}
	for len(queue) > 0 {
		p := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[p] {
			continue
		}
		seen[p] = true
		visit(filepath.Dir(p), head(filepath.Join(repo.Root, p)))
	}
	return seen
}

// head is the first maxReadBytes of a file, or "" when it cannot be read.
func head(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	text, _ := io.ReadAll(io.LimitReader(f, maxReadBytes))
	return string(text)
}

// inside resolves p from the root of the checkout. A file is itself, a
// directory is the files directly in it, and anything else is nothing.
func inside(root, p string) []string {
	p = filepath.Clean(p)
	if !filepath.IsLocal(p) {
		return nil
	}
	info, err := os.Stat(filepath.Join(root, p))
	switch {
	case err != nil:
		return nil
	case info.Mode().IsRegular():
		return []string{filepath.ToSlash(p)}
	case !info.IsDir():
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(root, p))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			out = append(out, filepath.ToSlash(filepath.Join(p, e.Name())))
		}
	}
	return out
}

func isAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}
