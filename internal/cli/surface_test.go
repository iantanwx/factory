package cli

import "testing"

// C1 and C3: `loop work create --help` and `loop work shape --help` list
// blocked-by.
func TestWorkCreateAndWorkShapeHelpListBlockedBy(t *testing.T) {
	r := New()
	for _, verb := range []string{"create", "shape"} {
		help, code := runJSON(t, r, "work", verb, "--help")
		if code != 0 {
			t.Fatalf("work %s --help exited %d: %v", verb, code, help)
		}
		found := false
		for _, f := range help["data"].(map[string]any)["flags"].([]any) {
			found = found || f.(map[string]any)["name"] == "blocked-by"
		}
		if !found {
			t.Errorf("work %s --help lists no blocked-by", verb)
		}
	}
}

// C18: `loop work shape` with no --priority, or a priority outside 1 to 4,
// exits 2 with USAGE before it reaches the server.
func TestC18WorkShapeRefusesAMissingOrUnknownPriority(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"no priority", nil},
		{"priority 0", []string{"--priority", "0"}},
		{"priority 5", []string{"--priority", "5"}},
		{"priority high", []string{"--priority", "high"}},
	} {
		args := append([]string{"work", "shape", "bug-1", "--title", "t", "--body", "b"}, c.args...)
		env, code := runJSON(t, New(), args...)
		if code != 2 || env["code"] != CodeUsage {
			t.Errorf("%s: exit %d, code %v; want exit 2 with USAGE", c.name, code, env["code"])
		}
	}
}

// C12: `loop epic create --help` lists request and blocked-by, and the
// capability matrix admits only the coordinator on epic create.
func TestC12EpicCreateListsRequestAndBlockedByForTheCoordinatorOnly(t *testing.T) {
	help, code := runJSON(t, New(), "epic", "create", "--help")
	if code != 0 {
		t.Fatalf("epic create --help exited %d: %v", code, help)
	}
	flags := map[any]bool{}
	for _, f := range help["data"].(map[string]any)["flags"].([]any) {
		flags[f.(map[string]any)["name"]] = true
	}
	for _, want := range []string{"request", "blocked-by"} {
		if !flags[want] {
			t.Errorf("epic create --help lists no %s", want)
		}
	}
	m, err := DeriveMatrix(Surface())
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Verbs["epic create"]; len(got) != 1 || got[0] != PrincipalCoordinator {
		t.Errorf("epic create admits %v, want only %s", got, PrincipalCoordinator)
	}
}

// C12: `loop prd submit --help` lists run among its callers, beside the
// intake room, and the capability matrix admits a run and a room.
func TestC12PRDSubmitListsRunAmongItsCallers(t *testing.T) {
	help, code := runJSON(t, New(), "prd", "submit", "--help")
	if code != 0 {
		t.Fatalf("prd submit --help exited %d: %v", code, help)
	}
	callers := help["data"].(map[string]any)["callers"].([]any)
	if len(callers) != 2 || callers[0] != "intake room" || callers[1] != "run" {
		t.Errorf("prd submit --help lists the callers %v, want [intake room run]", callers)
	}
	m, err := DeriveMatrix(Surface())
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Verbs["prd submit"]; len(got) != 2 || got[0] != PrincipalRun || got[1] != PrincipalRoom {
		t.Errorf("prd submit admits %v, want %s and %s", got, PrincipalRun, PrincipalRoom)
	}
}
