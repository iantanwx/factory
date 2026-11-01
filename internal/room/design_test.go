package room

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/userspace"
)

// C25 of spec factorio-a4yq: a design room opens only on an epic in
// design. On an epic in any other state the open refuses with rule
// not_in_design, and opens no room.
func TestC25DesignRoomRefusesAnEpicOutsideDesign(t *testing.T) {
	w := newSkillWorld(t, uiConfig)
	for _, status := range []string{machine.Architecture, machine.DesignReview, machine.SpecNeeded} {
		ep := w.item("epic", status, "")
		res := w.s.open(context.Background(), &rpc.Call{Principal: rpc.Principal{Kind: cli.PrincipalCoordinator, RunID: "coord-1"},
			Inv: cli.Invocation{Flags: map[string]string{"kind": "design", "project": "alpha", "item": ep, "reason": "design it", "score": "4"}}})
		if r := res.Refusal; r == nil || r.Code != cli.CodeTransitionNotAllowed || r.Rule != "not_in_design" {
			t.Fatalf("a design room on an epic in %s = %+v, want TRANSITION_NOT_ALLOWED not_in_design", status, res)
		}
	}
	rooms, err := w.s.Rooms(context.Background(), true)
	if err != nil || len(rooms) != 0 {
		t.Fatalf("rooms = %+v %v, want none", rooms, err)
	}
}

// C27 of spec factorio-a4yq: a design room of a project whose design.skill
// is the pin zz-stub-canvas gets a brief whose design object names the
// block's run, capture and skill, and its checkout holds that pin.
func TestC27DesignRoomBriefNamesTheCanvasSkillAndItsCheckoutHoldsThePin(t *testing.T) {
	stub := fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: zz-stub-canvas\n---\n"), Mode: 0o644}}
	sum, err := pack.TreeHash(stub)
	if err != nil {
		t.Fatal(err)
	}
	pin := config.SkillPin{Name: "zz-stub-canvas", Repo: "acme/skills", Commit: "0123456789abcdef0123456789abcdef01234567", SHA256: sum}
	w := newSkillWorld(t, uiConfig+"design:\n  run: make serve\n  capture: make shot\n  skill: zz-stub-canvas\n"+
		"skills:\n  - name: zz-stub-canvas\n    repo: "+pin.Repo+"\n    commit: "+pin.Commit+"\n    path: \"\"\n    sha256: "+pin.SHA256+"\n")
	w.s.cfg.SkillStore = userspace.Store{Dir: filepath.Join(t.TempDir(), "userspace-skills")}
	if err := w.s.cfg.SkillStore.Put("alpha", pin, stub); err != nil {
		t.Fatal(err)
	}
	co := w.open("design", w.item("epic", machine.Design, ""))
	raw, err := os.ReadFile(filepath.Join(co, brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Stage  string            `json:"stage"`
		Design map[string]string `json:"design"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"run": "make serve", "capture": "make shot", "skill": "zz-stub-canvas"}
	if got.Stage != "design-room" || !maps.Equal(got.Design, want) {
		t.Fatalf("brief = %s; want stage design-room and the design object %v", raw, want)
	}
	if _, err := os.Stat(filepath.Join(co, brief.SkillRoot(agenthost.Claude), "zz-stub-canvas", "SKILL.md")); err != nil {
		t.Fatalf("the design room's checkout holds no zz-stub-canvas pin: %v", err)
	}
}

// A room of any other kind gets no design object, even in a project with
// a design block.
func TestOnlyADesignRoomBriefNamesTheDesignBlock(t *testing.T) {
	w := newSkillWorld(t, uiConfig+"design:\n  run: make serve\n  capture: make shot\n")
	co := w.open("architect", w.item("epic", machine.Architecture, ""))
	raw, err := os.ReadFile(filepath.Join(co, brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["design"]; ok {
		t.Fatalf("an architect room's brief = %s; want no design object", raw)
	}
}
