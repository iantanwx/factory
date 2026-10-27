package gates

import (
	"context"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/release"
)

// readmeNaming is a README whose Requirements section names each binary
// of the table except skip.
func readmeNaming(skip string) string {
	var b strings.Builder
	b.WriteString("# loop\n\n## Requirements\n\n")
	for _, r := range release.Requirements {
		if r.Bin != skip {
			b.WriteString("- `" + r.Bin + "`\n")
		}
	}
	b.WriteString("\n## Develop\n\nRun `gitleaks` in CI.\n")
	return b.String()
}

const loopGoMod = "module github.com/iantanwx/loop\n\ngo 1.26.0\n"

// A README that omits one row of the table fails, and the finding names
// the row. A name outside the Requirements section does not count.
func TestRequirementsGateNamesTheRowTheReadmeOmits(t *testing.T) {
	r := testRepo(t, map[string]string{"go.mod": loopGoMod, "README.md": readmeNaming("gitleaks")})
	got, err := Requirements(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].File != "README.md" || got[0].Rule != "requirement_not_in_readme" ||
		!strings.Contains(got[0].Message, "gitleaks") {
		t.Fatalf("findings = %+v", got)
	}

	r = testRepo(t, map[string]string{"go.mod": loopGoMod, "README.md": readmeNaming("")})
	if got, err := Requirements(context.Background(), r); err != nil || len(got) != 0 {
		t.Fatalf("a README that names every row = %+v %v", got, err)
	}
}

// A README with no Requirements section fails once for each row.
func TestRequirementsGateFailsAReadmeWithNoSection(t *testing.T) {
	r := testRepo(t, map[string]string{"go.mod": loopGoMod, "README.md": "# loop\n\nRequires `git`.\n"})
	got, err := Requirements(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(release.Requirements) {
		t.Fatalf("findings = %+v", got)
	}
}

// The table names the loop's own binaries, so a client project's README
// owes it nothing.
func TestRequirementsGateSkipsAnotherProject(t *testing.T) {
	r := testRepo(t, map[string]string{"go.mod": "module example.com/shop\n", "README.md": "# shop\n"})
	if got, err := Requirements(context.Background(), r); err != nil || len(got) != 0 {
		t.Fatalf("another project = %+v %v", got, err)
	}
}
