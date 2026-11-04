package gates

import (
	"context"
	"testing"
)

// A block that a tool writes between named markers holds a semicolon. The
// project exempts that region in its config, with a reason. The prose gate
// passes. The same semicolon outside the region still fails the gate.
func TestProseSkipsAnExemptMarkedRegion(t *testing.T) {
	cfg := "merge:\n  mechanism: direct\n" +
		"prose:\n  exempt:\n" +
		"    - path: AGENTS.md\n      region: nextjs-agent-rules\n      reason: next dev writes this block again\n"
	generated := "<!-- BEGIN:nextjs-agent-rules -->\nThis is not the Next.js you know; read the guide first.\n<!-- END:nextjs-agent-rules -->\n"

	exempt := testRepo(t, map[string]string{
		".loop/config.yaml": cfg,
		"AGENTS.md":         "# Agents\n\n" + generated + "\nRun the tests.\n",
	})
	findings, err := Prose(context.Background(), exempt)
	if err != nil {
		t.Fatal(err)
	}
	if got := rules(findings); len(got) != 0 {
		t.Fatalf("exempt region: findings %v, want none", got)
	}

	outside := testRepo(t, map[string]string{
		".loop/config.yaml": cfg,
		"AGENTS.md":         "# Agents\n\n" + generated + "\nRun the tests; then push.\n",
	})
	findings, err = Prose(context.Background(), outside)
	if err != nil {
		t.Fatal(err)
	}
	if got := rules(findings); len(got) != 1 || got[0] != "ste" {
		t.Fatalf("semicolon outside the region: findings %v, want [ste]", got)
	}
}

// An entry with no region exempts the whole file from both linters. A
// file that no entry names still fails the gate.
func TestProseSkipsAnExemptFile(t *testing.T) {
	cfg := "merge:\n  mechanism: direct\n" +
		"prose:\n  exempt:\n" +
		"    - path: CLAUDE.md\n      reason: a vendor writes this file\n"
	slop := "# Claude\n\nThis is a seamless setup; it is a game-changer — really.\n"

	r := testRepo(t, map[string]string{".loop/config.yaml": cfg, "CLAUDE.md": slop})
	findings, err := Prose(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got := rules(findings); len(got) != 0 {
		t.Fatalf("exempt file: findings %v, want none", got)
	}

	r = testRepo(t, map[string]string{".loop/config.yaml": cfg, "CLAUDE.md": slop, "AGENTS.md": slop})
	findings, err = Prose(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got := rules(findings); len(got) != 2 || got[0] != "ste" || got[1] != "unslop" {
		t.Fatalf("file outside the exemption: findings %v, want [ste unslop]", got)
	}
}

// The unslop linter skips an exempt region too. An em dash in the region
// fails nothing, and the same em dash below it fails unslop.
func TestProseUnslopSkipsAnExemptRegion(t *testing.T) {
	cfg := "merge:\n  mechanism: direct\n" +
		"prose:\n  exempt:\n" +
		"    - path: AGENTS.md\n      region: vendor\n      reason: a vendor writes this block\n"
	block := "<!-- BEGIN:vendor -->\nRead the guide — it is short.\n<!-- END:vendor -->\n"

	r := testRepo(t, map[string]string{".loop/config.yaml": cfg, "AGENTS.md": "# Agents\n\n" + block})
	findings, err := Prose(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got := rules(findings); len(got) != 0 {
		t.Fatalf("exempt region: findings %v, want none", got)
	}

	r = testRepo(t, map[string]string{".loop/config.yaml": cfg, "AGENTS.md": "# Agents\n\n" + block + "\nRead the guide — it is short.\n"})
	findings, err = Prose(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got := rules(findings); len(got) != 1 || got[0] != "unslop" {
		t.Fatalf("em dash outside the region: findings %v, want [unslop]", got)
	}
}
