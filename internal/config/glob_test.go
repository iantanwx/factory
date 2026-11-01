package config

import (
	"math/rand/v2"
	"strings"
	"testing"
)

func mustGlob(t *testing.T, s string) Glob {
	t.Helper()
	g, err := ParseGlob(s)
	if err != nil {
		t.Fatalf("ParseGlob(%q): %v", s, err)
	}
	return g
}

// TestC4GlobMatchesLiteralPaths proves C4: a ** segment matches zero or
// more whole segments, and every other segment matches one segment by
// path.Match.
func TestC4GlobMatchesLiteralPaths(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"web/**", "web/src/App.tsx", true},
		{"web/**", "web", true},
		{"web/**", "webapp/x.ts", false},
		{"**/*.tsx", "App.tsx", true},
		{"**/*.tsx", "a/b/c/App.tsx", true},
		{"**/*.tsx", "a/b/App.ts", false},
		{"apps/*/src/**", "apps/web/src/main.ts", true},
		{"apps/*/src/**", "apps/web/lib/main.ts", false},
	}
	for _, c := range cases {
		if got := mustGlob(t, c.glob).Match(c.path); got != c.want {
			t.Errorf("%q.Match(%q) = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}

// TestC4AGlobOfALiteralPathMatchesThatPath proves the property of C4: for
// 1000 generated literal paths p, ParseGlob(p).Match(p) holds.
func TestC4AGlobOfALiteralPathMatchesThatPath(t *testing.T) {
	const alphabet = "abcxyzABC019-_."
	r := rand.New(rand.NewPCG(1, 2))
	for range 1000 {
		segs := make([]string, 1+r.IntN(5))
		for i := range segs {
			for segs[i] == "" || segs[i] == "." || segs[i] == ".." {
				b := make([]byte, 1+r.IntN(8))
				for j := range b {
					b[j] = alphabet[r.IntN(len(alphabet))]
				}
				segs[i] = string(b)
			}
		}
		p := strings.Join(segs, "/")
		if g := mustGlob(t, p); !g.Match(p) || g.String() != p {
			t.Fatalf("ParseGlob(%q): Match = %v, String = %q; want true and the path", p, g.Match(p), g.String())
		}
	}
}

// TestC5GlobReachesADirectory proves C5: Reaches answers whether the glob
// can match a path under the directory, at the root, one level and three
// levels.
func TestC5GlobReachesADirectory(t *testing.T) {
	cases := []struct {
		glob, dir string
		want      bool
	}{
		{"web/**", "web/src/", true},
		{"**/*.tsx", "internal/", true},
		{"apps/*/src/**", "apps/", true},
		{"web/*.tsx", "web/components/", false},
		{"web/*", "web/x/", false},
		{"app/**", "lib/", false},
		{"web/**", "web/src/components/ui/", true},
		{"**/*.tsx", "a/b/c/", true},
		{"apps/*/src/**", "apps/web/lib/", false},
		{"apps/*/src/*.ts", "apps/web/src/", true},
		{"web/**", "", true},
		{"**/*.tsx", "", true},
		{"apps/*/src/**", "", true},
		{"app/**", "", true},
		{"README.md", "", true},
	}
	for _, c := range cases {
		if got := mustGlob(t, c.glob).Reaches(c.dir); got != c.want {
			t.Errorf("%q.Reaches(%q) = %v, want %v", c.glob, c.dir, got, c.want)
		}
	}
}
