package machine_test

import (
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/shape"
)

var (
	numbered = regexp.MustCompile(`^[0-9]+\. `)
	heading  = regexp.MustCompile("^[0-9]+\\. `## ([^`]+)`")
)

// C15, the reproducer half: section 4 of the reproducer runbook lists each
// section of shape.Bug as a "## " heading, and names each of its limits
// in words on the line of the section it holds for. The body total is
// named in a paragraph of its own.
func TestC15TheReproducerNamesTheBugShape(t *testing.T) {
	raw, err := fs.ReadFile(pack.Source(), "runbooks/reproducer.md")
	if err != nil {
		t.Fatal(err)
	}
	sec := pack.Section(string(raw), 2, func(s string) bool { return strings.HasSuffix(s, "Shape the ticket") })
	if sec == "" {
		t.Fatal("the reproducer runbook holds no ## 4. Shape the ticket")
	}
	// A numbered item, or a paragraph, is one rule, with each run of
	// white space as one space.
	var rules []string
	for _, l := range strings.Split(sec, "\n") {
		if strings.TrimSpace(l) == "" || numbered.MatchString(l) || len(rules) == 0 {
			rules = append(rules, "")
		}
		rules[len(rules)-1] += " " + l
	}
	items := map[string]string{}
	var names []string
	for _, r := range rules {
		r = strings.Join(strings.Fields(r), " ")
		if m := heading.FindStringSubmatch(r); m != nil {
			names = append(names, m[1])
			items[m[1]] = r
		} else if strings.HasPrefix(r, "The body") {
			items[""] = r
		}
	}
	var want []string
	for _, n := range shape.Bug.Required {
		want = append(want, n.Name)
	}
	if !slices.Equal(names, want) {
		t.Errorf("the runbook lists %q, shape.Bug requires %q", names, want)
	}
	for _, l := range shape.Bug.Limits {
		words := fmt.Sprintf("%d words", l.Words)
		if !strings.Contains(items[l.Section], words) {
			t.Errorf("the runbook's text for %q does not name %q: %q", l.Section, words, items[l.Section])
		}
	}
}
