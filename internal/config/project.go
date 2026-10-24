package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ProjectConfigPath is where project config sits in the repository.
const ProjectConfigPath = ".loop/config.yaml"

// MergeMechanism names how an eligible PR merges (KTD29).
type MergeMechanism string

// The three merge mechanisms (KTD29).
const (
	MergeMergify     MergeMechanism = "mergify"
	MergeGitHubQueue MergeMechanism = "github-queue"
	MergeDirect      MergeMechanism = "direct"
)

// ProjectConfig is the repository scope of R35. The server reads it from the
// default branch only (KTD20).
type ProjectConfig struct {
	Manifest    string                 `yaml:"manifest,omitempty"` // repository path of the secret manifest
	Setup       string                 `yaml:"setup,omitempty"`    // the command that prepares a fresh checkout
	Tracker     TrackerConfig          `yaml:"tracker,omitempty"`
	Merge       MergeConfig            `yaml:"merge"`
	Stages      map[string]StageConfig `yaml:"stages,omitempty"`
	Gates       []Command              `yaml:"gates,omitempty"`
	PolicyHooks []Command              `yaml:"policy_hooks,omitempty"`
	Signoff     SignoffConfig          `yaml:"signoff,omitempty"`
	Health      HealthConfig           `yaml:"health,omitempty"`
}

// HealthConfig is the project's own health limits, which the overwatch
// sweep reads (ADR 0009). Each field is optional, and none has a default:
// a part whose limit is absent reports its reading and holds no work.
type HealthConfig struct {
	// AuditWorkflow is the file name under .github/workflows of the
	// project's dependency audit, such as audit.yml.
	AuditWorkflow string `yaml:"audit_workflow,omitempty"`
}

// workflowFileRe matches the file name of a workflow under
// .github/workflows.
var workflowFileRe = regexp.MustCompile(`^[A-Za-z0-9._-]+\.ya?ml$`)

// SignoffConfig names who signs the project's documents off. GitHub
// notifies each reader when the server opens a PRD or a spec PR, the two
// documents a person merges. Empty means GitHub notifies nobody, and the
// operator learns of a sign-off from the inbox only.
type SignoffConfig struct {
	Readers []string `yaml:"readers,omitempty"` // GitHub logins, never an organisation
}

// TrackerConfig names the project's tracker adapter. Empty means none.
type TrackerConfig struct {
	Adapter string `yaml:"adapter,omitempty"`
}

// MergeConfig names the project's merge mechanism.
type MergeConfig struct {
	Mechanism MergeMechanism `yaml:"mechanism"`
}

// StageConfig sets one stage for the project. A listed stage is enabled
// unless it says enabled: false.
type StageConfig struct {
	Enabled   *bool         `yaml:"enabled,omitempty"`
	Model     string        `yaml:"model,omitempty"`
	Harness   string        `yaml:"harness,omitempty"`
	Effort    string        `yaml:"effort,omitempty"`     // overrides the stage's default effort
	RunBudget time.Duration `yaml:"run_budget,omitempty"` // overrides the host default (KTD9)
	Schedule  string        `yaml:"schedule,omitempty"`   // overrides the host default schedule
	// Escalated is the profile of a run on an item whose model_tier is
	// escalated, after two code-review rejects. Nil takes the default.
	Escalated *EscalatedProfile `yaml:"escalated,omitempty"`
}

// EscalatedProfile is the stronger profile a stage's run takes after two
// code-review rejects. Its model is the full id the harness records, since
// the start compares the receipt with it exactly. An empty harness or
// effort takes the stage's own.
type EscalatedProfile struct {
	Model   string `yaml:"model,omitempty"`
	Harness string `yaml:"harness,omitempty"`
	Effort  string `yaml:"effort,omitempty"`
}

// Command is one named shell command, such as a gate or a policy hook.
type Command struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
}

// ParseProjectConfig parses and validates project config.
func ParseProjectConfig(data []byte) (*ProjectConfig, error) {
	var cfg ProjectConfig
	if err := decodeStrict(ProjectConfigPath, data, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *ProjectConfig) validate() error {
	ck := &checker{source: ProjectConfigPath}
	if c.Manifest != "" && !repoPath(c.Manifest) {
		ck.fail("manifest", "must be a clean path inside the repository")
	}
	if HasControl(strings.ReplaceAll(c.Setup, "\n", "")) {
		ck.fail("setup", "must not hold a control character")
	}
	if c.Tracker.Adapter != "" && !validIdent(c.Tracker.Adapter) {
		ck.fail("tracker.adapter", "must be a lower-case identifier")
	}
	switch c.Merge.Mechanism {
	case MergeMergify, MergeGitHubQueue, MergeDirect:
	default:
		ck.fail("merge.mechanism", "must be mergify, github-queue or direct")
	}
	for _, name := range slices.Sorted(maps.Keys(c.Stages)) {
		s, f := c.Stages[name], "stages."+name
		if !validIdent(name) {
			ck.fail("stages", "stage names must be lower-case identifiers")
			continue
		}
		if s.Harness != "" && !validIdent(s.Harness) {
			ck.fail(f+".harness", "must be a lower-case identifier")
		}
		if s.Effort != "" && !effortRe.MatchString(s.Effort) {
			ck.fail(f+".effort", "must be one lower-case word, such as high")
		}
		if HasControl(s.Model) || HasControl(s.Schedule) {
			ck.fail(f, "must not hold a control character")
		}
		if s.RunBudget < 0 {
			ck.fail(f+".run_budget", "must not be negative")
		}
		if e := s.Escalated; e != nil {
			if e.Harness != "" && !validIdent(e.Harness) {
				ck.fail(f+".escalated.harness", "must be a lower-case identifier")
			}
			if e.Effort != "" && !effortRe.MatchString(e.Effort) {
				ck.fail(f+".escalated.effort", "must be one lower-case word, such as high")
			}
			if HasControl(e.Model) {
				ck.fail(f+".escalated", "must not hold a control character")
			}
		}
	}
	checkCommands(ck, "gates", c.Gates)
	checkCommands(ck, "policy_hooks", c.PolicyHooks)
	seen := map[string]bool{}
	for i, r := range c.Signoff.Readers {
		f := fmt.Sprintf("signoff.readers[%d]", i)
		switch {
		case !ownerRe.MatchString(r):
			ck.fail(f, "must be a GitHub login")
		case seen[strings.ToLower(r)]:
			ck.fail(f, "names a reader twice")
		}
		seen[strings.ToLower(r)] = true
	}
	if w := c.Health.AuditWorkflow; w != "" && !workflowFileRe.MatchString(w) {
		ck.fail("health.audit_workflow", "must be the file name of a workflow under .github/workflows, such as audit.yml")
	}
	return ck.err()
}

func checkCommands(ck *checker, field string, cmds []Command) {
	seen := map[string]bool{}
	for i, cmd := range cmds {
		f := fmt.Sprintf("%s[%d]", field, i)
		switch {
		case !validIdent(cmd.Name):
			ck.fail(f+".name", "must be a lower-case identifier")
		case seen[cmd.Name]:
			ck.fail(f+".name", "names a command twice")
		}
		seen[cmd.Name] = true
		if strings.TrimSpace(cmd.Run) == "" {
			ck.fail(f+".run", "must be a nonempty command")
		}
	}
}

// repoPath reports whether p is a clean relative path inside the
// repository.
func repoPath(p string) bool {
	clean := path.Clean(p)
	return !path.IsAbs(p) && clean == p && clean != ".." && !strings.HasPrefix(clean, "../") && !HasControl(p)
}

// ManifestPath is the repository path of the secret manifest.
func (c *ProjectConfig) ManifestPath() string {
	if c.Manifest == "" {
		return DefaultManifestPath
	}
	return c.Manifest
}

// StageEnabled reports whether the project enables stage.
func (c *ProjectConfig) StageEnabled(stage string) bool {
	s, ok := c.Stages[stage]
	return ok && (s.Enabled == nil || *s.Enabled)
}

// CheckStages refuses a stage name outside known. The scheduler owns
// the stage list, so it calls this after it loads a config.
func (c *ProjectConfig) CheckStages(known []string) error {
	ck := &checker{source: ProjectConfigPath}
	for _, name := range slices.Sorted(maps.Keys(c.Stages)) {
		found := false
		for _, k := range known {
			found = found || k == name
		}
		if !found {
			ck.fail("stages."+name, "is not a stage of this loop")
		}
	}
	return ck.err()
}

// RefReader returns the bytes of path at a git ref. It returns an error that
// matches fs.ErrNotExist when the ref holds no such file. The GitHub
// service supplies the reader over the project's bare mirror.
type RefReader func(ctx context.Context, ref, path string) ([]byte, error)

// DefaultRef is the ref that holds a project's committed config: the remote
// default branch. An agent can edit config on its own branch, so no other
// ref is ever read (KTD20).
func DefaultRef(defaultBranch string) string { return "origin/" + defaultBranch }

// ProjectSnapshot is a project's config and manifest from one read.
type ProjectSnapshot struct {
	Config      *ProjectConfig
	Manifest    *Manifest
	ConfigRaw   []byte
	ManifestRaw []byte // nil when the default manifest is absent
	// Branch is the default branch the read named. A run of the project
	// starts from it.
	Branch string
}

// BaseBranch is the branch a run starts from: the branch of the read, or
// main for a snapshot that names none.
func (s *ProjectSnapshot) BaseBranch() string {
	if s.Branch == "" {
		return DefaultBranch
	}
	return s.Branch
}

// ReadProjectSnapshot reads project config and the secret manifest from the
// default branch and validates both. An absent default manifest reads as an
// empty manifest. A manifest path that config names must exist.
func ReadProjectSnapshot(ctx context.Context, read RefReader, defaultBranch string) (*ProjectSnapshot, error) {
	if err := checkBranchName(defaultBranch); err != nil {
		return nil, err
	}
	ref := DefaultRef(defaultBranch)
	raw, err := read(ctx, ref, ProjectConfigPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &InvalidError{Source: ProjectConfigPath, Reason: "the file is missing on " + ref, Missing: true}
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s at %s: %w", ProjectConfigPath, ref, err)
	}
	cfg, err := ParseProjectConfig(raw)
	if err != nil {
		return nil, err
	}
	snap := &ProjectSnapshot{Config: cfg, ConfigRaw: raw, Manifest: &Manifest{}, Branch: defaultBranch}
	mraw, err := read(ctx, ref, cfg.ManifestPath())
	switch {
	case errors.Is(err, fs.ErrNotExist) && cfg.Manifest == "":
		return snap, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, &InvalidError{Source: ProjectConfigPath, Field: "manifest", Reason: "names a file that is missing on " + ref}
	case err != nil:
		return nil, fmt.Errorf("config: read %s at %s: %w", cfg.ManifestPath(), ref, err)
	}
	if snap.Manifest, err = ParseManifest(mraw); err != nil {
		return nil, err
	}
	snap.ManifestRaw = mraw
	return snap, nil
}

// checkBranchName refuses a default branch name that git could read as an
// option, a revision expression or another ref.
func checkBranchName(b string) error {
	if b == "" || strings.HasPrefix(b, "-") || strings.Contains(b, "..") || strings.ContainsAny(b, " ~^:?*[\\@{") || HasControl(b) {
		return &InvalidError{Source: "project registration", Field: "default branch", Reason: "is not a plain branch name"}
	}
	return nil
}
