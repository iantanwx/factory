package config

import (
	"errors"
	"path"
	"slices"
	"strings"
)

// globStar is the segment that matches zero or more whole segments.
const globStar = "**"

// Glob is one checked path glob of a ui block. Each segment is exactly **,
// or a path.Match pattern that holds no **. Only ParseGlob makes one, so
// no matcher checks a glob again.
type Glob struct{ segs []string }

// ParseGlob checks s against the glob rule. The error names the rule and
// never the glob.
func ParseGlob(s string) (Glob, error) {
	switch {
	case strings.HasPrefix(s, "/"):
		return Glob{}, errors.New("a glob must not start with /")
	case strings.Contains(s, "{"):
		return Glob{}, errors.New("a glob must not hold {, since no brace expands")
	case HasControl(s):
		return Glob{}, errors.New("a glob must not hold a control character")
	}
	segs := strings.Split(s, "/")
	for _, seg := range segs {
		switch {
		case seg == "":
			return Glob{}, errors.New("a glob must hold no empty segment")
		case seg == "." || seg == "..":
			return Glob{}, errors.New("a glob must hold no . or .. segment")
		case seg == globStar:
		case strings.Contains(seg, globStar):
			return Glob{}, errors.New("** must be a whole segment")
		default:
			if _, err := path.Match(seg, ""); err != nil {
				return Glob{}, errors.New("a segment is not a valid path.Match pattern")
			}
		}
	}
	return Glob{segs: segs}, nil
}

func (g Glob) String() string { return strings.Join(g.segs, "/") }

// Meets reports whether the glob g meets one "May change" entry. A literal
// path matches by g. A glob or a directory entry meets g when g reaches
// its literal directory: the entry cut at the last / before its first
// glob character.
func Meets(g Glob, entry string) bool {
	entry = strings.TrimPrefix(entry, "./")
	i := strings.IndexAny(entry, "*?[")
	if i < 0 && !strings.HasSuffix(entry, "/") {
		return g.Match(entry)
	}
	if i < 0 {
		i = len(entry)
	}
	return g.Reaches(entry[:max(strings.LastIndex(entry[:i], "/"), 0)])
}

// EntriesMeet reports whether two "May change" entries can name one file,
// by the prefix rule of footprint in scripts/sdlc/ticket-files.ts (alpha):
// they meet when the prefix of one starts with the prefix of the other.
// The rule over-reports, and that is the safe direction: a false meeting
// delays one ticket by one tick.
func EntriesMeet(a, b string) bool {
	pa, pb := entryPrefix(a), entryPrefix(b)
	return strings.HasPrefix(pa, pb) || strings.HasPrefix(pb, pa)
}

// entryPrefix is the literal text that one entry covers. A glob stops at
// its first glob character. A file stops at its stem, the name before its
// first dot, so pkg/x.go covers pkg/x_test.go. A directory keeps its path,
// and a dotfile its whole name.
func entryPrefix(entry string) string {
	entry = strings.TrimPrefix(entry, "./")
	if i := strings.IndexAny(entry, "*?["); i >= 0 {
		return entry[:i]
	}
	entry = strings.TrimSuffix(entry, "/")
	dir := strings.LastIndex(entry, "/") + 1
	if dot := strings.IndexByte(entry[dir:], '.'); dot > 0 {
		return entry[:dir+dot]
	}
	return entry
}

// Match reports whether g matches the literal slash path p.
func (g Glob) Match(p string) bool {
	return slices.Contains(g.walk(strings.Split(p, "/")), len(g.segs))
}

// Reaches reports whether g can match some path under dir. The empty dir,
// the repository root, is reached by every glob.
func (g Glob) Reaches(dir string) bool {
	var segs []string
	if dir = strings.TrimSuffix(dir, "/"); dir != "" {
		segs = strings.Split(dir, "/")
	}
	// A position short of the end can still match at least one more
	// segment: a ** segment, or a pattern that path.Match checked.
	for _, i := range g.walk(segs) {
		if i < len(g.segs) {
			return true
		}
	}
	return false
}

// walk returns each position in g that the segments can reach. A ** at
// position i stays at i as it takes a segment, and reaches i+1 with none.
func (g Glob) walk(segs []string) []int {
	at := g.close([]int{0})
	for _, s := range segs {
		var next []int
		for _, i := range at {
			switch {
			case i == len(g.segs):
			case g.segs[i] == globStar:
				next = append(next, i)
			default:
				if ok, _ := path.Match(g.segs[i], s); ok {
					next = append(next, i+1)
				}
			}
		}
		if at = g.close(next); len(at) == 0 {
			break
		}
	}
	return at
}

// close adds each position that a ** at a held position reaches with no
// segment, and drops duplicates.
func (g Glob) close(at []int) []int {
	var out []int
	for _, i := range at {
		for ; ; i++ {
			if !slices.Contains(out, i) {
				out = append(out, i)
			}
			if i == len(g.segs) || g.segs[i] != globStar {
				break
			}
		}
	}
	return out
}
