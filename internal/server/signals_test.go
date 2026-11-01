package server

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/machine"
)

// A ticket that enters repair twice shows one red_build signal with two
// cases in work show.
func TestWorkShowListsARedBuildSignal(t *testing.T) {
	w := newGHWorld(t)
	id := w.claim()
	ctx := context.Background()
	for _, mv := range []struct{ verb, head string }{
		{"pr_open", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, {machine.VerbSimplifyOff, ""}, {machine.VerbCIRed, ""},
		{"fix_pushed", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, {machine.VerbCIRed, ""},
	} {
		params := map[string]string{}
		if mv.head != "" {
			params["head"] = mv.head
		}
		res := w.srv.Machine.Apply(ctx, machine.Request{Project: "alpha", Item: id, Verb: mv.verb, Params: params, Actor: machine.Server("test")})
		if res.Refusal != nil {
			t.Fatalf("%s: %+v", mv.verb, *res.Refusal)
		}
	}

	env := w.run("work", "show", id)
	data, _ := env["data"].(map[string]any)
	sigs, _ := data["signals"].([]any)
	if env["ok"] != true || data["status"] != machine.Repair || len(sigs) != 1 {
		t.Fatalf("work show = %v", env)
	}
	sig, _ := sigs[0].(map[string]any)
	cases, _ := sig["cases"].([]any)
	if sig["kind"] != "red_build" || len(cases) != 2 {
		t.Fatalf("signal = %v, want red_build with two cases", sig)
	}
}
