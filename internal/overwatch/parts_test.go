package overwatch_test

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/overwatch"
)

// C32: the shipped part list is whole. Parts() holds one part for each name
// in PartNames, and no other.
func TestC32PartsHoldEachNameOfPartNamesOnce(t *testing.T) {
	var names []string
	for _, p := range overwatch.Parts() {
		names = append(names, p.Name)
	}
	slices.Sort(names)
	want := slices.Sorted(slices.Values(overwatch.PartNames))
	if !slices.Equal(names, want) {
		t.Fatalf("Parts() holds %q, want one part for each of %q", names, want)
	}
}

// Each part's Command is a loop command line the coordinator can run by
// hand: the CLI surface parses it. A part that names a removed verb or
// flag value, such as `loop work list --kind decision`, fails here. An
// empty Command means that no command repeats the read.
func TestEachPartCommandParsesAgainstTheCLISurface(t *testing.T) {
	surface := cli.New()
	for _, p := range overwatch.Parts() {
		if p.Command == "" {
			continue
		}
		words := strings.Fields(p.Command)
		if len(words) < 2 || words[0] != "loop" {
			t.Errorf("part %s: Command %q does not start with loop and a verb", p.Name, p.Command)
			continue
		}
		var out strings.Builder
		if exit := surface.Run(context.Background(), append(words[1:], "--help"), &out, io.Discard); exit != 0 {
			t.Errorf("part %s: Command %q does not parse: exit %d, %s", p.Name, p.Command, exit, out.String())
		}
	}
}
