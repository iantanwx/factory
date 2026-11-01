// Package qa computes the checks of one QA run from the verify skills of a
// tree and the ticket the run holds.
package qa

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/sched"
)

// skillsDir holds each verify skill, as .claude/skills/verify-<app>/SKILL.md.
const (
	skillsDir    = ".claude/skills"
	verifyPrefix = "verify-"
)

var appName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// App is one verify skill. Name is the <app> of its directory verify-<app>,
// Skill the path of its SKILL.md, and Paths the globs of its front matter.
// An app with no Paths reaches every ticket.
type App struct {
	Name, Skill string
	Paths       []config.Glob
}

// MarshalJSON writes each glob as the skill spells it.
func (a App) MarshalJSON() ([]byte, error) {
	paths := []string{}
	for _, g := range a.Paths {
		paths = append(paths, g.String())
	}
	return json.Marshal(struct {
		Name  string   `json:"name"`
		Skill string   `json:"skill"`
		Paths []string `json:"paths"`
	}{a.Name, a.Skill, paths})
}

// Plan is the checks one QA run must make. Drive names each app to drive,
// and is empty exactly when the tree holds no verify skill. Visual is true
// exactly when DesignSkill is not "".
type Plan struct {
	Apps        []App    `json:"apps"`
	Drive       []string `json:"drive"`
	Visual      bool     `json:"visual"`
	DesignSkill string   `json:"design_skill"`
	Bounds      Bounds   `json:"bounds"`
}

// Apps reads each .claude/skills/verify-<app>/SKILL.md of tree, sorted by
// name. A bad name, a name two skills share, or bad paths is an error that
// names the path.
func Apps(tree fs.FS) ([]App, error) {
	dirs, err := fs.ReadDir(tree, skillsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var apps []App
	named := map[string]string{} // each front matter name to its path
	for _, d := range dirs {
		name, ok := strings.CutPrefix(d.Name(), verifyPrefix)
		if !ok || !d.IsDir() {
			continue
		}
		path := skillsDir + "/" + d.Name() + "/SKILL.md"
		text, err := fs.ReadFile(tree, path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !appName.MatchString(name) {
			return nil, fmt.Errorf("qa: %s: the app name %q must match [a-z0-9][a-z0-9-]*", path, name)
		}
		front, err := frontMatter(text)
		if err != nil {
			return nil, fmt.Errorf("qa: %s: %v", path, err)
		}
		if other, ok := named[front.Name]; ok {
			return nil, fmt.Errorf("qa: %s: the skill name %s is the name of %s too", path, front.Name, other)
		}
		if front.Name != "" {
			named[front.Name] = path
		}
		app := App{Name: name, Skill: path}
		for i, s := range front.Paths {
			g, err := config.ParseGlob(s)
			if err != nil {
				return nil, fmt.Errorf("qa: %s: paths[%d]: %v", path, i, err)
			}
			app.Paths = append(app.Paths, g)
		}
		apps = append(apps, app)
	}
	return apps, nil
}

type front struct {
	Name  string   `yaml:"name"`
	Paths []string `yaml:"paths"`
}

// frontMatter reads the name and the paths of the front matter that opens
// a SKILL.md.
func frontMatter(text []byte) (front, error) {
	rest, ok := strings.CutPrefix(strings.ReplaceAll(string(text), "\r\n", "\n"), "---\n")
	if !ok {
		return front{}, errors.New("the file opens with no front matter")
	}
	head, _, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return front{}, errors.New("the front matter does not close")
	}
	var f front
	if err := yaml.Unmarshal([]byte(head), &f); err != nil {
		return front{}, errors.New("the front matter must hold a name and a list of globs in paths")
	}
	return f, nil
}

// MakePlan picks the apps of one QA run: each app that reaches a "May
// change" entry of mayChange, or every app when none reaches one. Visual QA
// runs when the run holds designSkill.
func MakePlan(apps []App, mayChange []string, designSkill string) Plan {
	p := Plan{Apps: slices.Clone(apps), Drive: []string{}, Visual: designSkill != "", DesignSkill: designSkill,
		Bounds: DefaultBounds}
	if p.Apps == nil {
		p.Apps = []App{}
	}
	p.Bounds.Widths = slices.Clone(DefaultBounds.Widths)
	for _, a := range apps {
		if reaches(a, mayChange) {
			p.Drive = append(p.Drive, a.Name)
		}
	}
	if len(p.Drive) == 0 {
		for _, a := range apps {
			p.Drive = append(p.Drive, a.Name)
		}
	}
	return p
}

// reaches reports whether one of a's globs meets one entry, or a holds no
// glob.
func reaches(a App, mayChange []string) bool {
	if len(a.Paths) == 0 {
		return true
	}
	for _, g := range a.Paths {
		for _, e := range mayChange {
			if sched.Meets(g, e) {
				return true
			}
		}
	}
	return false
}
