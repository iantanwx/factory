package gates

import (
	"encoding/json"
	"io"
	"io/fs"
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
// A Go package pattern `dir/...` reaches each Go file under dir, with the
// directories Go skips left out, as `go test ./...` does. The word make
// reaches the Makefile, and the name of a package manager reaches
// package.json, of which the walk reads the script lines alone: its keys
// name no file, and "scripts" would reach a directory of them. The walk can over-reach,
// through a path in a comment, and then it passes a row. It never fails
// one that a gate runs through a path it names.

// scriptMechanism is the rung of a check script. Its home in a project is
// a script that a gate runs.
const scriptMechanism = "A check script with an exit code"

// maxReadBytes bounds the text the walk reads of one file.
const maxReadBytes = 1 << 20

var pathWord = regexp.MustCompile(`[\w./-]+`)

// manifests are the files a runner's name reaches.
var manifests = []struct {
	word  *regexp.Regexp
	files []string
}{
	{regexp.MustCompile(`\bmake\b`), []string{"GNUmakefile", "makefile", "Makefile"}},
	{regexp.MustCompile(`\b(npm|pnpm|yarn|bun)\b`), []string{"package.json"}},
}

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
			if w == "./..." {
				queue = append(queue, goFiles(repo.Root, dir)...)
				continue
			}
			if !strings.ContainsFunc(w, isAlnum) {
				continue
			}
			w = strings.TrimLeft(w, "/")
			if base, ok := strings.CutSuffix(w, "/..."); ok {
				for _, p := range []string{base, filepath.Join(dir, base)} {
					queue = append(queue, goFiles(repo.Root, p)...)
				}
				continue
			}
			for _, p := range []string{w, filepath.Join(dir, w)} {
				queue = append(queue, inside(repo.Root, p)...)
			}
		}
		for _, m := range manifests {
			if m.word.MatchString(text) {
				for _, f := range m.files {
					queue = append(queue, inside(repo.Root, f)...)
				}
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
		text := head(filepath.Join(repo.Root, p))
		if filepath.Base(p) == "package.json" {
			text = packageScripts(text)
		}
		visit(filepath.Dir(p), text)
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

// packageScripts is the script lines of a package.json, one to a line, or
// "" when it holds none.
func packageScripts(text string) string {
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal([]byte(text), &pkg) != nil {
		return ""
	}
	lines := make([]string, 0, len(pkg.Scripts))
	for _, line := range pkg.Scripts {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
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

// goFiles is each Go file under the directory p of the checkout. It skips
// the directories that a Go package pattern skips: testdata, and each name
// that starts with . or _.
func goFiles(root, p string) []string {
	p = filepath.Clean(p)
	if !filepath.IsLocal(p) {
		return nil
	}
	var out []string
	_ = filepath.WalkDir(filepath.Join(root, p), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		name := d.Name()
		switch {
		case d.IsDir() && rel != p && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")):
			return filepath.SkipDir
		case d.Type().IsRegular() && strings.HasSuffix(name, ".go"):
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

func isAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}
