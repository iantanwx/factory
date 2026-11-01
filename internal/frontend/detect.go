// Package frontend finds the signs of a user interface in a tree: a web
// framework that a package.json depends on, a .tsx or .vue file, and an ios
// or android directory. It proposes the ui block that covers them.
package frontend

import (
	"cmp"
	"encoding/json"
	"path"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/config"
)

// SignalKind names what shows a frontend.
type SignalKind string

// The three kinds of signal.
const (
	KindPackage SignalKind = "package" // a package.json depends on a framework
	KindFile    SignalKind = "file"    // a .tsx or .vue file
	KindMobile  SignalKind = "mobile"  // an ios or android directory
)

// Signal is one sign of a frontend in a tree.
type Signal struct {
	Kind   SignalKind `json:"kind"`
	Path   string     `json:"path"`   // the tracked path that shows it
	Detail string     `json:"detail"` // the framework, the extension or the directory name
}

// Detection is what Detect found in one tree.
type Detection struct {
	Signals  []Signal         // sorted by Path, then Detail
	Proposed *config.UIConfig // nil when Signals is empty
}

// Tree is the tracked files of one tree.
type Tree interface {
	Paths() []string // each tracked file path, slash form
	Read(path string) ([]byte, error)
}

var (
	frameworks = []string{"next", "react", "svelte", "vue"}
	extensions = []string{".tsx", ".vue"}
	mobileDirs = []string{"android", "ios"}
)

// Detect finds each signal in t and proposes a block that covers them. A
// package.json that does not parse or cannot be read shows nothing.
func Detect(t Tree) Detection {
	var sigs []Signal
	mobile := map[string]bool{}
	for _, p := range t.Paths() {
		dirs := strings.Split(p, "/")
		dirs = dirs[:len(dirs)-1]
		for i, d := range dirs {
			if dir := strings.Join(dirs[:i+1], "/"); slices.Contains(mobileDirs, d) && !mobile[dir] {
				mobile[dir] = true
				sigs = append(sigs, Signal{Kind: KindMobile, Path: dir, Detail: d})
			}
		}
		switch base := path.Base(p); {
		case base == "package.json":
			for _, f := range dependsOn(t, p) {
				sigs = append(sigs, Signal{Kind: KindPackage, Path: p, Detail: f})
			}
		case slices.Contains(extensions, path.Ext(base)):
			sigs = append(sigs, Signal{Kind: KindFile, Path: p, Detail: path.Ext(base)})
		}
	}
	if len(sigs) == 0 {
		return Detection{}
	}
	slices.SortFunc(sigs, func(a, b Signal) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), strings.Compare(a.Detail, b.Detail))
	})
	return Detection{Signals: sigs, Proposed: propose(sigs)}
}

// dependsOn returns each framework that the package.json at p names under
// dependencies, devDependencies or peerDependencies, in name order.
func dependsOn(t Tree, p string) []string {
	b, err := t.Read(p)
	if err != nil {
		return nil
	}
	var pkg struct {
		Deps map[string]any `json:"dependencies"`
		Dev  map[string]any `json:"devDependencies"`
		Peer map[string]any `json:"peerDependencies"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return nil
	}
	var out []string
	for _, f := range frameworks {
		_, d := pkg.Deps[f]
		_, v := pkg.Dev[f]
		_, r := pkg.Peer[f]
		if d || v || r {
			out = append(out, f)
		}
	}
	return out
}

// propose covers each package directory and each mobile directory with
// <dir>/**, drops a directory that another one holds, and adds **/*.tsx or
// **/*.vue for a file that no directory glob covers. Kind is native when a
// mobile directory shows.
func propose(sigs []Signal) *config.UIConfig {
	ui := &config.UIConfig{Kind: config.UIWeb}
	var dirs []string
	for _, s := range sigs {
		switch s.Kind {
		case KindMobile:
			ui.Kind = config.UINative
			dirs = append(dirs, s.Path)
		case KindPackage:
			dirs = append(dirs, strings.TrimSuffix(path.Dir(s.Path), "."))
		}
	}
	slices.Sort(dirs)
	var kept []string
	for _, d := range slices.Compact(dirs) {
		if !slices.ContainsFunc(kept, func(k string) bool { return k == "" || strings.HasPrefix(d, k+"/") }) {
			kept = append(kept, d)
			ui.Paths = append(ui.Paths, dirGlob(d))
		}
	}
	for _, ext := range extensions {
		if slices.ContainsFunc(sigs, func(s Signal) bool {
			return s.Kind == KindFile && s.Detail == ext &&
				!slices.ContainsFunc(ui.Paths, func(g config.Glob) bool { return g.Match(s.Path) })
		}) {
			ui.Paths = append(ui.Paths, mustGlob("**/*"+ext))
		}
	}
	return ui
}

// globMeta escapes each character that path.Match reads as a pattern, so a
// directory glob matches the directory's own name.
var globMeta = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`)

// dirGlob is <dir>/**, or ** for the root and for a directory name that no
// glob can spell.
func dirGlob(dir string) config.Glob {
	if dir != "" {
		if g, err := config.ParseGlob(globMeta.Replace(dir) + "/**"); err == nil {
			return g
		}
	}
	return mustGlob("**")
}

func mustGlob(s string) config.Glob {
	g, err := config.ParseGlob(s)
	if err != nil {
		panic(err)
	}
	return g
}
