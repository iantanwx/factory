package shape_test

import (
	"reflect"
	"testing"

	"github.com/iantanwx/loop/internal/shape"
)

func TestContractIDsReadsTheIDsOfTheShape(t *testing.T) {
	cases := []struct {
		name  string
		shape shape.Shape
		body  string
		want  []string
	}{
		{
			"a ticket's ids come from Contracts to prove, in order, once each",
			shape.Ticket,
			"## Source\n\nC9 is out of scope.\n\n## Contracts to prove\n\n- C3. One.\n- C1. Two, with C3 again.\n- C10. Three.\n",
			[]string{"C3", "C1", "C10"},
		},
		{
			"a bug's ids come from its Contract headings",
			shape.Bug,
			"## Contract C-bug-1\n\nC4 fails.\n\n## Source\n\n## Contract C-bug-2\n\nmore\n",
			[]string{"C-bug-1", "C-bug-2"},
		},
		{"a body with no contract holds no id", shape.Ticket, "## Source\n\nC1.\n", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shape.ContractIDs(c.shape, c.body); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ContractIDs = %q, want %q", got, c.want)
			}
		})
	}
}

func TestUnnamedReturnsEachIDTheReviewOmits(t *testing.T) {
	ids := []string{"C1", "C10", "C-bug-1"}
	cases := []struct {
		review string
		want   []string
	}{
		{"C1, C10 and C-bug-1 hold.", nil},
		{"C10 holds, and C-bug-10 holds.", []string{"C1", "C-bug-1"}},
		{"(C1) and C10. C-bug-1:", nil},
		{"C1a and xC10", []string{"C1", "C10", "C-bug-1"}},
	}
	for _, c := range cases {
		if got := shape.Unnamed(ids, c.review); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Unnamed(%q) = %q, want %q", c.review, got, c.want)
		}
	}
}

func TestByNameReturnsTheNamedShape(t *testing.T) {
	for _, name := range []string{"ticket", "bug"} {
		s, ok := shape.ByName(name)
		if !ok || s.Name != name {
			t.Errorf("ByName(%q) = %q, %v", name, s.Name, ok)
		}
	}
	if _, ok := shape.ByName("epic"); ok {
		t.Error(`ByName("epic") found a shape`)
	}
}
