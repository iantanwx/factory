// Package release builds, signs, installs, upgrades, rolls back and
// restores a loop instance (U16, KTD26). Every step reports through a
// value the CLI prints as one JSON envelope. Nothing here prompts.
package release

import (
	"strconv"
	"strings"
)

// version is a parsed semantic version: vMAJOR.MINOR.PATCH[-PRE][+BUILD].
type version struct {
	nums [3]int
	pre  string
	ok   bool
}

func parseVersion(s string) version {
	rest, ok := strings.CutPrefix(s, "v")
	if !ok {
		return version{}
	}
	if i := strings.IndexByte(rest, '+'); i >= 0 {
		rest = rest[:i]
	}
	var v version
	core, pre, hasPre := strings.Cut(rest, "-")
	if hasPre {
		if pre == "" {
			return version{}
		}
		v.pre = pre
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}
	}
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return version{}
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return version{}
		}
		v.nums[i] = n
	}
	v.ok = true
	return v
}

// ValidVersion reports whether s is a release version: vMAJOR.MINOR.PATCH
// with an optional pre-release part.
func ValidVersion(s string) bool { return parseVersion(s).ok }

// CompareVersions orders two versions. A string that is not a version,
// such as "dev", sorts below every version.
func CompareVersions(a, b string) int {
	va, vb := parseVersion(a), parseVersion(b)
	switch {
	case !va.ok && !vb.ok:
		return strings.Compare(a, b)
	case !va.ok:
		return -1
	case !vb.ok:
		return 1
	}
	for i := range va.nums {
		if va.nums[i] != vb.nums[i] {
			if va.nums[i] < vb.nums[i] {
				return -1
			}
			return 1
		}
	}
	return comparePre(va.pre, vb.pre)
}

// comparePre orders pre-release parts by semver: no part sorts above any
// part, numeric identifiers sort numerically and below alphanumeric ones.
func comparePre(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == bs[i] {
			continue
		}
		na, ea := strconv.Atoi(as[i])
		nb, eb := strconv.Atoi(bs[i])
		switch {
		case ea == nil && eb == nil:
			if na < nb {
				return -1
			}
			return 1
		case ea == nil:
			return -1
		case eb == nil:
			return 1
		}
		return strings.Compare(as[i], bs[i])
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}
