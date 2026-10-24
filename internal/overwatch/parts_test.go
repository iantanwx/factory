package overwatch_test

import (
	"slices"
	"testing"

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
