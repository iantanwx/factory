// Command docsonly decides whether a CI job runs every step or only the
// documents gates, and writes docs=true or docs=false to GITHUB_OUTPUT.
// It reads the event from GITHUB_EVENT_NAME, PR_BASE, BEFORE and FORCED,
// and compares in the current directory.
package main

import (
	"fmt"
	"os"

	"github.com/iantanwx/loop/tools/ci"
)

func main() {
	base := ci.BaseOf(ci.Event{
		Name:   os.Getenv("GITHUB_EVENT_NAME"),
		PRBase: os.Getenv("PR_BASE"),
		Before: os.Getenv("BEFORE"),
		Forced: os.Getenv("FORCED"),
	})
	var files []string
	if base.SHA != "" {
		files = ci.FilesSince(base.SHA, ".")
	}
	d := ci.Decide(base, files)
	run := "Every gate"
	if d.Docs {
		run = "Documents run"
	}
	fmt.Printf("%s: %s\n", run, d.Why)
	out, err := os.OpenFile(os.Getenv("GITHUB_OUTPUT"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "docsonly: GITHUB_OUTPUT:", err)
		os.Exit(1)
	}
	defer func() { _ = out.Close() }()
	if _, err := fmt.Fprintf(out, "docs=%t\n", d.Docs); err != nil {
		fmt.Fprintln(os.Stderr, "docsonly:", err)
		os.Exit(1)
	}
}
