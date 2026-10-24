package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// A run that holds a ticket reads the ticket's epic, body and all, though
// its token holds only the ticket. A write to the epic stays refused.
func TestRunReadsTheEpicOfItsTicket(t *testing.T) {
	w := newGHWorld(t)
	ctx := context.Background()
	m := w.srv.Machine
	epic, res := m.Create(ctx, machine.CreateRequest{Project: "alpha", Kind: machine.KindEpic, Title: "e",
		Description: "The epic's body: read me first.", Verb: "create_architecture", Actor: machine.Server("test")})
	if res.Refusal != nil {
		t.Fatalf("create epic: %+v", *res.Refusal)
	}
	ticket, res := m.Create(ctx, machine.CreateRequest{Project: "alpha", Kind: machine.KindTicket, Title: "t", Verb: "create",
		Actor: machine.Server("test"), Facts: map[string]string{machine.FactEpic: epic}})
	if res.Refusal != nil {
		t.Fatalf("create ticket: %+v", *res.Refusal)
	}
	w.issue(ticket, machine.StageImplementer, "run-1")
	w.t.Chdir(w.checkout)
	w.cli = cli.Surface()
	(&cli.Client{Instance: w.dir, Budget: 5 * time.Second, Getwd: func() (string, error) { return w.checkout, nil }}).Attach(w.cli)

	env := w.run("work", "show", epic, "--project", "alpha")
	if env["ok"] != true {
		t.Fatalf("work show of the ticket's epic = %v", env)
	}
	data, _ := env["data"].(map[string]any)
	if body, _ := data["body"].(string); !strings.Contains(body, "read me first") {
		t.Fatalf("work show of the epic holds no body: %v", env["data"])
	}
	env = w.run("work", "note", epic, "--body", "not mine", "--project", "alpha")
	if env["code"] != cli.CodeCapabilityDenied || env["rule"] != "item_not_held" {
		t.Fatalf("work note of the epic = %v", env)
	}
}
