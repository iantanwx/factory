package captures

import (
	"encoding/json"
	"reflect"
	"testing"
)

var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func TestBranchIsTheTicketBranchWithTheCapturesSuffix(t *testing.T) {
	if got := Branch("factorio-8i04"); got != "loop/factorio-8i04-captures" {
		t.Fatalf("Branch = %q, want loop/factorio-8i04-captures", got)
	}
}

// C11: a set of one PNG for each cited state passes. A set that lacks a
// state gives captures_missing, a file that names no cited state gives
// capture_unknown, and a file whose bytes are not a PNG gives
// capture_not_png. Each fault names each state or file.
func TestC11CheckRefusesAnIncompleteOrWrongSet(t *testing.T) {
	cited := []string{"S1.1", "S1.2"}
	cases := []struct {
		name  string
		files map[string][]byte
		want  *Fault
	}{
		{"complete", map[string][]byte{"S1.1.png": png, "S1.2.png": png}, nil},
		{"no set", nil, &Fault{Rule: RuleMissing, Names: []string{"S1.1", "S1.2"}}},
		{"one state alone", map[string][]byte{"S1.1.png": png}, &Fault{Rule: RuleMissing, Names: []string{"S1.2"}}},
		{"an uncited state", map[string][]byte{"S1.1.png": png, "S1.2.png": png, "S3.1.png": png},
			&Fault{Rule: RuleUnknown, Names: []string{"S3.1.png"}}},
		{"another name", map[string][]byte{"S1.1.png": png, "S1.2.png": png, "notes.txt": png},
			&Fault{Rule: RuleUnknown, Names: []string{"notes.txt"}}},
		{"text bytes", map[string][]byte{"S1.1.png": []byte("not a picture\n"), "S1.2.png": png},
			&Fault{Rule: RuleNotPNG, Names: []string{"S1.1.png"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Check(cited, c.files); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Check = %+v, want %+v", got, c.want)
			}
		})
	}
}

// C10: the manifest names the ticket, the epic, the record path and the
// cited ids in the order the ticket cites them.
func TestC10ManifestNamesTheTicketTheRecordAndTheStates(t *testing.T) {
	m := ManifestOf("tk-1", "tk-9", []string{"S1.2", "S1.1"})
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ticket":"tk-1","epic":"tk-9","record":"docs/design/tk-9/record.md","states":["S1.2","S1.1"]}`
	if string(raw) != want {
		t.Fatalf("manifest %s, want %s", raw, want)
	}
}

// C27: a share is a percent from 0% to 100% with its % sign and at most
// two decimals, read as hundredths of a percent.
func TestC27ShareReadsHundredthsOfAPercent(t *testing.T) {
	for share, want := range map[string]int{"1.20%": 120, "0.8%": 80, "0%": 0, "100%": 10000, "100.00%": 10000, "0.05%": 5} {
		if got, ok := Hundredths(share); !ok || got != want {
			t.Errorf("Hundredths(%q) = %d, %v, want %d", share, got, ok, want)
		}
	}
	for _, share := range []string{"1.205%", "1.2", "-1%", "101%", "100.01%", "", "%", ".5%", "1.%", "1,20%", " 1%", "99999999999999999999%"} {
		if got, ok := Hundredths(share); ok {
			t.Errorf("Hundredths(%q) = %d, want a refusal", share, got)
		}
	}
}
