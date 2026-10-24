package pack_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/escalation"
)

// The fixer runbook's join command, run on two files that `escalation
// judge --out` writes, yields a file that `escalation route` reads as two
// panels (F-27).
func TestFixerJoinCommandFeedsRoute(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not on PATH")
	}
	line := regexp.MustCompile(`(?m)^jq -s .*panels\.json$`).FindString(read(t, "runbooks/fixer.md"))
	if line == "" {
		t.Fatal("the fixer runbook names no jq command that writes panels.json")
	}
	dir := t.TempDir()
	for _, f := range []string{"f1", "f2"} {
		// writePanel encodes one panel the same way.
		text, err := escalation.EncodePanels([]escalation.Panel{{Finding: "finding " + f, Judges: []escalation.JudgeAnswer{
			{Judge: "fable-5", Level: "computed", Choice: escalation.Inside, Reason: "a command answers it"},
			{Judge: "sol-6", Level: "computed", Choice: escalation.Inside, Reason: "a command answers it"},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f+".json"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", strings.ReplaceAll(line, "<dir>", dir))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", line, err, out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "panels.json"))
	if err != nil {
		t.Fatal(err)
	}
	ps, err := escalation.ParsePanels(string(raw))
	if err != nil {
		t.Fatalf("route refuses the joined file of %q: %v", line, err)
	}
	if len(ps) != 2 || ps[0].Finding != "finding f1" || ps[1].Finding != "finding f2" {
		t.Fatalf("route reads %+v, want the two findings in order", ps)
	}
}
