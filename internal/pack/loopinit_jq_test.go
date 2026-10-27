package pack_test

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

var (
	// jqCommand is one jq call in the skill and its single-quoted program.
	jqCommand = regexp.MustCompile(`jq [^'\n]*'([^'\n]*)'`)
	// jqForm is the one form of a ratcheted jq line. The group is the key
	// filter, the only part that a project writes.
	jqForm = regexp.MustCompile(`^jq -rn '\[inputs\] \| select\(length > 0\) \| \(\.\[\] \| (.+)\), "loop:end"'$`)
)

// C30: every jq line that loop-init teaches prints the end line only after
// a tool printed something. Run with its key filter replaced by .a[], each
// line prints nothing on empty input, and one end line after the keys of a
// two-document stream.
func TestC30LoopInitJqLinesReadSilenceAsAFailure(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatalf("jq is not on PATH: %v", err)
	}
	var lines []string
	for _, m := range jqCommand.FindAllStringSubmatch(read(t, "skills/loop-init/SKILL.md"), -1) {
		if strings.Contains(m[1], "loop:end") {
			lines = append(lines, m[0])
		}
	}
	if len(lines) == 0 {
		t.Fatal("loop-init teaches no jq line that prints loop:end")
	}
	for _, line := range lines {
		form := jqForm.FindStringSubmatch(line)
		if form == nil {
			t.Errorf("%s does not have the form jq -rn '[inputs] | select(length > 0) | (.[] | <keys>), \"loop:end\"'", line)
			continue
		}
		keys := form[1]
		if strings.Contains(keys, "?") || strings.Contains(keys, "// empty") {
			t.Errorf("the key filter %s hides a changed output shape with ? or // empty", keys)
		}
		run := strings.Replace(line, "| "+keys+")", "| .a[])", 1)
		for _, c := range []struct{ in, want string }{
			{"", ""},
			{`{"a":["x"]} {"a":["y"]}`, "x\ny\nloop:end\n"},
		} {
			cmd := exec.Command("sh", "-c", run)
			cmd.Stdin = strings.NewReader(c.in)
			out, err := cmd.Output()
			if err != nil || string(out) != c.want {
				t.Errorf("%s on %q printed %q, %v; want %q", line, c.in, out, err, c.want)
			}
		}
	}
}
