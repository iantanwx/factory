package admission

import (
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/machine"
)

func TestClassify(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	cases := []struct {
		name string
		s    Settlement
		want Class
	}{
		{"finish done", Settlement{Finish: "done"}, ClassDone},
		{"finish failed beats an outage", Settlement{Finish: "failed", Started: at(0), Ended: at(9),
			Outages: []Outage{{at(2), at(5)}}}, ClassFailed},
		{"finish blocked", Settlement{Finish: "blocked"}, ClassBlocked},
		{"never ready", Settlement{NotReady: true, Ended: at(1)}, ClassNotReady},
		{"usage limit", Settlement{QuotaHit: true, Ended: at(1)}, ClassQuotaHit},
		{"end unknown", Settlement{Started: at(0)}, ClassUnknown},
		{"no CLI call, no finish", Settlement{Started: at(0), Ended: at(40)}, ClassCrashed},
		{"CLI calls, no finish", Settlement{Started: at(0), LastCLICall: at(20), Ended: at(40)}, ClassCrashed},
		{"outage before the last call", Settlement{Started: at(0), LastCLICall: at(20), Ended: at(40),
			Outages: []Outage{{at(5), at(15)}}}, ClassCrashed},
		{"outage after the session ended", Settlement{Started: at(0), LastCLICall: at(5), Ended: at(10),
			Outages: []Outage{{at(30), at(40)}}}, ClassCrashed},
		{"outage after the last call", Settlement{Started: at(0), LastCLICall: at(5), Ended: at(40),
			Outages: []Outage{{at(10), at(20)}}}, ClassInfra},
	}
	for _, c := range cases {
		if got := Classify(c.s); got != c.want {
			t.Errorf("%s: Classify = %s, want %s", c.name, got, c.want)
		}
	}
}

// The plan's two outage scenarios (U6), with injected times.
func TestOutageScenarios(t *testing.T) {
	w := newWorld(t)
	t0 := time.Now().Add(-time.Hour)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

	// The server is SIGKILLed for 10 minutes while a run's CLI times out
	// with exit 4 and the agent ends its session. The server observed the
	// run's last call before the kill.
	id := w.ticket(machine.Todo)
	ad := w.mustAdmit(machine.StageImplementer, id)
	out := w.settle(ad, Settlement{Started: at(0), LastCLICall: at(3), Ended: at(12),
		Outages: []Outage{{Start: at(5), End: at(15)}}})
	if out.Class != ClassInfra || out.Counted {
		t.Fatalf("SIGKILL scenario = %+v, want infra and not counted", out)
	}

	// The server is down while a run makes no CLI call, and the session
	// ends after the server returns, with no CLI call after the outage.
	ad = w.mustAdmit(machine.StageImplementer, id)
	out = w.settle(ad, Settlement{Started: at(20), Ended: at(50), Outages: []Outage{{Start: at(25), End: at(35)}}})
	if out.Class != ClassInfra || out.Counted {
		t.Fatalf("no-call scenario = %+v, want infra and not counted", out)
	}
	if n := w.failures(id); n != 0 {
		t.Fatalf("failures = %d after two infra runs, want 0", n)
	}

	// A run that makes a CLI call after the outage and then crashes counts.
	ad = w.mustAdmit(machine.StageImplementer, id)
	out = w.settle(ad, Settlement{Started: at(20), LastCLICall: at(40), Ended: at(55),
		Outages: []Outage{{Start: at(25), End: at(35)}}})
	if out.Class != ClassCrashed || !out.Counted {
		t.Fatalf("call-after-outage scenario = %+v, want crashed and counted", out)
	}
	if n := w.failures(id); n != 1 {
		t.Fatalf("failures = %d, want 1", n)
	}
}
