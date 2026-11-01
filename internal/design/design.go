// Package design reads the design record of one epic,
// docs/design/<epic>/record.md: the PRD it maps, the prototype commit it
// pins, and each screen with its stories and its states. The design gate
// and `loop design capture` both read a record with Parse, so they read
// one format. It does no I/O.
package design

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// RecordFile is the name of the record in its epic's directory.
const RecordFile = "record.md"

const (
	DefaultPixelPercent = 0.5 // the operator's default, room-36e9bc8c
	MaxPixelPercent     = 5.0 // the upper end of the allowed range, inclusive
)

// Dir is the directory of an epic's record and its pictures.
func Dir(epic string) string { return "docs/design/" + epic + "/" }

// Record is the parsed record of one epic.
type Record struct {
	Epic    string // the id on the **Epic:** line
	PRD     string // the repository path on the **PRD:** line
	Pin     Pin
	Screens []Screen
}

// Pin is the prototype commit that the record shows. A record whose
// **Prototype:** line names no commit on the epic's prototype branch holds
// the zero Pin.
type Pin struct {
	Branch string
	Commit string
}

// Screen is one `### S<n>. <name>` heading and what it holds.
type Screen struct {
	ID          string // S<n>
	Name        string
	Line        int
	Stories     []string // the labels on its **Stories:** line
	StoriesLine int      // 0 when the screen holds no **Stories:** line
	States      []State
}

// State is one state of one screen. A parsed State always holds a picture
// and a target. A state line that lacks either is a Problem, not a State.
type State struct {
	ID      string // S<n>.<m>
	Name    string
	Line    int
	Picture string // a .png file in the record's directory
	Target  string // the string the capture command receives
	Pixel   *Pixel // nil when the state holds no pixel mark
}

// Pixel marks a state for a pixel comparison. Percent is the share of the
// pixels that may differ, from 0 to MaxPixelPercent inclusive.
type Pixel struct {
	Percent float64
	Default bool // the mark names no number, so Percent is DefaultPixelPercent
}

// Problem is one fault of a record, at its line, under a rule of the
// design gate.
type Problem struct {
	Line int
	Rule string
	Msg  string
}

var (
	fieldLine  = regexp.MustCompile(`^\*\*(Epic|PRD|Prototype|Stories):\*\*[ \t]*(.*?)\s*$`)
	pinValue   = regexp.MustCompile("^`([^`]+)` at `([0-9a-f]{40})`$")
	screenHead = regexp.MustCompile(`^###\s+(S[0-9]+)\.\s+(.+?)\s*$`)
	listItem   = regexp.MustCompile(`^[-*+]\s`)
	stateItem  = regexp.MustCompile(`^[-*+]\s+\*\*(S[0-9]+)\.([0-9]+)\s+(.+?)\.\*\*\s*(.*?)\s*$`)
	picture    = regexp.MustCompile("(?i)\\bpicture\\s+`([^`]*)`")
	target     = regexp.MustCompile("(?i)\\btarget\\s+`([^`]*)`")
	codeSpan   = regexp.MustCompile("`[^`]*`")
	pixelMark  = regexp.MustCompile(`(?i)(?:^|,)\s*pixel\b\s*(.*?)\s*$`)
	percent    = regexp.MustCompile(`^(-?[0-9]+(?:\.[0-9]+)?)%$`)
)

// Parse reads the record text that sits in dir, Dir(<epic>). It returns
// what it could read, and each problem with its line and rule.
func Parse(dir, text string) (Record, []Problem) {
	epic := path.Base(dir)
	var rec Record
	var out []Problem
	add := func(line int, rule, format string, a ...any) {
		out = append(out, Problem{Line: line, Rule: rule, Msg: fmt.Sprintf(format, a...)})
	}
	epicLine, pinLine := 0, 0
	var ids []string
	var stateLines []int // the state lines of each screen, parsed or not
	for i, raw := range strings.Split(text, "\n") {
		n, l := i+1, strings.TrimRight(raw, " \t\r")
		var screen *Screen
		if len(rec.Screens) > 0 {
			screen = &rec.Screens[len(rec.Screens)-1]
		}
		if m := fieldLine.FindStringSubmatch(l); m != nil {
			switch m[1] {
			case "Epic":
				rec.Epic, epicLine = m[2], n
			case "PRD":
				rec.PRD = strings.Trim(m[2], "`")
			case "Prototype":
				pinLine = n
				p := pinValue.FindStringSubmatch(m[2])
				switch {
				case p == nil:
					add(n, "pin_missing", "the **Prototype:** line reads `loop/%s-prototype` at `<40-character commit>`", epic)
				case p[1] != "loop/"+epic+"-prototype":
					add(n, "pin_branch", "the pin names branch %s; the prototype of %s is on loop/%s-prototype", p[1], epic, epic)
				default:
					rec.Pin = Pin{Branch: p[1], Commit: p[2]}
				}
			case "Stories":
				if screen == nil {
					add(n, "record_shape", "a **Stories:** line sits under no screen")
					continue
				}
				screen.StoriesLine = n
				for _, s := range strings.Split(m[2], ",") {
					if s = strings.TrimSpace(s); s != "" {
						screen.Stories = append(screen.Stories, s)
					}
				}
			}
			continue
		}
		if m := screenHead.FindStringSubmatch(l); m != nil {
			if slices.Contains(ids, m[1]) {
				add(n, "record_shape", "%s is already a screen; each screen has its own id", m[1])
			}
			ids = append(ids, m[1])
			rec.Screens = append(rec.Screens, Screen{ID: m[1], Name: m[2], Line: n})
			stateLines = append(stateLines, 0)
			continue
		}
		if strings.HasPrefix(l, "### ") {
			add(n, "record_shape", "a screen heading reads `### S<n>. <name>`")
			continue
		}
		if screen == nil || !listItem.MatchString(l) {
			continue
		}
		stateLines[len(stateLines)-1]++
		m := stateItem.FindStringSubmatch(l)
		if m == nil {
			add(n, "record_shape", "a state line reads `- **S<n>.<m> <name>.** Picture `<file>.png`, target `<target>`.`")
			continue
		}
		st := State{ID: m[1] + "." + m[2], Name: m[3], Line: n}
		ok := true
		bad := func(rule, format string, a ...any) { add(n, rule, format, a...); ok = false }
		switch {
		case m[1] != screen.ID:
			bad("record_shape", "%s sits under %s; a state's id starts with its screen's id", st.ID, screen.ID)
		case slices.Contains(ids, st.ID):
			bad("record_shape", "%s is already a state; each state has its own id", st.ID)
		}
		ids = append(ids, st.ID)
		rest := m[4]
		if p := picture.FindStringSubmatch(rest); p == nil || path.Base(p[1]) != p[1] || !strings.HasSuffix(p[1], ".png") {
			bad("state_no_picture", "%s names no picture; write Picture `<file>.png`, a file in %s", st.ID, Dir(epic))
		} else {
			st.Picture = p[1]
		}
		if t := target.FindStringSubmatch(rest); t == nil || t[1] == "" {
			bad("record_shape", "%s names no target; write target `<target>`, the string the capture command receives", st.ID)
		} else {
			st.Target = t[1]
		}
		if p := pixelMark.FindStringSubmatch(strings.TrimSuffix(codeSpan.ReplaceAllString(rest, "``"), ".")); p != nil {
			px, good := parsePixel(p[1])
			if !good {
				bad("pixel_threshold", "%s: the pixel mark reads %q; write a percent from 0%% to 5%% with its %% sign, such as pixel 1.5%%, or pixel alone for the default %v%%", st.ID, p[1], DefaultPixelPercent)
			} else {
				st.Pixel = &px
			}
		}
		if ok {
			screen.States = append(screen.States, st)
		}
	}
	switch {
	case epicLine == 0:
		add(0, "record_shape", "the record holds no **Epic:** line")
	case rec.Epic != epic:
		add(epicLine, "record_shape", "the **Epic:** line names %s, and the record sits in %s", rec.Epic, Dir(epic))
	}
	if rec.PRD == "" {
		add(0, "record_shape", "the record holds no **PRD:** line with the path of its PRD")
	}
	if pinLine == 0 {
		add(0, "pin_missing", "the record holds no **Prototype:** line; it reads `loop/%s-prototype` at `<40-character commit>`", epic)
	}
	if len(rec.Screens) == 0 {
		add(0, "record_shape", "the record holds no screen; a screen is a `### S<n>. <name>` heading")
	}
	for i, s := range rec.Screens {
		if len(s.Stories) == 0 {
			add(s.Line, "screen_no_story", "%s cites no story; write **Stories:** with the PRD's story labels under it", s.ID)
		}
		if stateLines[i] == 0 {
			add(s.Line, "record_shape", "%s holds no state", s.ID)
		}
	}
	return rec, out
}

// parsePixel reads the text after a pixel mark: nothing for the default,
// or a percent with its % sign in the allowed range.
func parsePixel(s string) (Pixel, bool) {
	if s == "" {
		return Pixel{Percent: DefaultPixelPercent, Default: true}, true
	}
	m := percent.FindStringSubmatch(s)
	if m == nil {
		return Pixel{}, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil || v < 0 || v > MaxPixelPercent {
		return Pixel{}, false
	}
	return Pixel{Percent: v}, true
}
