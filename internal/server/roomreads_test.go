package server

import (
	"context"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// An intake room reads with work show the epic its PRD submit created,
// and each epic that its reason names.
func TestIntakeRoomReadsTheEpicsItCreatedAndItsReasonNames(t *testing.T) {
	w, _, _ := newDocWorld(t)
	ctx := context.Background()
	named, res := w.srv.Machine.Create(ctx, machine.CreateRequest{Project: "alpha", Kind: machine.KindEpic, Title: "named",
		Description: "The named epic's body.", Verb: "create_architecture", Actor: machine.Server("test")})
	if res.Refusal != nil {
		t.Fatalf("create epic: %+v", *res.Refusal)
	}
	r := w.openRoom("intake", "", "split "+named+" into two epics")
	co := r["checkout"].(string)
	gt.Commit(t, co, "prd", map[string]string{"docs/prd/widgets.md": "# Widgets\n\n## US-01 See widgets\n"})
	env, code := w.as(co, "prd", "submit", "--path", "docs/prd/widgets.md", "--title", "Widgets")
	if code != 0 {
		t.Fatalf("prd submit = %d %v", code, env)
	}
	created := env["data"].(map[string]any)["epic"].(string)

	for _, id := range []string{created, named} {
		if env, code := w.as(co, "work", "show", id, "--project", "alpha"); code != 0 || env["ok"] != true {
			t.Errorf("the room's work show %s = %d %v", id, code, env)
		}
	}
}

// A room reads no item that it neither created nor names, and its write
// on a named item stays refused.
func TestRoomReadsNoOtherItemAndWritesNone(t *testing.T) {
	w, _, _ := newDocWorld(t)
	ctx := context.Background()
	var ids []string
	for _, title := range []string{"named", "other"} {
		id, res := w.srv.Machine.Create(ctx, machine.CreateRequest{Project: "alpha", Kind: machine.KindEpic, Title: title,
			Verb: "create_architecture", Actor: machine.Server("test")})
		if res.Refusal != nil {
			t.Fatalf("create epic: %+v", *res.Refusal)
		}
		ids = append(ids, id)
	}
	named, other := ids[0], ids[1]
	co := w.openRoom("intake", "", "split "+named+" into two epics")["checkout"].(string)

	env, code := w.as(co, "work", "show", other, "--project", "alpha")
	if code != 3 || env["code"] != "CAPABILITY_DENIED" || env["rule"] != "item_not_held" {
		t.Errorf("the room's work show %s = %d %v, want CAPABILITY_DENIED item_not_held", other, code, env)
	}
	env, code = w.as(co, "work", "note", named, "--project", "alpha", "--body", "a note")
	if code != 3 || env["code"] != "CAPABILITY_DENIED" {
		t.Errorf("the room's work note %s = %d %v, want CAPABILITY_DENIED", named, code, env)
	}
}
