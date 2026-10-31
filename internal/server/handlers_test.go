package server

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/userspace"
)

// exemption is one server verb that may have no handler yet, and the epic
// that owns it. The epic's work deletes the exemption with the handler.
type exemption struct {
	verb, epic string
}

// handlerExemptions lists every server verb with no handler, in path order.
var handlerExemptions = []exemption{}

// inventoryFaults compares the unhandled server verbs with the exemptions.
// It names each unhandled verb with no exemption, each exempt verb with a
// handler, and each exemption that names no epic.
func inventoryFaults(unhandled []string, exempt []exemption) []string {
	var faults []string
	for _, v := range unhandled {
		if !slices.ContainsFunc(exempt, func(e exemption) bool { return e.verb == v }) {
			faults = append(faults, fmt.Sprintf("%s has no handler and no exemption", v))
		}
	}
	for _, e := range exempt {
		if !slices.Contains(unhandled, e.verb) {
			faults = append(faults, fmt.Sprintf("%s has a handler: delete its exemption", e.verb))
		}
		if e.epic == "" {
			faults = append(faults, fmt.Sprintf("the exemption of %s names no epic", e.verb))
		}
	}
	return faults
}

// C1: each server verb of the server with beads, the scheduler and the
// GitHub service, its merge interface and its pipeline, has a handler, or
// an exemption that names its epic.
func TestEachServerVerbHasAHandlerOrANamedExemption(t *testing.T) {
	w, gh, _ := newDocWorld(t)
	if err := (&Merge{GitHub: gh}).Setup(w.srv); err != nil {
		t.Fatal(err)
	}
	got := w.srv.RPC.Unhandled()
	if faults := inventoryFaults(got, handlerExemptions); len(faults) > 0 {
		t.Fatalf("handler inventory: %q", faults)
	}
	want := make([]string, len(handlerExemptions))
	for i, e := range handlerExemptions {
		want[i] = e.verb
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Unhandled() = %q, want the exemptions %q", got, want)
	}
}

// C1: the inventory fails in each of its three cases, and only in those.
func TestInventoryFaultsNameEachBrokenExemption(t *testing.T) {
	exempt := []exemption{{"run show", "factorio-wd0"}}
	for _, tc := range []struct {
		name      string
		unhandled []string
		exempt    []exemption
		want      []string
	}{
		{"each unhandled verb is exempt", []string{"run show"}, exempt, nil},
		{"a new server verb has no handler and no exemption", []string{"run show", "work teleport"}, exempt,
			[]string{"work teleport has no handler and no exemption"}},
		{"an exempt verb has a handler", nil, exempt,
			[]string{"run show has a handler: delete its exemption"}},
		{"an exemption names no epic", []string{"run show"}, []exemption{{"run show", ""}},
			[]string{"the exemption of run show names no epic"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inventoryFaults(tc.unhandled, tc.exempt); !slices.Equal(got, tc.want) {
				t.Fatalf("faults = %q, want %q", got, tc.want)
			}
		})
	}
}

// C18: project show lists the userspace skills. Each pin of the last good
// config carries its fields and held, which the store answers. Each
// directory outside loop- under either skill root at the default branch is
// listed with its root. A project that declares nothing returns two empty
// lists.
func TestC18ProjectShowListsTheUserspaceSkills(t *testing.T) {
	tree := fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: neon\n---\n"), Mode: 0o644}}
	sum, err := pack.TreeHash(tree)
	if err != nil {
		t.Fatal(err)
	}
	pin := config.SkillPin{Name: "neon", Repo: "neondatabase/agent-skills",
		Commit: "ccca3334a7542d3133134cc6926f2fafc6edeb1f", Path: "skills/neon", SHA256: sum}
	pinned := "merge: {mechanism: direct}\nskills:\n  - name: neon\n    repo: " + pin.Repo +
		"\n    commit: " + pin.Commit + "\n    path: " + pin.Path + "\n    sha256: " + pin.SHA256 + "\n"
	committed := map[string][]config.TreeEntry{
		".claude/skills": {{Name: "house-style", Dir: true}, {Name: "loop-implementer", Dir: true}, {Name: "README.md"}},
		".agents/skills": {{Name: "house-style", Dir: true}, {Name: "loop-asd-ste100", Dir: true}},
	}
	for _, tc := range []struct {
		name   string
		lkg    string
		stored bool
		tree   map[string][]config.TreeEntry
		want   string
	}{
		{"held", pinned, true, committed, `{"pins":[{"name":"neon","repo":"neondatabase/agent-skills",` +
			`"commit":"ccca3334a7542d3133134cc6926f2fafc6edeb1f","path":"skills/neon","sha256":"` + sum + `","held":true}],` +
			`"committed":[{"root":".claude/skills","name":"house-style"},{"root":".agents/skills","name":"house-style"}]}`},
		{"not held", pinned, false, committed, `{"pins":[{"name":"neon","repo":"neondatabase/agent-skills",` +
			`"commit":"ccca3334a7542d3133134cc6926f2fafc6edeb1f","path":"skills/neon","sha256":"` + sum + `","held":false}],` +
			`"committed":[{"root":".claude/skills","name":"house-style"},{"root":".agents/skills","name":"house-style"}]}`},
		{"nothing declared", "", false, nil, `{"pins":[],"committed":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := instanceDir(t)
			if _, err := rpc.EnsureAdminCredential(dir); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveHost(filepath.Join(dir, config.HostConfigFile), &config.HostConfig{StateDir: dir, Throughput: config.DefaultThroughput()}); err != nil {
				t.Fatal(err)
			}
			st := userspace.Store{Dir: filepath.Join(t.TempDir(), "userspace-skills")}
			if tc.stored {
				if err := st.Put("alpha", pin, tree); err != nil {
					t.Fatal(err)
				}
			}
			// The fake tree reader serves the entries at origin/trunk only.
			refs := func(_ context.Context, p config.Project) (config.Refs, string, error) {
				return config.Refs{Tree: func(_ context.Context, ref, dir string) ([]config.TreeEntry, error) {
					if ref != config.DefaultRef("trunk") {
						return nil, nil
					}
					return tc.tree[dir], nil
				}}, p.BaseBranch(), nil
			}
			var srv *Server
			start(t, dir, Options{Sched: SchedOptions{Refs: refs, SkillStore: st},
				Setup: []func(*Server) error{func(s *Server) error { srv = s; return nil }}}, true)
			ctx := context.Background()
			if env, code, _ := call(t, dir, time.Second, nil, "project", "add", "alpha", "--repo", "iantanwx/alpha", "--path", "/srv/alpha", "--branch", "trunk"); code != 0 {
				t.Fatalf("project add = %d %v", code, env)
			}
			if tc.lkg != "" {
				if err := config.StoreLKG(srv.Store).SaveLKG(ctx, "alpha", []byte(tc.lkg), nil); err != nil {
					t.Fatal(err)
				}
			}
			env, code, raw := call(t, dir, time.Second, nil, "project", "show", "alpha")
			d, _ := env["data"].(map[string]any)
			if code != 0 || d["id"] != "alpha" {
				t.Fatalf("project show = %d %s", code, raw)
			}
			var want any
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(d["user_skills"], want) {
				t.Fatalf("user_skills = %v, want %s", d["user_skills"], tc.want)
			}
		})
	}
}
