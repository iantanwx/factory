package server

import (
	"testing"

	"github.com/iantanwx/loop/internal/machine"
)

// The coordinator writes on an item of the project it names. Its token
// holds no project, so the CLI's read of the item's status for the write's
// key must name that project too, or every such write is refused before
// it reaches the server's verb.
func TestCoordinatorWritesOnAnItemOfTheProjectItNames(t *testing.T) {
	w := newHumanWorld(t, "")
	id := todoTicket(t, w.bx)
	if env, code := w.coordinator("work", "note", id, "--project", "alpha", "--body", "the operator asked for this"); code != 0 {
		t.Fatalf("work note = %d %v", code, env)
	}
	if env, code := w.coordinator("work", "edit", id, "--project", "alpha", "--body", "A new body."); code != 0 {
		t.Fatalf("work edit = %d %v", code, env)
	}
	is, err := w.bx.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, body := machine.TaskText(is); body != "A new body." {
		t.Fatalf("body = %q", body)
	}
}
