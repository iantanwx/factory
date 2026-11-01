//go:build acceptance

package scenarios

import (
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/gitx/gittest"
)

// laneGate is the lane binary's gate. Only lane_orca_test.go, under the
// tags acceptance and orca, sets it. TestMain runs it first, and exits with
// any non-zero code it returns.
var laneGate func() int

// TestMain runs the lane gate when it is set, takes or builds the loop
// binary, runs the scenarios, and then holds the row catalogue to what the
// scenarios applied.
func TestMain(m *testing.M) {
	flag.Parse()
	if laneGate != nil {
		if code := laneGate(); code != 0 {
			os.Exit(code)
		}
	}
	if _, _, ok := acceptance.BeadsBinaries(); !ok {
		fmt.Println(acceptance.SkipMessage)
		// CI provisions both binaries, and so does the lane, so a miss
		// there is a fault, never a pass. A box without them still skips.
		if os.Getenv("CI") != "" || laneGate != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "accbin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if bin := os.Getenv(acceptance.LoopBinaryEnv); bin != "" {
		if err := acceptance.CheckLoop(bin); err != nil {
			fmt.Println(err)
			_ = os.RemoveAll(dir)
			os.Exit(1)
		}
		acceptance.Binary = bin
	} else if acceptance.Binary, err = acceptance.BuildBinary(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(dir)
		os.Exit(2)
	}
	binDir = dir
	code := gittest.Main(m)
	if code == 0 {
		code = checkCatalogue()
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// binDir holds the binaries TestMain built.
var binDir string
