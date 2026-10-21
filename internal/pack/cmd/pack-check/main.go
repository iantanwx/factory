// Command pack-check runs the pack gate (R39) over the embedded pack.
// It checks every command reference against the schema this binary
// builds: the CLI surface plus the refusal codes of the client-side host
// verbs. `make pack-check` runs it with both prose linters.
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
			fmt.Fprintln(os.Stderr, "pack-check:", err)
			os.Exit(2)
		}
	}
	findings, err := pack.Check(pack.Source(), reg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pack-check:", err)
		os.Exit(2)
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) > 0 {
		fmt.Printf("\npack-check: %d finding(s)\n", len(findings))
		os.Exit(1)
	}
	fmt.Println("pack-check: the pack matches the schema")
}
