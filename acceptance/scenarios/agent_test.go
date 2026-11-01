//go:build acceptance

package scenarios

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/iantanwx/loop/acceptance"
)

// agent is a scripted agent that knows only what a run finds in its
// checkout: .loop/brief.json, and the schema that `loop schema --json`
// prints. It runs every command as a separate process with the world's
// CLI environment, which holds no GitHub, harness or push credential.
// Before each loop command it checks the verb, each flag and each enum
// value against the schema, so a command the schema does not name fails
// the scenario.
type agent struct {
	w     *acceptance.World
	dir   string
	brief agentBrief
	verbs map[string]schemaVerb
}

// agentBrief is the slice of .loop/brief.json the agent reads.
type agentBrief struct {
	RunID       string   `json:"run_id"`
	Project     string   `json:"project"`
	Stage       string   `json:"stage"`
	Runbook     string   `json:"runbook"`
	BaseBranch  string   `json:"base_branch"`
	AllowedNext []string `json:"allowed_next"`
	Item        *struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Title  string `json:"title"`
		Status string `json:"status"`
		Body   string `json:"body"`
	} `json:"item"`
}

type schemaVerb struct {
	Path string `json:"path"`
	Args []struct {
		Name     string `json:"name"`
		Required bool   `json:"required"`
	} `json:"args"`
	Flags []schemaFlag `json:"flags"`
}

type schemaFlag struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Enum     []string `json:"enum"`
}

// startAgent reads the brief and the schema in dir.
func startAgent(w *acceptance.World, dir string) *agent {
	w.T.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".loop", "brief.json"))
	if err != nil {
		w.T.Fatalf("the agent finds no brief: %v", err)
	}
	a := &agent{w: w, dir: dir, verbs: map[string]schemaVerb{}}
	if err := json.Unmarshal(raw, &a.brief); err != nil {
		w.T.Fatalf("the brief is no JSON object: %v", err)
	}
	r := w.Must(w.In(dir, "schema", "--json"))
	var s struct {
		Verbs []schemaVerb `json:"verbs"`
	}
	b, _ := json.Marshal(r.Data())
	if err := json.Unmarshal(b, &s); err != nil || len(s.Verbs) == 0 {
		w.T.Fatalf("the schema lists no verbs: %v", err)
	}
	for _, v := range s.Verbs {
		a.verbs[v.Path] = v
	}
	return a
}

// item is the brief's item id. A run with no item fails the scenario.
func (a *agent) item() string {
	if a.brief.Item == nil {
		a.w.T.Fatalf("the brief of %s names no item", a.brief.RunID)
	}
	return a.brief.Item.ID
}

// loop checks one command against the schema and runs it.
func (a *agent) loop(args ...string) acceptance.Result {
	a.w.T.Helper()
	v, rest := a.verb(args)
	var positional int
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		if !strings.HasPrefix(tok, "--") {
			positional++
			continue
		}
		name, _, inline := strings.Cut(strings.TrimPrefix(tok, "--"), "=")
		j := slices.IndexFunc(v.Flags, func(f schemaFlag) bool { return f.Name == name })
		if j < 0 {
			if name == "json" || name == "text" {
				continue
			}
			a.w.T.Fatalf("the schema gives %q no flag --%s", v.Path, name)
		}
		f := v.Flags[j]
		if f.Type == "bool" || inline {
			continue
		}
		i++
		if i >= len(rest) {
			a.w.T.Fatalf("--%s of %q has no value", name, v.Path)
		}
		if len(f.Enum) > 0 && !slices.Contains(f.Enum, rest[i]) {
			a.w.T.Fatalf("the schema allows %v for --%s of %q, not %q", f.Enum, name, v.Path, rest[i])
		}
	}
	for _, f := range v.Flags {
		if f.Required && !slices.ContainsFunc(rest, func(t string) bool { return t == "--"+f.Name || strings.HasPrefix(t, "--"+f.Name+"=") }) {
			a.w.T.Fatalf("%q needs --%s", v.Path, f.Name)
		}
	}
	if want := len(v.Args); positional != want {
		a.w.T.Fatalf("%q takes %d argument(s), and the agent passed %d", v.Path, want, positional)
	}
	return a.w.In(a.dir, args...)
}

// verb finds the longest schema path that args start with.
func (a *agent) verb(args []string) (schemaVerb, []string) {
	a.w.T.Helper()
	for n := min(2, len(args)); n > 0; n-- {
		if v, ok := a.verbs[strings.Join(args[:n], " ")]; ok {
			return v, args[n:]
		}
	}
	a.w.T.Fatalf("the schema names no verb for %v", args)
	return schemaVerb{}, nil
}

// git runs git in the checkout with the agent's environment.
func (a *agent) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = a.dir
	cmd.Env = append(a.w.CLIEnv(), "GIT_AUTHOR_NAME=agent", "GIT_AUTHOR_EMAIL=agent@example.invalid",
		"GIT_COMMITTER_NAME=agent", "GIT_COMMITTER_EMAIL=agent@example.invalid")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (a *agent) mustGit(args ...string) string {
	a.w.T.Helper()
	out, err := a.git(args...)
	if err != nil {
		a.w.T.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

var codeSpan = regexp.MustCompile("`([^`]+)`")

// implement does the ticket the brief names: its body names a file and
// the line it holds, each in a code span. It commits, proves that it
// cannot push by itself, pushes and opens the PR through loop, and
// finishes.
func (a *agent) implement() string {
	a.w.T.Helper()
	spans := codeSpan.FindAllStringSubmatch(a.brief.Item.Body, -1)
	if len(spans) < 2 {
		a.w.T.Fatalf("the ticket body names no file and line: %q", a.brief.Item.Body)
	}
	path, line := spans[0][1], spans[1][1]
	if err := os.WriteFile(filepath.Join(a.dir, path), []byte(line+"\n"), 0o644); err != nil {
		a.w.T.Fatal(err)
	}
	a.mustGit("add", path)
	a.mustGit("commit", "-q", "-m", a.brief.Item.Title)
	head := a.mustGit("rev-parse", "HEAD")
	if out, err := a.git("push", "origin", "HEAD:refs/heads/agent-push"); err == nil {
		a.w.T.Fatalf("the agent pushed with no credential: %s", out)
	}
	if r := a.loop("branch", "push"); r.Code != 0 || r.Str("head") != head {
		a.w.T.Fatalf("%s", r)
	}
	a.w.Must(a.loop("pr", "open", "--title", a.brief.Item.Title, "--body", "Implements "+a.item()+"."))
	a.w.Must(a.loop("run", "finish", "--outcome", "done"))
	passSimplify(a.w, a.item())
	return head
}

// review reads the item's PR and submits verdict at the head it read.
func (a *agent) review(verdict string) string {
	a.w.T.Helper()
	head := a.w.Must(a.loop("pr", "show")).Str("head_sha")
	a.w.Must(a.loop("pr", "diff"))
	a.w.Must(a.loop("review", "submit", a.item(), "--verdict", verdict, "--head", head, "--body", "Read the diff at "+head[:12]+"."))
	a.w.Must(a.loop("run", "finish", "--outcome", "done"))
	return head
}
