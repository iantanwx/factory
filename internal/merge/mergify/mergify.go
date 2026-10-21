// Package mergify is the mergify merge mechanism (KTD29). A hand-off
// labels the PR. The project's Mergify configuration, rendered from
// deploy/mergify.yml.tmpl, queues a PR only with that label and the
// loop/eligible status, which the loop posts only on a reviewed head. A
// re-queue removes Mergify's dequeued label and posts Mergify's queue
// command as the App.
package mergify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"text/template"

	"github.com/iantanwx/loop/deploy"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/merge"
)

// Names the template and the mechanism share.
const (
	// QueueLabel is the label a hand-off adds. Every queueing rule of the
	// template needs it.
	QueueLabel = "loop-queue"
	// DequeuedLabel is the label Mergify adds when it drops a PR.
	DequeuedLabel = "dequeued"
	// QueueCommand resets Mergify's memory of a failed attempt.
	QueueCommand = "@mergifyio queue"
	// SetupBranch holds the template PR. It is not an item branch.
	SetupBranch = "loop-setup/mergify"
	// ConfigPath is where the template PR puts the configuration.
	ConfigPath = ".mergify.yml"
)

// configPaths are the places Mergify reads its configuration from. Any
// one of them on the base branch counts.
var configPaths = []string{".mergify.yml", ".mergify/config.yml", ".github/mergify.yml"}

// Mergify is the mechanism.
type Mergify struct {
	gh *github.Service

	mu   sync.Mutex
	slug string
}

// New returns the mechanism over the server's GitHub actor.
func New(gh *github.Service) *Mergify { return &Mergify{gh: gh} }

// Name implements merge.Mechanism.
func (m *Mergify) Name() config.MergeMechanism { return config.MergeMergify }

// HandOff labels the PR. loop/eligible is already on its head.
func (m *Mergify) HandOff(ctx context.Context, pr merge.PR) error {
	return m.gh.AddLabel(ctx, pr.Project, pr.Number, QueueLabel)
}

// Requeue removes dequeued, labels the PR again and posts the queue
// command as the App. A fresh label alone was dequeued again from
// Mergify's memory of the failed attempt (alpha #2850, land-queue.ts).
func (m *Mergify) Requeue(ctx context.Context, pr merge.PR) error {
	if err := m.gh.RemoveLabel(ctx, pr.Project, pr.Number, DequeuedLabel); err != nil {
		return err
	}
	if err := m.gh.AddLabel(ctx, pr.Project, pr.Number, QueueLabel); err != nil {
		return err
	}
	return m.gh.Comment(ctx, pr.Project, pr.Number, QueueCommand)
}

// Withdraw removes the queue label, so no queueing rule matches the PR.
func (m *Mergify) Withdraw(ctx context.Context, pr merge.PR) error {
	return m.gh.RemoveLabel(ctx, pr.Project, pr.Number, QueueLabel)
}

// SetupError refuses `project add` until the template PR merges.
type SetupError struct {
	Number int
	URL    string
}

func (e *SetupError) Error() string {
	return fmt.Sprintf("merge: the repository has no Mergify configuration; merge the loop's template PR #%d (%s), then run project add again",
		e.Number, e.URL)
}

// Check passes when the base branch holds a Mergify configuration. With
// none, it opens one template PR through the App and refuses the add
// until that PR merges. A repeated add finds the same PR.
func (m *Mergify) Check(ctx context.Context, p config.Project) error {
	base, err := m.gh.DefaultBranch(ctx, p)
	if err != nil {
		return err
	}
	for _, path := range configPaths {
		ok, err := m.gh.FileAt(ctx, p, base, path)
		if err != nil || ok {
			return err
		}
	}
	slug, err := m.appSlug(ctx)
	if err != nil {
		return err
	}
	_, checks, err := m.gh.BranchRules(ctx, p)
	if err != nil {
		return err
	}
	body, err := Render(Template{Bot: slug + "[bot]", Base: base, Checks: checks})
	if err != nil {
		return err
	}
	pr, _, err := m.gh.OpenFilePR(ctx, p, github.FilePR{
		Branch: SetupBranch, Path: ConfigPath, Content: body,
		Message: "Add the loop's Mergify configuration",
		Title:   "Add the loop's Mergify configuration",
		Body: "The loop lands eligible PRs through Mergify. This file queues only a PR that the loop labelled `" + QueueLabel +
			"` and that holds `" + github.EligibleContext + "` at its head.",
	})
	if err != nil {
		return err
	}
	return &SetupError{Number: pr.Number, URL: pr.URL}
}

func (m *Mergify) appSlug(ctx context.Context) (string, error) {
	m.mu.Lock()
	s := m.slug
	m.mu.Unlock()
	if s != "" {
		return s, nil
	}
	s, err := m.gh.AppSlug(ctx)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.slug = s
	m.mu.Unlock()
	return s, nil
}

// Template holds the inputs of deploy/mergify.yml.tmpl.
type Template struct {
	Bot    string   // the loop App's bot user, "<slug>[bot]"
	Base   string   // the default branch
	Checks []string // the required CI contexts of the base, without loop/eligible
}

var (
	botRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\[bot\]$`)
	tmpl   = template.Must(template.New("mergify").Funcs(template.FuncMap{"cond": cond}).Parse(deploy.MergifyTemplate))
	errBad = errors.New("merge: the Mergify template input")
)

// cond renders one Mergify condition as a YAML double-quoted string, so
// no input can end the scalar.
func cond(op, value string) (string, error) {
	b, err := json.Marshal(op + " " + value)
	return string(b), err
}

// Render renders the project's Mergify configuration.
func Render(t Template) ([]byte, error) {
	switch {
	case !botRe.MatchString(t.Bot):
		return nil, fmt.Errorf("%w: %q is not a bot user", errBad, t.Bot)
	case t.Base == "" || config.HasControl(t.Base) || strings.ContainsAny(t.Base, " \"'"):
		return nil, fmt.Errorf("%w: %q is not a plain branch name", errBad, t.Base)
	}
	for _, c := range t.Checks {
		if c == "" || config.HasControl(c) {
			return nil, fmt.Errorf("%w: check %q holds a control character", errBad, c)
		}
	}
	var out bytes.Buffer
	err := tmpl.Execute(&out, map[string]any{
		"Bot": t.Bot, "Base": t.Base, "Label": QueueLabel, "Eligible": github.EligibleContext,
		"Dequeued": DequeuedLabel, "Checks": t.Checks,
	})
	return out.Bytes(), err
}
