package admission

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/machine"
)

// DependsOn is the ticket section that names what must merge first.
const DependsOn = "Depends on"

// PullHead is one open pull request of a project: its number and its head
// branch.
type PullHead struct {
	Number int
	Branch string
}

// PullReader lists a project's open pull requests. The GitHub service
// supplies it. Admission calls it outside every lock, and only for a
// ticket whose `## Depends on` names a pull request.
type PullReader interface {
	OpenPulls(ctx context.Context, project string) ([]PullHead, error)
}

// Named is what `## Depends on` names that could be a pull request.
type Named struct {
	Numbers  []int
	Branches []string
}

var (
	// pullNumberRe finds `#41`, with no word or path character before it,
	// or the number of a pull request URL.
	pullNumberRe = regexp.MustCompile(`(?:^|[^\w/])#(\d+)\b|/pull/(\d+)\b`)
	pullSpanRe   = regexp.MustCompile("`([^`\\s]+)`")
)

// NamedPulls returns each number and each code span in the body's
// `## Depends on`, in the order the section holds them. A pull request
// named in any other section is not read. Ported from namedPulls in
// scripts/sdlc/pr-dependency.ts (alpha).
func NamedPulls(body string) Named {
	section := dependsOn(body)
	var out Named
	for _, m := range pullNumberRe.FindAllStringSubmatch(section, -1) {
		n, err := strconv.Atoi(m[1] + m[2])
		if err == nil && !slices.Contains(out.Numbers, n) {
			out.Numbers = append(out.Numbers, n)
		}
	}
	for _, m := range pullSpanRe.FindAllStringSubmatch(section, -1) {
		if !slices.Contains(out.Branches, m[1]) {
			out.Branches = append(out.Branches, m[1])
		}
	}
	return out
}

// dependsOn returns the text under `## Depends on`, up to the next heading
// of level 1 or 2.
func dependsOn(body string) string {
	lines := strings.Split(body, "\n")
	start := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == "## "+DependsOn })
	if start < 0 {
		return ""
	}
	var out []string
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// pullHolds names each open pull request that the ticket's `## Depends on`
// names. A named pull request that merged or closed holds nothing, and so
// does a number that names no open pull request: GitHub numbers issues and
// pull requests from one sequence. A failed read is an error, so an
// unknown result never admits the ticket. It makes outside reads, so no
// caller holds a lock.
func (a *Admission) pullHolds(ctx context.Context, it machine.Item) ([]string, error) {
	if a.cfg.Pulls == nil || it.Kind != machine.KindTicket {
		return nil, nil
	}
	t, err := a.task(ctx, it.Project, it.ID)
	if err != nil {
		return nil, fmt.Errorf("admission: read %s: %w", it.ID, err)
	}
	named := NamedPulls(t.Body)
	if len(named.Numbers) == 0 && len(named.Branches) == 0 {
		return nil, nil
	}
	open, err := a.cfg.Pulls.OpenPulls(ctx, it.Project)
	if err != nil {
		return nil, fmt.Errorf("admission: read the open pull requests that %s depends on: %w", it.ID, err)
	}
	var out []string
	for _, p := range open {
		if slices.Contains(named.Numbers, p.Number) || slices.Contains(named.Branches, p.Branch) {
			out = append(out, fmt.Sprintf("pull request #%d (`%s`), which is still open", p.Number, p.Branch))
		}
	}
	return out, nil
}
