package config_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/pack"
)

const (
	goodCommit = "0123456789abcdef0123456789abcdef01234567"
	goodSHA    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// pinJSON is one pin as a JSON object. YAML reads the same text as a flow
// mapping, so one row feeds both readers. extra adds fields after sha256.
func pinJSON(name, repo, commit, path, sha, extra string) string {
	return fmt.Sprintf(`{"name": %q, "repo": %q, "commit": %q, "path": %q, "sha256": %q%s}`, name, repo, commit, path, sha, extra)
}

func good(name string) string { return pinJSON(name, "owner/repo", goodCommit, "", goodSHA, "") }

// pinCases are the bad pins of C2. Each row names the pin label and the
// field that its refusal must name.
var pinCases = []struct {
	name  string
	pins  []string
	label string
	field string
}{
	{"empty name", []string{good("")}, "pin 0", "name"},
	{"upper-case name", []string{good("Neon")}, `"Neon"`, "name"},
	{"name with a slash", []string{good("neon/x")}, `"neon/x"`, "name"},
	{"name in the loop- namespace", []string{good("loop-neon")}, `"loop-neon"`, "name"},
	{"commit of 39 characters", []string{pinJSON("neon", "owner/repo", goodCommit[:39], "", goodSHA, "")}, `"neon"`, "commit"},
	{"commit not hex", []string{pinJSON("neon", "owner/repo", strings.Repeat("g", 40), "", goodSHA, "")}, `"neon"`, "commit"},
	{"sha256 outside 0-9a-f", []string{pinJSON("neon", "owner/repo", goodCommit, "", strings.Repeat("A", 64), "")}, `"neon"`, "sha256"},
	{"short sha256", []string{pinJSON("neon", "owner/repo", goodCommit, "", goodSHA[:63], "")}, `"neon"`, "sha256"},
	{"path that is .", []string{pinJSON("neon", "owner/repo", goodCommit, ".", goodSHA, "")}, `"neon"`, "path"},
	{"absolute path", []string{pinJSON("neon", "owner/repo", goodCommit, "/skills/neon", goodSHA, "")}, `"neon"`, "path"},
	{"path with ..", []string{pinJSON("neon", "owner/repo", goodCommit, "skills/../../neon", goodSHA, "")}, `"neon"`, "path"},
	{"repo with no owner", []string{pinJSON("neon", "repo", goodCommit, "", goodSHA, "")}, `"neon"`, "repo"},
	{"repo as a URL", []string{pinJSON("neon", "https://github.com/owner/repo", goodCommit, "", goodSHA, "")}, `"neon"`, "repo"},
	{"repo with a third segment", []string{pinJSON("neon", "owner/repo/x", goodCommit, "", goodSHA, "")}, `"neon"`, "repo"},
	{"repo that is ..", []string{pinJSON("neon", "owner/..", goodCommit, "", goodSHA, "")}, `"neon"`, "repo"},
	{"repo that holds ..", []string{pinJSON("neon", "owner/a..b", goodCommit, "", goodSHA, "")}, `"neon"`, "repo"},
	{"repo with a space", []string{pinJSON("neon", "owner/re po", goodCommit, "", goodSHA, "")}, `"neon"`, "repo"},
	{"unknown field", []string{pinJSON("neon", "owner/repo", goodCommit, "", goodSHA, `, "tag": "v1"`)}, `"neon"`, "tag"},
	{"two pins with one name", []string{good("neon"), good("neon")}, `"neon"`, "name"},
}

// goodPins are pins that both readers accept.
var goodPins = [][]string{
	{good("neon")},
	{good("asd-ste100"), good("neon-postgres")},
	{pinJSON("impeccable", "pbakaus/impeccable", goodCommit, ".agents/skills/impeccable", goodSHA, "")},
	{pinJSON("neon", "neondatabase/agent-skills", goodCommit, "skills/neon", goodSHA, "")},
	{pinJSON("x", "a-b/c.d_e-f", goodCommit, "", goodSHA, "")},
}

const baseConfig = "merge:\n  mechanism: direct\n"

func projectConfig(pins []string) []byte {
	return []byte(baseConfig + "skills: [" + strings.Join(pins, ", ") + "]\n")
}

func thirdParty(pins []string) fstest.MapFS {
	return fstest.MapFS{pack.ThirdPartyFile: {Data: []byte(`{"skills": [` + strings.Join(pins, ", ") + `]}`)}}
}

// TestC2ABadUserspacePinDoesNotLoad proves C2: each bad pin fails the
// parse with an error that names the pin and the field.
func TestC2ABadUserspacePinDoesNotLoad(t *testing.T) {
	for _, c := range pinCases {
		t.Run(c.name, func(t *testing.T) {
			got, err := config.ParseProjectConfig(projectConfig(c.pins))
			if err == nil {
				t.Fatalf("want a refusal, got %+v", got)
			}
			if !strings.Contains(err.Error(), c.label) || !strings.Contains(err.Error(), c.field) {
				t.Fatalf("the refusal does not name %s and %s: %v", c.label, c.field, err)
			}
		})
	}
}

// TestC2TwoGoodPinsParseInOrder proves the last case of C2: two valid pins
// with distinct names parse, in their order.
func TestC2TwoGoodPinsParseInOrder(t *testing.T) {
	cfg, err := config.ParseProjectConfig(projectConfig([]string{
		pinJSON("neon-postgres", "neondatabase/agent-skills", goodCommit, "skills/neon-postgres", goodSHA, ""),
		good("asd-ste100"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range cfg.Skills {
		got = append(got, fmt.Sprintf("%s %s %s %s %s", p.Name, p.Repo, p.Commit, p.Path, p.SHA256))
	}
	want := []string{
		"neon-postgres neondatabase/agent-skills " + goodCommit + " skills/neon-postgres " + goodSHA,
		"asd-ste100 owner/repo " + goodCommit + "  " + goodSHA,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("skills = %q, want %q", got, want)
	}
}

// TestC3AKernelPinAndAUserspacePinPassOneCheck proves C3: for each row of
// C2 and each good pin, ReadThirdParty and ParseProjectConfig both accept
// or both refuse. The shipped kernel pins pass as userspace pins, the repo
// rule among the checks.
func TestC3AKernelPinAndAUserspacePinPassOneCheck(t *testing.T) {
	rows := map[string][]string{}
	for _, c := range pinCases {
		rows["bad: "+c.name] = c.pins
	}
	for i, pins := range goodPins {
		rows[fmt.Sprintf("good %d", i)] = pins
	}
	var shipped []string
	for _, p := range pack.ThirdPartyPins() {
		shipped = append(shipped, pinJSON(p.Name, p.Repo, p.Commit, p.Path, p.SHA256, ""))
	}
	if len(shipped) == 0 {
		t.Fatal("the release embeds no pin")
	}
	rows["shipped"] = shipped
	for name, pins := range rows {
		t.Run(name, func(t *testing.T) {
			_, kernel := pack.ReadThirdParty(thirdParty(pins))
			_, user := config.ParseProjectConfig(projectConfig(pins))
			if (kernel == nil) != (user == nil) {
				t.Fatalf("the readers disagree: kernel %v, userspace %v", kernel, user)
			}
			if strings.HasPrefix(name, "bad: ") == (kernel == nil) {
				t.Fatalf("outcome %v for %s", kernel, name)
			}
		})
	}
}
