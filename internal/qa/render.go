package qa

import (
	"fmt"
	"strings"
)

// Render writes the title, the summary and the text of the loop/qa check
// run of r. links maps each evidence path to its URL, and a path with no
// URL stays a code span.
func Render(r Report, links map[string]string) (title, summary, text string) {
	var s, t strings.Builder
	switch c := r.Conclusion(); {
	case r.Verify == verifyMissing:
		title = "QA found no verify skill"
		s.WriteString("The tree at the head holds no verify skill, so no check ran. Write one with loop-verify-create.\n")
	case c == "failure":
		title = "QA failed"
	case c == "success":
		title = "QA passed"
	default:
		title = "QA did not drive every app"
	}
	for _, c := range r.Checks {
		fmt.Fprintf(&s, "- %s: %s\n", c.name(), c.Result)
		fmt.Fprintf(&t, "### %s: %s\n\n", c.name(), c.Result)
		if c.Result == resultUnstartable {
			t.WriteString("The app started at neither the head nor the merge base.\n\n")
			continue
		}
		if c.Goal != "" {
			fmt.Fprintf(&t, "Goal: %s\n\n", c.Goal)
		}
		fmt.Fprintf(&t, "%d steps, %d minutes, %d screens.\n\n", c.Steps, c.Minutes, c.Screens)
		for _, f := range c.Findings {
			fmt.Fprintf(&t, "- %s: %s\n", f.Severity, f.Summary)
			if len(f.Steps) > 0 {
				fmt.Fprintf(&t, "  - Steps: %s\n", strings.Join(f.Steps, ", then "))
			}
			for _, p := range f.Evidence {
				if url, ok := links[p]; ok {
					fmt.Fprintf(&t, "  - Evidence: [%s](%s)\n", p, url)
				} else {
					fmt.Fprintf(&t, "  - Evidence: `%s`\n", p)
				}
			}
		}
		if len(c.Findings) > 0 {
			t.WriteString("\n")
		}
	}
	if len(r.Bugs) > 0 {
		fmt.Fprintf(&s, "\nBugs filed: %s\n", strings.Join(r.Bugs, ", "))
		fmt.Fprintf(&t, "### Bugs\n\n- %s\n\n", strings.Join(r.Bugs, "\n- "))
	}
	if r.Notes != "" {
		fmt.Fprintf(&t, "### Notes\n\n%s\n", r.Notes)
	}
	return title, s.String(), t.String()
}
