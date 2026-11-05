package cli

import (
	"slices"
	"strings"
	"testing"
)

// coordinatorScopes is the scope of each coordinator verb, by the two scope
// lists of the factorio-1edo spec (ADR 0015 decision 4).
var coordinatorScopes = map[string]VerbScope{
	"work peek": ScopeProject, "work show": ScopeProject, "work list": ScopeProject,
	"work edit": ScopeProject, "work note": ScopeProject, "work block": ScopeProject,
	"work unblock": ScopeProject, "work cancel": ScopeProject, "work shape": ScopeProject,
	"work ask": ScopeProject, "work duplicate": ScopeProject, "work supersede": ScopeProject,
	"work create": ScopeProject,
	"run show":    ScopeProject, "run list": ScopeProject, "run events": ScopeProject,
	"pr pending": ScopeProject, "merge status": ScopeProject, "overwatch sweep": ScopeProject,
	"decision file": ScopeProject, "decision list": ScopeProject, "decision fire": ScopeProject,
	"decision defer": ScopeProject, "decision settle": ScopeProject, "decision moot": ScopeProject,
	"fault report": ScopeProject, "fault show": ScopeProject, "fault list": ScopeProject,
	"inbox list": ScopeProject, "inbox answer": ScopeProject, "inbox defer": ScopeProject,
	"inbox prepare": ScopeProject, "inbox wait": ScopeProject, "inbox note": ScopeProject,
	"room open": ScopeProject, "room close": ScopeProject, "room list": ScopeProject,
	"epic create": ScopeProject, "epic adr": ScopeProject, "epic skip-design": ScopeProject,
	"epic accept-difference": ScopeProject,
	"stage pause":            ScopeProject, "stage resume": ScopeProject, "stage list": ScopeProject,
	"settings show": ScopeProject, "settings set": ScopeProject,
	"project list": ScopeProject, "project show": ScopeProject,

	"fault ack": ScopeInstance, "fault resolve": ScopeInstance, "retro compared": ScopeInstance,
	"coordinator upgrade": ScopeInstance,
	"upgrade":             ScopeInstance, "rollback": ScopeInstance, "doctor": ScopeInstance,
	"doctor-github": ScopeInstance, "doctor-env": ScopeInstance,
}

func coordinatorCalls(v *Verb) bool {
	return slices.Contains(v.Callers, "coordinator") || slices.Contains(v.Callers, "host coordinator")
}

// C4: every verb of the surface whose callers include coordinator or host
// coordinator names the scope the spec gives it.
func TestC4EveryCoordinatorVerbNamesItsScope(t *testing.T) {
	r := Surface()
	seen := map[string]bool{}
	for _, v := range r.Verbs() {
		if !coordinatorCalls(v) {
			continue
		}
		seen[v.Path] = true
		want, listed := coordinatorScopes[v.Path]
		if !listed {
			t.Errorf("%s is a coordinator verb that the scope lists leave out", v.Path)
			continue
		}
		if v.Scope != want {
			t.Errorf("%s names the scope %q, want %q", v.Path, v.Scope, want)
		}
	}
	for path := range coordinatorScopes {
		if !seen[path] {
			t.Errorf("%s is in the scope lists, but no coordinator calls it", path)
		}
	}
}

// C4: DeriveMatrix refuses a registry with one coordinator verb that names
// no scope, and the error names the verb.
func TestC4DeriveMatrixRefusesACoordinatorVerbWithNoScope(t *testing.T) {
	for _, callers := range [][]string{{"coordinator"}, {"host", "host coordinator"}} {
		r := NewRegistry()
		if err := r.RegisterVerb(Verb{Noun: "work", Name: "fly", Summary: "s", Side: SideClient, Callers: callers}); err != nil {
			t.Fatal(err)
		}
		_, err := DeriveMatrix(r)
		if err == nil {
			t.Fatalf("callers %v: a coordinator verb with no scope must be refused", callers)
		}
		if !strings.Contains(err.Error(), `"work fly"`) {
			t.Errorf("callers %v: the error %q does not name the verb", callers, err)
		}
	}
	for _, scope := range []VerbScope{ScopeProject, ScopeInstance} {
		r := NewRegistry()
		if err := r.RegisterVerb(Verb{Noun: "work", Name: "fly", Summary: "s", Side: SideClient, Callers: []string{"coordinator"}, Scope: scope}); err != nil {
			t.Fatal(err)
		}
		if _, err := DeriveMatrix(r); err != nil {
			t.Errorf("scope %q: %v", scope, err)
		}
	}
	r := NewRegistry()
	if err := r.RegisterVerb(Verb{Noun: "work", Name: "fly", Summary: "s", Side: SideClient, Callers: []string{"coordinator"}, Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveMatrix(r); err == nil {
		t.Error("a scope other than project or instance must be refused")
	}
}

// C4: `loop schema` and `--help` print each coordinator verb's scope, and a
// verb no coordinator calls prints none.
func TestC4SchemaPrintsTheScope(t *testing.T) {
	r := New()
	env, code := runJSON(t, r, "schema")
	if code != 0 {
		t.Fatalf("schema exited %d: %v", code, env)
	}
	scopes := map[any]any{}
	for _, v := range env["data"].(map[string]any)["verbs"].([]any) {
		v := v.(map[string]any)
		scopes[v["path"]] = v["scope"]
	}
	for path, want := range map[string]any{"work show": "project", "fault ack": "instance", "upgrade": "instance", "run finish": nil} {
		if got := scopes[path]; got != want {
			t.Errorf("schema prints the scope of %s as %v, want %v", path, got, want)
		}
	}
	help, code := runJSON(t, r, "settings", "set", "--help")
	if code != 0 {
		t.Fatalf("settings set --help exited %d: %v", code, help)
	}
	if got := help["data"].(map[string]any)["scope"]; got != "project" {
		t.Errorf("settings set --help prints the scope %v, want project", got)
	}
}
