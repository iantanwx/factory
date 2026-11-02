package shape

import (
	"fmt"
	"regexp"
	"strings"
)

// Screens is the **Screens:** line of one ticket body: the state ids it
// cites, in line order, or the reason of a none line. A parsed Screens
// holds states or a reason, never both and never neither.
type Screens struct {
	States []string
	None   string
}

// ScreensFault is one fault of a **Screens:** line. A faulty line is no
// Screens.
type ScreensFault struct {
	Rule string // screens_missing, screens_no_reason, screens_bad_id, screens_twice
	Msg  string
}

var (
	screensLine = regexp.MustCompile(`^\*\*Screens:\*\*\s*(.*?)\s*$`)
	screensNone = regexp.MustCompile(`^none\b\.?\s*(.*)$`)
	stateID     = regexp.MustCompile(`^S[0-9]+\.[0-9]+$`)
)

// ScreensOf reads the **Screens:** line of the ## Source section of body.
// present is false when the section holds no such line.
func ScreensOf(body string) (s Screens, present bool, faults []ScreensFault) {
	var found []line
	for _, sec := range sections(body) {
		if sec.Heading != "Source" {
			continue
		}
		for _, l := range sec.Lines {
			if !l.Fenced && screensLine.MatchString(l.Text) {
				found = append(found, l)
			}
		}
	}
	if len(found) == 0 {
		return Screens{}, false, nil
	}
	fault := func(rule, format string, a ...any) (Screens, bool, []ScreensFault) {
		return Screens{}, true, []ScreensFault{{Rule: rule, Msg: fmt.Sprintf(format, a...)}}
	}
	if len(found) > 1 {
		return fault("screens_twice", "a second **Screens:** line on line %d; ## Source holds one", found[1].N)
	}
	v := screensLine.FindStringSubmatch(found[0].Text)[1]
	if v == "" {
		return fault("screens_missing", "the **Screens:** line cites no state; write the state ids, or none. <reason>")
	}
	if m := screensNone.FindStringSubmatch(v); m != nil {
		if m[1] == "" {
			return fault("screens_no_reason", "the **Screens:** none line holds no reason; write none. <why the ticket draws no screen>")
		}
		return Screens{None: m[1]}, true, nil
	}
	for _, id := range strings.Split(strings.TrimSuffix(v, "."), ",") {
		id = strings.TrimSpace(id)
		if !stateID.MatchString(id) {
			return fault("screens_bad_id", "the **Screens:** line cites %q; a state id reads S<n>.<m>", id)
		}
		s.States = append(s.States, id)
	}
	return s, true, nil
}
