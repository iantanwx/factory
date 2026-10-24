// Package brief writes what one run finds in its checkout at start (KTD18,
// KTD19): .loop/brief.json and its rendered .md, the prompt file, the
// pinned skill pack for the run's harness, and .env. Every injected path
// goes into .git/info/exclude, so git status stays clean and git add -A
// stages none of them. A tracked file at an injected path refuses the run.
package brief

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
)

// Injected file paths, relative to the checkout root. cli.InjectedFiles
// lists them all.
const (
	BriefJSON  = cli.BriefJSONFile
	BriefMD    = cli.BriefMDFile
	PromptFile = cli.PromptFile
	EnvFile    = cli.EnvFile
	// The CLI and the token writer create these later. They are excluded
	// in advance.
	TokenFile   = cli.TokenPath
	PendingFile = cli.PendingFile
	PendingLock = cli.PendingLockFile
)

// MaxAttempts is the failure limit an attempt counts against (R10).
const MaxAttempts = 2

// Brief is what a run reads first. It names secrets and never holds a
// value.
type Brief struct {
	RunID       string `json:"run_id"`
	Project     string `json:"project"`
	Stage       string `json:"stage"`
	Role        string `json:"role"`
	Harness     string `json:"harness"`
	Model       string `json:"model"`
	PackVersion string `json:"pack_version"`
	PackHash    string `json:"pack_hash"`
	Runbook     string `json:"runbook"`
	BaseBranch  string `json:"base_branch"`
	Item        *Item  `json:"item,omitempty"`
	// Attempt is this attempt's number for the item's current inputs.
	Attempt       int      `json:"attempt,omitempty"`
	MaxAttempts   int      `json:"max_attempts,omitempty"`
	PriorFailures []string `json:"prior_failures,omitempty"`
	Handoff       []string `json:"handoff,omitempty"`
	// KeptCheckouts names the checkouts of the item's earlier attempts
	// that ended with no finish and still hold uncommitted work.
	KeptCheckouts []KeptCheckout `json:"kept_checkouts,omitempty"`
	// RedBuild is the failed CI of the item's pull request. Only a
	// repair's brief carries it.
	RedBuild    *RedBuild `json:"red_build,omitempty"`
	AllowedNext []string  `json:"allowed_next"`
	Secrets     Secrets   `json:"secrets"`
	// Faults lists the loop faults filed from this project and stage that
	// are open, acknowledged, or fixed in the last FaultWindow (KTD31).
	Faults []Fault `json:"faults,omitempty"`
}

// FaultWindow is how long a fixed fault stays in the briefs.
const FaultWindow = 14 * 24 * time.Hour

// Fault is one loop fault as a brief names it. `loop fault show <id>`
// prints the whole report.
type Fault struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	FixedIn string `json:"fixed_in,omitempty"`
}

// Item is the work item a run holds.
type Item struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Title     string            `json:"title"`
	Status    string            `json:"status"`
	Body      string            `json:"body,omitempty"`
	Labels    []string          `json:"labels,omitempty"`
	Documents []string          `json:"documents,omitempty"`
	Facts     map[string]string `json:"facts,omitempty"`
}

// MaxDirty is how many dirty files a brief names for one kept checkout.
const MaxDirty = 50

// KeptCheckout is the checkout of an earlier attempt that ended with no
// finish. The server keeps it, and its uncommitted work, for the next run.
type KeptCheckout struct {
	RunID  string `json:"run_id"`
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	// Dirty lists at most MaxDirty modified, added, deleted or untracked
	// paths. More counts the paths left out.
	Dirty []string `json:"dirty"`
	More  int      `json:"more,omitempty"`
}

// RedBuild is the failed CI of the item's pull request at its head, read
// when a repair starts, so the run sees the failure before any read
// (alpha implementer-ci.ts). `loop pr logs` prints the whole tail.
type RedBuild struct {
	PR     int           `json:"pr"`
	Head   string        `json:"head"`
	Checks []FailedCheck `json:"checks,omitempty"`
	// Unreadable says why the checks did not read. The run still starts.
	Unreadable string `json:"unreadable,omitempty"`
}

// FailedCheck is one failed CI job: the gate that failed, the failing
// tests and an excerpt of its log, or why the log did not read.
type FailedCheck struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
	// Gate is the gate `loop gates` named in `FAIL <gate>`.
	Gate  string       `json:"gate,omitempty"`
	Tests []FailedTest `json:"tests,omitempty"`
	// Excerpt starts at the first failing test, or is the end of the log
	// when the log names none.
	Excerpt    []string `json:"excerpt,omitempty"`
	Unreadable string   `json:"unreadable,omitempty"`
}

// FailedTest is one failing test and the package or file that holds it.
type FailedTest struct {
	Package string `json:"package,omitempty"`
	Name    string `json:"name"`
}

// Secrets names the manifest secrets in .env and the ones with no value.
type Secrets struct {
	Present []string `json:"present"`
	Missing []string `json:"missing"`
}

// Markdown renders the brief for a person and for a harness that reads
// Markdown more easily than JSON.
func (b Brief) Markdown() string {
	var s strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&s, format+"\n", args...) }
	line("# Run %s", b.RunID)
	line("")
	line("- Project: %s", b.Project)
	line("- Stage: %s (role %s)", b.Stage, b.Role)
	line("- Harness: %s, model %s", b.Harness, b.Model)
	line("- Runbook: `%s` (pack %s, %s)", b.Runbook, b.PackVersion, b.PackHash)
	line("- Base branch: %s", b.BaseBranch)
	if b.Item != nil {
		line("")
		line("## Item %s", b.Item.ID)
		line("")
		line("- Kind: %s", b.Item.Kind)
		line("- Title: %s", b.Item.Title)
		line("- Status: %s", b.Item.Status)
		if len(b.Item.Labels) > 0 {
			line("- Labels: %s", strings.Join(b.Item.Labels, ", "))
		}
		for _, d := range b.Item.Documents {
			line("- Document: `%s`", d)
		}
		if b.Item.Body != "" {
			line("")
			line("%s", b.Item.Body)
		}
	}
	if b.Attempt > 0 {
		line("")
		line("This is attempt %d of %d for the item's current inputs.", b.Attempt, b.MaxAttempts)
		for _, f := range b.PriorFailures {
			line("- Earlier failure: %s", f)
		}
	}
	for _, h := range b.Handoff {
		line("- Handoff note: %s", h)
	}
	if r := b.RedBuild; r != nil {
		line("")
		if r.PR != 0 {
			line("## Red build of PR #%d at %s", r.PR, r.Head)
		} else {
			line("## Red build")
		}
		line("")
		line("Read it before `loop pr logs`, which prints the whole tail of each failed job.")
		if r.Unreadable != "" {
			line("")
			line("CI log unreadable: %s", r.Unreadable)
		}
		for _, c := range r.Checks {
			line("")
			line("Check `%s` failed: %s", c.Name, c.URL)
			line("")
			if c.Unreadable != "" {
				line("CI log unreadable: %s", c.Unreadable)
				continue
			}
			if c.Gate != "" {
				line("The gate that failed: `%s`.", c.Gate)
				line("")
			}
			shown := "first"
			if len(c.Tests) == 0 {
				shown = "last"
				line("The log names no failing test. The excerpt is the end of the failed job.")
			} else {
				line("Failing tests:")
				line("")
				for _, t := range c.Tests {
					line("- `%s` > %s", t.Package, t.Name)
				}
			}
			line("")
			line("The %s %d lines of the log:", shown, len(c.Excerpt))
			line("")
			line("````text")
			for _, l := range c.Excerpt {
				line("%s", l)
			}
			line("````")
		}
	}
	if len(b.KeptCheckouts) > 0 {
		line("")
		line("## Uncommitted work of earlier attempts")
		line("")
		line("Each earlier attempt below ended with no finish. Its checkout is kept with the work it never committed.")
		line("Read each file there, and carry over what serves the item. Change nothing in that checkout.")
		for _, k := range b.KeptCheckouts {
			line("")
			if k.Branch != "" {
				line("- Run %s: `%s` (branch %s)", k.RunID, k.Path, k.Branch)
			} else {
				line("- Run %s: `%s`", k.RunID, k.Path)
			}
			for _, d := range k.Dirty {
				line("  - `%s`", d)
			}
			if k.More > 0 {
				line("  - and %d more: run `git -C %s status` to list them", k.More, k.Path)
			}
		}
	}
	if len(b.Faults) > 0 {
		line("")
		line("## Loop faults from this project and stage")
		line("")
		for _, f := range b.Faults {
			if f.FixedIn != "" {
				line("- %s: %s in %s", f.ID, f.Status, f.FixedIn)
			} else {
				line("- %s: %s", f.ID, f.Status)
			}
		}
		line("")
		line("`loop fault show <id>` prints a fault's report, its resolution and the release this host runs.")
	}
	line("")
	line("Allowed next moves: %s", orNone(b.AllowedNext))
	line("")
	line("Secrets in .env: %s", orNone(b.Secrets.Present))
	line("Secrets with no value: %s", orNone(b.Secrets.Missing))
	return s.String()
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}

// Prompt is the whole prompt text: it names only the brief and the
// runbook (KTD18). The coordinator holds no item and never finishes, so its
// prompt names no finish.
func Prompt(b Brief) string {
	finish := " When you are done, run `loop run finish --outcome done|failed|blocked`."
	if b.Role == "coordinator" {
		finish = ""
	}
	return fmt.Sprintf("You are a loop %s run. Read %s and %s in this checkout first. "+
		"Then follow the runbook at %s.%s\n",
		b.Role, BriefMD, BriefJSON, b.Runbook, finish)
}

// Injection is everything one run start writes into a checkout.
type Injection struct {
	Checkout string
	Harness  agenthost.Harness
	Pack     Pack
	Brief    Brief
	// Env is each manifest secret the run's stage receives, by the
	// directory whose .env holds it. An empty map writes no .env and
	// removes an old one.
	Env EnvFiles
}

// Result is what Inject wrote.
type Result struct {
	PackHash string
	// Prompt is the absolute path of the prompt file.
	Prompt string
}

// Inject checks every injected path against the tracked files, lists each
// one in .git/info/exclude, then writes the pack, the brief, the prompt
// and .env. A tracked injected path returns a *ConflictError and writes
// nothing. The token file is the caller's to write.
func Inject(ctx context.Context, in Injection) (Result, error) {
	agents, dirs, err := packPaths(in.Harness, in.Pack)
	if err != nil {
		return Result{}, err
	}
	hash, err := in.Pack.Hash()
	if err != nil {
		return Result{}, err
	}
	files := cli.InjectedFiles()
	for _, p := range in.Env.Paths() {
		if !slices.Contains(files, p) {
			files = append(files, p)
		}
	}
	files = append(files, agents...)
	if err := CheckTracked(ctx, in.Checkout, files, dirs); err != nil {
		return Result{}, err
	}
	if err := Exclude(ctx, in.Checkout, files, dirs); err != nil {
		return Result{}, err
	}
	if err := in.Pack.Write(in.Checkout, in.Harness); err != nil {
		return Result{}, err
	}
	b := in.Brief
	b.PackVersion, b.PackHash = in.Pack.Version, hash
	if b.Runbook == "" {
		b.Runbook = Runbook(in.Harness, b.Stage)
	}
	if b.Attempt > 0 && b.MaxAttempts == 0 {
		b.MaxAttempts = MaxAttempts
	}
	if b.AllowedNext == nil {
		b.AllowedNext = []string{}
	}
	if err := writeBrief(in.Checkout, b); err != nil {
		return Result{}, err
	}
	prompt := filepath.Join(in.Checkout, PromptFile)
	if err := config.WriteFileAtomic(prompt, []byte(Prompt(b)), 0o600); err != nil {
		return Result{}, fmt.Errorf("brief: write the prompt: %w", err)
	}
	if len(in.Env) == 0 {
		if err := RemoveEnv(in.Checkout); err != nil {
			return Result{}, err
		}
	} else if err := WriteEnv(ctx, in.Checkout, in.Env); err != nil {
		return Result{}, err
	}
	return Result{PackHash: hash, Prompt: prompt}, nil
}

// RefreshPack writes the pack into the checkout of a session that already
// runs, with the checks Inject makes, and leaves the brief alone.
func RefreshPack(ctx context.Context, checkout string, h agenthost.Harness, p Pack) error {
	agents, dirs, err := packPaths(h, p)
	if err != nil {
		return err
	}
	if err := CheckTracked(ctx, checkout, agents, dirs); err != nil {
		return err
	}
	if err := Exclude(ctx, checkout, agents, dirs); err != nil {
		return err
	}
	return p.Write(checkout, h)
}

// packPaths lists the pack's agent files and skill directories as the
// harness reads them.
func packPaths(h agenthost.Harness, p Pack) (agents, dirs []string, err error) {
	root := SkillRoot(h)
	if root == "" {
		return nil, nil, fmt.Errorf("brief: no skill directory for harness %q", h)
	}
	skills, err := p.Skills()
	if err != nil {
		return nil, nil, err
	}
	dirs = make([]string, 0, len(skills))
	for _, s := range skills {
		dirs = append(dirs, root+"/"+s)
	}
	agents, err = p.AgentFiles(h)
	if err != nil {
		return nil, nil, err
	}
	return agents, dirs, nil
}

func writeBrief(checkout string, b Brief) error {
	if err := os.MkdirAll(filepath.Join(checkout, cli.LoopDir), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if err := config.WriteFileAtomic(filepath.Join(checkout, BriefJSON), append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("brief: write %s: %w", BriefJSON, err)
	}
	if err := config.WriteFileAtomic(filepath.Join(checkout, BriefMD), []byte(b.Markdown()), 0o600); err != nil {
		return fmt.Errorf("brief: write %s: %w", BriefMD, err)
	}
	return nil
}
