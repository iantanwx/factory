package server

import (
	"context"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// F-16: the coordinator files the ticket that writes an architecture
// epic's ADR under that epic. The epic has no PRD, so the ticket cites the
// operator's request; it still carries the epic in its epic fact, and the
// run that holds the ticket reads the epic, body and all.
func TestADRTicketOfAnArchitectureEpicReadsItsEpic(t *testing.T) {
	w, _, _ := newDocWorld(t)
	env, code := w.coordinator("epic", "create", "--architecture", "--project", "alpha", "--title", "Quality ratchets",
		"--body", "The epic's body: what the ADR decides.")
	if code != 0 {
		t.Fatalf("epic create = %d %v", code, env)
	}
	epic := env["data"].(map[string]any)["epic"].(string)

	env, code = w.coordinator("work", "create", "--project", "alpha", "--title", "docs: write ADR 0003 for architecture epic "+epic,
		"--body", "Write the ADR. Read the epic first: loop work show "+epic, "--epic", epic,
		"--request", "Ian: write the ADR for that epic")
	if code != 0 {
		t.Fatalf("work create of the ADR ticket under its architecture epic = %d %v", code, env)
	}
	ticket := env["data"].(map[string]any)["item"].(string)
	if got := w.show(ticket).Fact(machine.FactEpic); got != epic {
		t.Fatalf("ADR ticket epic fact = %q; want %q", got, epic)
	}

	co := w.runToken("run-1", ticket, machine.StageImplementer)
	env, code = w.as(co, "work", "show", epic, "--project", "alpha")
	if code != 0 {
		t.Fatalf("work show of the ADR ticket's epic = %d %v", code, env)
	}
	if body, _ := env["data"].(map[string]any)["body"].(string); !strings.Contains(body, "what the ADR decides") {
		t.Fatalf("work show of the epic holds no body: %v", env["data"])
	}
}

// An architecture epic has no PRD, so its ticket cites no story, needs the
// operator's request, and only the coordinator creates it.
func TestArchitectureEpicTicketCitesNoStory(t *testing.T) {
	w, _, _ := newDocWorld(t)
	env, code := w.coordinator("epic", "create", "--architecture", "--project", "alpha", "--title", "Quality ratchets")
	if code != 0 {
		t.Fatalf("epic create = %d %v", code, env)
	}
	epic := env["data"].(map[string]any)["epic"].(string)

	env, _ = w.coordinator("work", "create", "--project", "alpha", "--title", "ADR", "--epic", epic,
		"--story", "US-01", "--request", "Ian: write the ADR")
	if env["code"] != cli.CodeUsage {
		t.Fatalf("work create with --story under an architecture epic = %v; want %s", env, cli.CodeUsage)
	}
	env, _ = w.coordinator("work", "create", "--project", "alpha", "--title", "ADR", "--epic", epic)
	if env["code"] != cli.CodePRDRequired {
		t.Fatalf("work create under an architecture epic with no --request = %v; want %s", env, cli.CodePRDRequired)
	}
	runCo := w.runToken("run-x", "", machine.StageImplementer)
	env, _ = w.as(runCo, "work", "create", "--project", "alpha", "--title", "ADR", "--epic", epic,
		"--request", "Ian: write the ADR")
	if env["code"] != cli.CodePRDRequired {
		t.Fatalf("a run's ticket under an architecture epic = %v; want %s", env, cli.CodePRDRequired)
	}
}

// C4 through the server: the pipeline reads the project's own config, so
// `epic create --request` in a project that does not enable prd-author is
// refused with prd_author_disabled, and no epic is created.
func TestEpicCreateRequestRefusesAProjectWithoutAPRDAuthor(t *testing.T) {
	w, _, _ := newDocWorld(t)
	env, code := w.coordinator("epic", "create", "--project", "alpha", "--title", "Watchlists",
		"--request", "Ian: port the tickertown watchlist")
	if code == 0 || env["code"] != cli.CodeUsage || env["rule"] != "prd_author_disabled" {
		t.Fatalf("epic create --request = %d %v, want USAGE prd_author_disabled", code, env)
	}
	beads, err := w.bx.List(context.Background(), beadsx.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(beads) != 0 {
		t.Fatalf("a refused epic create made %d beads", len(beads))
	}
}
