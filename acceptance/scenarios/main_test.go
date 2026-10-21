//go:build acceptance

package scenarios

import (
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/iantanwx/loop/acceptance"
)

// TestMain builds the loop binary once, runs the scenarios, and then holds
// the row catalogue to what the scenarios applied.
func TestMain(m *testing.M) {
	flag.Parse()
	if _, _, ok := acceptance.BeadsBinaries(); !ok {
		fmt.Println(acceptance.SkipMessage)
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "accbin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	acceptance.Binary, err = acceptance.BuildBinary(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(dir)
		os.Exit(2)
	}
	binDir = dir
	code := m.Run()
	if code == 0 {
		code = checkCatalogue()
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// binDir holds the binaries TestMain built.
var binDir string
