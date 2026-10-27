package server

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/room"
)

// C-bug-1: `loop room open` opens a room only for a finding the triage
// rubric routes to a person: level 4 or 5, or the Choice outside. An
// unscored open, and a score at levels 1 to 3, are refused, and no room
// opens, as `escalation raise` refuses them with ROUTED_TO_LOOP.
func TestRoomOpenNeedsAFindingTheRubricRoutesToAPerson(t *testing.T) {
	w := newRoomWorld(t)
	open := func(extra ...string) (map[string]any, int) {
		args := append([]string{"room", "open", "--kind", "intake", "--project", "alpha", "--reason", "tickertown gaps H7 and H8"}, extra...)
		return w.coordinator(args...)
	}
	if env, code := open(); code == 0 {
		t.Fatalf("an unscored room opened: %v", env)
	}
	for _, score := range []string{"1", "2", "3"} {
		if env, _ := open("--score", score); env["code"] != cli.CodeRoutedToLoop {
			t.Fatalf("a room at level %s = %v, want %s", score, env, cli.CodeRoutedToLoop)
		}
	}
	if n := w.count(`SELECT count(*) FROM rooms`); n != 0 {
		t.Fatalf("refused opens left %d rooms", n)
	}
	env, code := open("--score", "4")
	if code != 0 {
		t.Fatalf("a room at level 4 = %d %v", code, env)
	}
	if r := env["data"].(map[string]any)["room"].(map[string]any); r["state"] != room.Live {
		t.Fatalf("a room at level 4 = %v", r)
	}
}

// C-bug-1: level 5, and any level with the Choice outside, route the
// finding to a person, so the room opens. A score that is no level of
// the rubric is a usage error, and no room opens.
func TestRoomOpenAtOwnerOnlyOrOutsideOpensTheRoom(t *testing.T) {
	w := newRoomWorld(t)
	open := func(kind, item string, extra ...string) (map[string]any, int) {
		args := append([]string{"room", "open", "--kind", kind, "--project", "alpha", "--reason", "the operator chooses"}, extra...)
		if item != "" {
			args = append(args, "--item", item)
		}
		return w.coordinator(args...)
	}
	if env, _ := open("intake", "", "--score", "7"); env["code"] != cli.CodeUsage {
		t.Fatalf("a room at level 7 = %v, want %s", env, cli.CodeUsage)
	}
	if n := w.count(`SELECT count(*) FROM rooms`); n != 0 {
		t.Fatalf("a refused open left %d rooms", n)
	}
	env, code := open("intake", "", "--score", "2", "--choice", "outside")
	if code != 0 || env["data"].(map[string]any)["room"].(map[string]any)["state"] != room.Live {
		t.Fatalf("a room at level 2 with the Choice outside = %d %v", code, env)
	}
	env, code = open("review", todoTicket(t, w.bx), "--score", "owner-only")
	if code != 0 || env["data"].(map[string]any)["room"].(map[string]any)["state"] != room.Live {
		t.Fatalf("a room at level 5 = %d %v", code, env)
	}
}

// C16: the ROUTED_TO_LOOP hint of an intake room at level 1 to 3 names
// `loop epic create --request`, the route for new product work, and no
// room opens.
func TestC16RoomOpenRoutesNewProductWorkToEpicCreateRequest(t *testing.T) {
	w := newRoomWorld(t)
	for _, score := range []string{"1", "2", "3"} {
		env, _ := w.coordinator("room", "open", "--kind", "intake", "--project", "alpha", "--reason", "port the watchlist", "--score", score)
		if env["code"] != cli.CodeRoutedToLoop {
			t.Fatalf("an intake room at level %s = %v, want %s", score, env, cli.CodeRoutedToLoop)
		}
		if hint, _ := env["hint"].(string); !strings.Contains(hint, "`loop epic create --request`") {
			t.Errorf("the hint at level %s names no `loop epic create --request`: %q", score, hint)
		}
	}
	if n := w.count(`SELECT count(*) FROM rooms`); n != 0 {
		t.Fatalf("refused opens left %d rooms", n)
	}
}
