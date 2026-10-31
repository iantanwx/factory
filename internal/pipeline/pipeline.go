// Package pipeline is the document pipeline and work entry. Work
// enters as a PRD that an intake room submits: the server pushes its
// branch, opens its PR and creates the epic in prd_review in one call. Or
// the coordinator creates a PRD epic from the operator's request with epic
// create, and it waits in prd_needed for its PRD author. The
// doc-reviewer reviews it, the amender revises it once after a first
// reject, and the operator signs it off by merging its PR, which the
// GitHub reactor observes. The spec follows the same review and sign-off.
// The decomposer turns the approved spec into tickets in one batch, and
// every ticket cites a story label of the merged PRD (R45, KTD24). Only
// the coordinator creates PRD-less work, and only when the operator asked
// for it (R48).
//
// The moves themselves are rows of the epic table in internal/machine.
// This package serves the four verbs that create items: prd submit, epic
// create, epic decompose and work create. It also serves epic adr, the
// coordinator's send of a PRD epic to the ADR stage.
package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/shape"
	"github.com/iantanwx/loop/internal/store"
)

// Config configures the pipeline.
type Config struct {
	Store   *store.Store
	Machine *machine.Machine
	// Beads records the blocking edges of a decomposition.
	Beads    machine.BeadsResolver
	GitHub   *github.Service
	Projects github.Projects
	// Redactor refuses a batch whose text holds a stored secret before any
	// write. An empty redactor when nil.
	Redactor *secrets.Redactor
	Logger   *slog.Logger
	// IntakeCap refuses a new PRD epic with CAP_FULL while the project's
	// epics in counted states fill the intake cap. Admission.IntakeCap is
	// one. Required.
	IntakeCap func(ctx context.Context, project string) *cli.Refusal
	// StageEnabled says whether the project's config lists a stage.
	// Required.
	StageEnabled func(ctx context.Context, project, stage string) (bool, error)
}

// Pipeline serves prd submit, epic create, epic adr, epic decompose and
// work create.
type Pipeline struct {
	cfg Config
	log *slog.Logger
	// mu serialises find-or-create on a create key, so two calls with one
	// key create one item.
	mu sync.Mutex
	// crash lets a test stop a call at a kill point, as a crash would.
	crash func(point string) bool
}

// New returns the pipeline.
func New(cfg Config) (*Pipeline, error) {
	if cfg.Store == nil || cfg.Machine == nil || cfg.Beads == nil || cfg.GitHub == nil || cfg.Projects == nil ||
		cfg.IntakeCap == nil || cfg.StageEnabled == nil {
		return nil, errors.New("pipeline: the pipeline needs a store, the machine, beads, GitHub, the projects, the intake cap and the stage list")
	}
	if cfg.Redactor == nil {
		cfg.Redactor = secrets.NewRedactor()
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Pipeline{cfg: cfg, log: log}, nil
}

// Handlers returns the handlers of the pipeline's verbs, keyed by path.
func (p *Pipeline) Handlers() map[string]rpc.Handler {
	return map[string]rpc.Handler{
		"prd submit":     p.prdSubmit,
		"epic decompose": p.decompose,
		"epic create":    p.epicCreate,
		"epic adr":       p.epicADR,
		"work create":    p.workCreate,
	}
}

// Register sets the handlers on the RPC server.
func (p *Pipeline) Register(srv *rpc.Server) error {
	for path, h := range p.Handlers() {
		if err := srv.Handle(path, h); err != nil {
			return fmt.Errorf("pipeline: %w", err)
		}
	}
	return nil
}

// errCrash is what a call stopped at a kill point answers.
var errCrash = errors.New("pipeline: stopped at a kill point")

func (p *Pipeline) stop(point string) bool { return p.crash != nil && p.crash(point) }

// prdLabels reads the story labels of an epic's merged PRD on the base
// branch. An epic whose PRD has not merged is PRD_REQUIRED.
func (p *Pipeline) prdLabels(ctx context.Context, epic machine.Item) ([]string, *cli.Refusal) {
	path := epic.Fact(machine.FactPRDPath)
	if epic.Kind != machine.KindEpic || path == "" || epic.Fact(machine.FactPRDMerged) == "" {
		return nil, &cli.Refusal{Code: cli.CodePRDRequired, Rule: "no_merged_prd", Item: epic.ID, CurrentStatus: epic.Status,
			Hint: fmt.Sprintf("%s has no merged PRD; tickets cite the story labels of a PRD the operator merged", epic.ID)}
	}
	data, _, err := p.cfg.GitHub.ReadAtBase(ctx, epic.Project, path)
	if err != nil {
		return nil, &cli.Refusal{Code: cli.CodePRDRequired, Rule: "prd_unreadable", Item: epic.ID,
			Hint: fmt.Sprintf("the base branch holds no readable %s yet: %v", path, err)}
	}
	return shape.StoryLabels(string(data)), nil
}

// citeCheck refuses stories that are empty or that name a label the PRD
// does not hold.
func citeCheck(name string, stories, labels []string) string {
	if len(stories) == 0 {
		return name + " cites no story label"
	}
	var unknown []string
	for _, s := range stories {
		if !slices.Contains(labels, s) {
			unknown = append(unknown, s)
		}
	}
	if len(unknown) > 0 {
		return fmt.Sprintf("%s cites %s, which the merged PRD does not hold (it holds %s)", name,
			strings.Join(unknown, ", "), strings.Join(labels, ", "))
	}
	return ""
}

// byKey finds the item that work entry created under a create key.
func (p *Pipeline) byKey(ctx context.Context, project string, kind machine.Kind, key string) (*machine.Item, *cli.Refusal) {
	items, ref := p.cfg.Machine.List(ctx, project, machine.ListFilter{Kind: kind, Facts: map[string]string{machine.FactCreateKey: key}})
	if ref != nil {
		return nil, ref
	}
	if len(items) == 0 {
		return nil, nil
	}
	slices.SortFunc(items, func(a, b machine.Item) int { return strings.Compare(a.ID, b.ID) })
	return &items[0], nil
}

// findOrCreate returns the item created under cr's create key, or makes
// its bead in Initial. A crash between the bead and its first move leaves
// the bead, and the next call finds it.
func (p *Pipeline) findOrCreate(ctx context.Context, cr machine.CreateRequest) (machine.Item, *cli.Refusal) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := cr.Facts[machine.FactCreateKey]
	it, ref := p.byKey(ctx, cr.Project, cr.Kind, key)
	if ref != nil || it != nil {
		if it == nil {
			return machine.Item{}, ref
		}
		return *it, nil
	}
	id, ref := p.cfg.Machine.CreateBead(ctx, cr)
	if ref != nil {
		return machine.Item{}, ref
	}
	return p.cfg.Machine.Read(ctx, cr.Project, id)
}

// ownProject is the hint of a run that names another project.
const ownProject = "a run works only in its own project"

func (p *Pipeline) registration(id string) (config.Project, *cli.Refusal) {
	prj, err := p.cfg.Projects.Show(id)
	if err != nil {
		return config.Project{}, &cli.Refusal{Code: cli.CodeNotFound, Rule: "no_project", Hint: fmt.Sprintf("project %s: %v", id, err)}
	}
	return prj, nil
}

// roomOf names the room whose session holds the run id.
func (p *Pipeline) roomOf(ctx context.Context, run string) (string, error) {
	var id string
	err := p.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT id FROM rooms WHERE run_id = ? AND kind = 'intake'`, run).Scan(&id)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("pipeline: no intake room holds session %s", run)
	}
	return id, err
}
