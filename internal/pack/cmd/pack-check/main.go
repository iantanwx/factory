// Command pack-check runs the pack gate (R39) over the embedded pack.
// It checks every command reference against the schema this binary
// builds: the CLI surface plus the refusal codes of the client-side host
// verbs. It then holds the triage rubric to the escalation corpus and the
// server's literal. `make pack-check` runs it from the repository root,
// with both prose linters.
package main

import (
	"fmt"
	"os"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/release"
)

func main() {
	reg := cli.Surface()
	for _, c := range release.Refusals() {
		if err := reg.RegisterRefusal(c); err != nil {
			fail(err)
		}
	}
	findings, err := pack.Check(pack.Source(), reg)
	if err != nil {
		fail(err)
	}
	corpus, err := os.ReadFile(pack.CorpusPath)
	if err != nil {
		fail(err)
	}
	triage, err := pack.CheckTriage(pack.Source(), corpus)
	if err != nil {
		fail(err)
	}
	findings = append(findings, triage...)
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) > 0 {
		fmt.Printf("\npack-check: %d finding(s)\n", len(findings))
		os.Exit(1)
	}
	fmt.Println("pack-check: the pack matches the schema, and the rubric matches the corpus and the server")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "pack-check:", err)
	os.Exit(2)
}
