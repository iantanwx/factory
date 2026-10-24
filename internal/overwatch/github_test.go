package overwatch

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// The two projects of the GitHub parts' fixtures. alpha's repository is
// the one githubtest serves. GitHub answers no read of beta's, so every
// GitHub read of beta fails.
var (
	alpha = config.Project{ID: "alpha", Repo: gt.Repo}
	beta  = config.Project{ID: "beta", Repo: "acme/gadgets"}
)

// ghWorld is fakeWorld with a GitHub service over githubtest, and the
// project configs a test sets.
type ghWorld struct {
	*fakeWorld
	api     *gt.API
	gh      GitHubReads
	configs map[string]*config.ProjectSnapshot
}

func (w *ghWorld) GitHub() GitHubReads { return w.gh }

func (w *ghWorld) ProjectConfig(_ context.Context, project string) (*config.ProjectSnapshot, error) {
	s, ok := w.configs[project]
	if !ok {
		return nil, fmt.Errorf("no config of %s: %w", project, errFake)
	}
	return s, nil
}

// prs is githubtest's PR world.
func (w *ghWorld) prs() *gt.World { return w.api.World }

// auditWorkflow sets a project's health.audit_workflow.
func (w *ghWorld) auditWorkflow(project, file string) {
	w.configs[project] = &config.ProjectSnapshot{Config: &config.ProjectConfig{Health: config.HealthConfig{AuditWorkflow: file}}}
}

type projectList []config.Project

func (l projectList) List() ([]config.Project, error) { return l, nil }
func (l projectList) Show(id string) (config.Project, error) {
	for _, p := range l {
		if p.ID == id {
			return p, nil
		}
	}
	return config.Project{}, config.ErrProjectNotFound
}

// serviceReads is the GitHub reads of a github.Service, as the server's
// World serves them.
type serviceReads struct{ svc *github.Service }

func (r serviceReads) OpenPulls(ctx context.Context, p config.Project) ([]github.PR, error) {
	return r.svc.OpenPulls(ctx, p.ID)
}

func (r serviceReads) WorkflowRuns(ctx context.Context, p config.Project, q github.RunQuery) ([]github.RunView, error) {
	return r.svc.WorkflowRuns(ctx, p, q)
}

func (r serviceReads) RunJobs(ctx context.Context, p config.Project, runID int64, attempt int) ([]github.JobView, error) {
	return r.svc.RunJobs(ctx, p, runID, attempt)
}

func newGHWorld(t *testing.T, projects ...config.Project) *ghWorld {
	t.Helper()
	fw := newFakeWorld(t)
	fw.projects = projects
	api := gt.NewAPI(t)
	svc, err := github.New(github.Config{
		Apps: github.OneApp(gt.AppID, func(context.Context) ([]byte, error) { return gt.KeyPEM(), nil }),
		API:  api.URL(), Store: fw.st, Projects: projectList(projects), MirrorRoot: t.TempDir(),
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &ghWorld{fakeWorld: fw, api: api, gh: serviceReads{svc}, configs: map[string]*config.ProjectSnapshot{}}
}

// sweepOne sweeps one part and returns its report.
func sweepOne(t *testing.T, w World, p Part) PartReport {
	t.Helper()
	r := Sweep(context.Background(), w, []Part{p})
	if r.Records != "" {
		t.Fatalf("the records read failed: %s", r.Records)
	}
	if len(r.Parts) != 1 {
		t.Fatalf("report = %+v", r)
	}
	return r.Parts[0]
}

// partNamed is the shipped part named name.
func partNamed(t *testing.T, name string) Part {
	t.Helper()
	i := slices.IndexFunc(Parts(), func(p Part) bool { return p.Name == name })
	if i < 0 {
		t.Fatalf("Parts() holds no part %s", name)
	}
	return Parts()[i]
}

// expect checks a part's code, and that some line holds each of want.
func expect(t *testing.T, pr PartReport, code Code, want ...string) {
	t.Helper()
	if pr.Code != code {
		t.Fatalf("%s: code %d, want %d; lines:\n%s", pr.Name, pr.Code, code, strings.Join(pr.Lines, "\n"))
	}
	for _, s := range want {
		if !slices.ContainsFunc(pr.Lines, func(l string) bool { return strings.Contains(l, s) }) {
			t.Fatalf("%s: no line holds %q; lines:\n%s", pr.Name, s, strings.Join(pr.Lines, "\n"))
		}
	}
}

// unfiled counts the lines of pr that quote key with no record.
func unfiled(pr PartReport, key string) int {
	n := 0
	for _, l := range pr.Lines {
		if slices.Contains(Tokens(l), key) && !strings.Contains(l, "(filed in ") {
			n++
		}
	}
	return n
}

// C26, for each GitHub part of this entry: a GitHub read of one project
// that fails costs that project's lines only. They read UNREAD, and the
// part has code 2. The other project's lines in the part, and a store
// part's reading, stay as a sweep with no failure reads them.
func TestC26AFailedGitHubReadCostsItsProjectsPart(t *testing.T) {
	cases := []struct {
		part  string
		setup func(w *ghWorld)
		key   string // alpha's finding
		names string // what beta's UNREAD line names
	}{
		{"pulls", func(w *ghWorld) {
			w.prs().Add(&gt.PR{Head: "loop/alpha-x1", Base: "main"})
			w.prs().Add(&gt.PR{Head: "loop/alpha-x1", Base: "release"})
		}, "overwatch:pulls:alpha:alpha-x1:work", "beta"},
		{"main", func(w *ghWorld) {
			w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Conclusion: "failure", CreatedAt: w.now.Add(-time.Hour)})
		}, "overwatch:main:alpha:ci-yml@" + headA[:12], "beta"},
		{"audit", func(w *ghWorld) {
			w.auditWorkflow("alpha", "audit.yml")
			w.auditWorkflow("beta", "audit.yml")
			w.prs().AddRun(&gt.Run{ID: 9101, Workflow: "audit.yml", Event: "schedule", HeadSHA: headA, Conclusion: "failure",
				CreatedAt: w.now.Add(-time.Hour)})
		}, "overwatch:audit:alpha:audit-yml@9101", "audit.yml"},
		{"ci", func(w *ghWorld) {
			w.ciBudget("alpha", 5*time.Minute)
			w.ciBudget("beta", 5*time.Minute)
			w.prs().AddRun(&gt.Run{Workflow: "ci.yml", HeadSHA: headA, Conclusion: "success", Took: 9 * time.Minute, CreatedAt: w.now.Add(-time.Hour)})
		}, "overwatch:ci:alpha:push", "beta"},
		{"flaky", func(w *ghWorld) {
			w.prs().AddRun(rerun(headA, "failure", "success", w.now.Add(-time.Hour)))
		}, "overwatch:flaky:alpha:ci-yml/go-tests-race", "beta"},
	}
	store := readingPart("claims", Reading{Notes: []string{"no lease is held"}})
	for _, c := range cases {
		t.Run(c.part, func(t *testing.T) {
			ok := newGHWorld(t, alpha)
			c.setup(ok)
			want := Sweep(context.Background(), ok, []Part{partNamed(t, c.part), store})

			w := newGHWorld(t, alpha, beta)
			c.setup(w)
			got := Sweep(context.Background(), w, []Part{partNamed(t, c.part), store})
			if got.Code != Unread || got.Records != "" {
				t.Fatalf("report code %d, records %q; want 2 from the part alone", got.Code, got.Records)
			}
			var part, quiet PartReport
			for _, pr := range got.Parts {
				if pr.Name == c.part {
					part = pr
				} else {
					quiet = pr
				}
			}
			expect(t, part, Unread, "UNREAD", c.names)
			for _, l := range part.Lines {
				if strings.HasPrefix(l, "UNREAD") && !strings.Contains(l, "beta") {
					t.Fatalf("an UNREAD line does not name beta: %q", l)
				}
				if strings.Contains(l, "alpha") && strings.HasPrefix(l, "UNREAD") {
					t.Fatalf("alpha's read worked, and its line reads %q", l)
				}
			}
			if unfiled(part, c.key) != 1 {
				t.Fatalf("alpha's finding %s is lost: %v", c.key, part.Lines)
			}
			for _, l := range want.Parts[slices.IndexFunc(want.Parts, func(p PartReport) bool { return p.Name == c.part })].Lines {
				if !slices.Contains(part.Lines, l) {
					t.Fatalf("alpha's line %q is lost: %v", l, part.Lines)
				}
			}
			if quiet.Name != "claims" || quiet.Code != Quiet || len(quiet.Lines) != 1 || quiet.Lines[0] != "no lease is held" {
				t.Fatalf("the store part = %+v", quiet)
			}
		})
	}
}
