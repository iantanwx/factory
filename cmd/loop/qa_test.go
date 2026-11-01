package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// qaPlan runs `loop qa plan` in the working directory and returns its
// exit, the envelope and the envelope's data, as JSON.
func qaPlan(t *testing.T) (int, map[string]any, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"qa", "plan"}, strings.NewReader(""), &stdout, &stderr)
	var env map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not one JSON envelope: %v\n%s", err, stdout.String())
	}
	data, err := json.Marshal(env["data"])
	if err != nil {
		t.Fatal(err)
	}
	return exit, env, string(data)
}

// verifySkills is a checkout with two verify skills: web over apps/web/**
// and api over apps/api/**.
var verifySkills = with(goFiles, map[string]string{
	".claude/skills/verify-web/SKILL.md": "---\nname: verify-web\ndescription: drive the web app\npaths:\n  - apps/web/**\n---\n",
	".claude/skills/verify-api/SKILL.md": "---\nname: verify-api\ndescription: drive the api\npaths: [apps/api/**]\n---\n",
})

// ticketBrief is a brief whose ticket may change apps/web/src/a.ts, in a
// run that holds loop-impeccable.
const ticketBrief = `{"run_id": "r-1", "design_skill": "loop-impeccable", "item": {"id": "x-1", "kind": "ticket",
	"body": "## Files\n\nMay change: ` + "`apps/web/src/a.ts`" + `.\n"}}`

// C3 and C4: `loop qa plan` in a checkout with two verify skills prints
// the app whose paths meet the ticket, visual QA for the run's design
// skill, and the bounds.
func TestC3C4QAPlanPrintsTheAppThatMeetsTheTicketAndTheBounds(t *testing.T) {
	uiRepo(t, verifySkills, map[string]string{".loop/brief.json": ticketBrief})
	exit, env, data := qaPlan(t)
	want := `{"apps":[{"name":"api","paths":["apps/api/**"],"skill":".claude/skills/verify-api/SKILL.md"},` +
		`{"name":"web","paths":["apps/web/**"],"skill":".claude/skills/verify-web/SKILL.md"}],` +
		`"bounds":{"minutes":20,"screens":8,"steps":25,"variation_steps":10,"variations":3,` +
		`"widths":[{"height":800,"width":1280},{"height":844,"width":390}]},` +
		`"design_skill":"loop-impeccable","drive":["web"],"visual":true}`
	if exit != 0 || env["ok"] != true || data != want {
		t.Fatalf("exit %d, data %s, want exit 0, data %s\n%v", exit, data, want, env)
	}
}

// C3: a checkout with no verify skill prints an empty Drive.
func TestC3QAPlanPrintsAnEmptyDriveWithNoVerifySkill(t *testing.T) {
	uiRepo(t, goFiles, map[string]string{".loop/brief.json": ticketBrief})
	exit, env, data := qaPlan(t)
	if exit != 0 || !strings.Contains(data, `"apps":[]`) || !strings.Contains(data, `"drive":[]`) {
		t.Fatalf("exit %d, data %s, want exit 0 with no app\n%v", exit, data, env)
	}
}

// A checkout with no brief, or a bad verify skill, refuses with USAGE.
func TestQAPlanRefusesWithNoBriefOrABadSkill(t *testing.T) {
	cases := []struct {
		name      string
		tracked   map[string]string
		untracked map[string]string
		rule      string
	}{
		{"no brief", verifySkills, nil, "no_brief"},
		{"a bad verify skill", with(goFiles, map[string]string{".claude/skills/verify-Web/SKILL.md": "---\nname: verify-Web\n---\n"}),
			map[string]string{".loop/brief.json": ticketBrief}, "bad_verify_skill"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			uiRepo(t, c.tracked, c.untracked)
			exit, env, _ := qaPlan(t)
			if exit != 2 || env["code"] != "USAGE" || env["rule"] != c.rule {
				t.Fatalf("exit %d, want 2 USAGE %s\n%v", exit, c.rule, env)
			}
		})
	}
}
