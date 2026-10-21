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
	"strings"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
)

// Injected file paths, relative to the checkout root.
const (
	BriefJSON  = cli.LoopDir + "/brief.json"
	BriefMD    = cli.LoopDir + "/brief.md"
	PromptFile = cli.LoopDir + "/prompt.md"
	EnvFile    = ".env"
	// The CLI and the token writer create these later. They are excluded
	// in advance.
	TokenFile   = cli.LoopDir + "/" + cli.TokenFileName
	PendingFile = cli.LoopDir + "/" + cli.PendingFileName
	PendingLock = cli.LoopDir + "/pending.lock"
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
	AllowedNext   []string `json:"allowed_next"`
	Secrets       Secrets  `json:"secrets"`
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
// runbook (KTD18).
func Prompt(b Brief) string {
	return fmt.Sprintf("You are a loop %s run. Read %s and %s in this checkout first. "+
		"Then follow the runbook at %s. When you are done, run `loop run finish --outcome done|failed|blocked`.\n",
		b.Role, BriefMD, BriefJSON, b.Runbook)
}

// Injection is everything one run start writes into a checkout.
type Injection struct {
	Checkout string
	Harness  agenthost.Harness
	Pack     Pack
	Brief    Brief
	// Env is every manifest secret. An empty map writes no .env and
	// removes an old one.
	Env map[string]string
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
	root := SkillRoot(in.Harness)
	if root == "" {
		return Result{}, fmt.Errorf("brief: no skill directory for harness %q", in.Harness)
	}
	skills, err := in.Pack.Skills()
	if err != nil {
		return Result{}, err
	}
	hash, err := in.Pack.Hash()
	if err != nil {
		return Result{}, err
	}
	files := []string{BriefJSON, BriefMD, PromptFile, EnvFile, TokenFile, PendingFile, PendingLock}
	dirs := make([]string, 0, len(skills))
	for _, s := range skills {
		dirs = append(dirs, root+"/"+s)
	}
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
	b.PackVersion, b.PackHash, b.Runbook = in.Pack.Version, hash, Runbook(in.Harness, b.Stage)
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
	} else if err := WriteEnv(in.Checkout, in.Env); err != nil {
		return Result{}, err
	}
	return Result{PackHash: hash, Prompt: prompt}, nil
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
