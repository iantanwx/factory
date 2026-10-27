// Package settings is a project's settings in force: the read of
// `loop settings show`.
package settings

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gitx"
)

// Row is one setting in force.
type Row struct {
	Key    string        `json:"key"`
	Value  string        `json:"value"`
	Source config.Source `json:"source"`
	Commit string        `json:"commit,omitempty"` // project rows only
}

// View is the answer of `loop settings show`.
type View struct {
	Project string `json:"project"`
	Branch  string `json:"branch"`
	Head    string `json:"head"`
	Stale   bool   `json:"stale"`
	Problem string `json:"problem,omitempty"`
	Rows    []Row  `json:"settings"`
}

// Repo is one project's default branch after a fetch, in a git directory
// that holds its history.
type Repo struct {
	Dir    string
	Branch string
	Head   string
}

// Service reads a project's settings in force.
type Service struct {
	Host   func(ctx context.Context) (*config.HostConfig, error)
	Loader *config.Loader
	// Defaults gives the loop's own defaults for one project config. Nil
	// lists no default value.
	Defaults func(*config.ProjectConfig) config.Defaults
	// Repo fetches the project's default branch.
	Repo func(ctx context.Context, project string) (Repo, error)
}

// Show lists every setting of project in force, with its value and source.
// A project row names the newest commit that changed its value. When the
// head does not load, the view is stale and lists the last good copy.
func (s *Service) Show(ctx context.Context, project string) (View, *cli.Refusal) {
	repo, err := s.Repo(ctx, project)
	if err != nil {
		return View{}, cli.InternalRefusal(fmt.Errorf("settings: fetch the default branch of %s: %w", project, err))
	}
	hc, err := s.Host(ctx)
	if err != nil {
		return View{}, cli.InternalRefusal(fmt.Errorf("settings: load host config: %w", err))
	}
	v := View{Project: project, Branch: repo.Branch, Head: repo.Head}
	var pc *config.ProjectConfig
	var raw []byte
	l, err := s.Loader.Load(ctx, project, repo.read, repo.Branch)
	switch {
	case err == nil:
		pc, raw = l.Config, l.ConfigRaw
		if l.Stale {
			v.Stale, v.Problem = true, l.Problem.Error()
		}
	case !errors.Is(err, config.ErrConfigMissing):
		return View{}, cli.InternalRefusal(err)
	}
	var d config.Defaults
	if s.Defaults != nil {
		d = s.Defaults(pc)
	}
	v.Rows = rows(hc, pc, d)
	if pc == nil {
		return v, nil
	}
	if err := v.commits(ctx, repo, raw, hc); err != nil {
		return View{}, cli.InternalRefusal(err)
	}
	return v, nil
}

// rows lists every setting of pc over hc, sorted by key.
func rows(hc *config.HostConfig, pc *config.ProjectConfig, d config.Defaults) []Row {
	var out []Row
	for _, st := range config.Effective(hc, pc).Settings(config.KnownStages(hc), d) {
		out = append(out, Row{Key: st.Key.String(), Value: st.Value, Source: st.Source})
	}
	return out
}

// projectValues reads the project rows of one copy of the project file. A
// copy that does not parse holds none.
func projectValues(hc *config.HostConfig, raw []byte) map[string]string {
	out := map[string]string{}
	if raw == nil {
		return out
	}
	pc, err := config.ParseProjectConfig(raw)
	if err != nil {
		return out
	}
	for _, r := range rows(hc, pc, nil) {
		if r.Source == config.SourceProject {
			out[r.Key] = r.Value
		}
	}
	return out
}

// commits names the commit of each project row, from the history of the
// copy in force, raw.
func (v *View) commits(ctx context.Context, repo Repo, raw []byte, hc *config.HostConfig) error {
	want := map[string]string{}
	for _, r := range v.Rows {
		if r.Source == config.SourceProject {
			want[r.Key] = r.Value
		}
	}
	h, err := readHistory(ctx, repo.Dir, repo.Head)
	if err != nil {
		return err
	}
	if err := h.from(ctx, raw); err != nil {
		return err
	}
	commits, err := h.commits(ctx, want, func(b []byte) map[string]string { return projectValues(hc, b) })
	if err != nil {
		return err
	}
	for i := range v.Rows {
		v.Rows[i].Commit = commits[v.Rows[i].Key]
	}
	return nil
}

// read is the config.RefReader of the fetched head. It reads every path
// at Head, whatever ref the loader names, so one read sees one commit.
func (r Repo) read(ctx context.Context, _, path string) ([]byte, error) {
	spec := r.Head + ":" + path
	if _, err := gitx.Run(ctx, r.Dir, "cat-file", "-e", spec); err != nil {
		if _, rerr := gitx.Run(ctx, r.Dir, "rev-parse", "--verify", "--quiet", r.Head+"^{commit}"); rerr == nil {
			return nil, fmt.Errorf("%s holds no %s: %w", r.Head, path, fs.ErrNotExist)
		}
		return nil, err
	}
	return gitx.Run(ctx, r.Dir, "cat-file", "blob", spec)
}
