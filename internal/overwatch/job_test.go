package overwatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
)

// doorbell counts each ring and answers err.
type doorbell struct {
	texts []string
	err   error
}

func (d *doorbell) ring(_ context.Context, text string) error {
	d.texts = append(d.texts, text)
	return d.err
}

// ringRows counts the coordinator_ring rows of the store.
func ringRows(t *testing.T, w *fakeWorld) int {
	t.Helper()
	_, ok, err := LastRing(context.Background(), w.st)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		return 1
	}
	return 0
}

// C3: a tick whose every part reads code 0 calls the doorbell zero times
// and writes no coordinator_ring row. A tick whose every finding a record
// names reads code 0 too.
func TestC3AQuietSweepRingsNobody(t *testing.T) {
	w := newFakeWorld(t)
	d := &doorbell{}
	j := &Job{World: w, Ring: d.ring, Parts: []Part{
		readingPart("decisions", Reading{Notes: []string{"factorio-jam waits on: a release that holds factorio-88t ships; no by date"}}),
		readingPart("inbox", Reading{}),
	}}
	for range 3 {
		if err := j.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	key := Key("stalls", "factorio-x1y@in_progress")
	w.add("factorio", machine.Item{ID: "factorio-b9", Kind: machine.KindBug, Status: machine.Reported}, "Filed for "+key+".")
	j.Parts = append(j.Parts, keyPart("stalls", key))
	if err := j.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.texts) != 0 || ringRows(t, w) != 0 {
		t.Fatalf("a quiet sweep rang %q", d.texts)
	}
	if r, ok := j.Last(); !ok || r.Code != Quiet {
		t.Fatalf("the last report = %+v %v", r, ok)
	}
}

// C4: a tick whose report holds code 1 or 2 calls the doorbell exactly
// once, with server text that names parts only.
func TestC4ASweepWithWorkRingsOnceWithServerText(t *testing.T) {
	shortDeadline(t, 50*time.Millisecond)
	w := newFakeWorld(t)
	key := Key("stalls", "factorio-x1y@in_progress")
	d := &doorbell{}
	j := &Job{World: w, Ring: d.ring, Parts: []Part{
		{Name: "inbox", Read: func(context.Context, World) (Reading, error) { return Reading{}, errors.New("orca is down") }},
		readingPart("pulls", Reading{Findings: []Finding{{Key: Key("pulls", "factorio:factorio-x1y:work"), Line: "factorio-x1y holds #12 and #14"}}}),
		readingPart("stalls", Reading{Findings: []Finding{{Key: key, Line: "factorio-x1y stalls in in_progress: panel/7 waits (" + key + ")"}}}),
		readingPart("decisions", Reading{Notes: []string{"factorio-jam waits"}}),
		{Name: "server", Read: func(context.Context, World) (Reading, error) { panic("boom") }},
	}}
	if err := j.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.texts) != 1 {
		t.Fatalf("the doorbell rang %d times: %q", len(d.texts), d.texts)
	}
	want := "loop overwatch: work in stalls, pulls; unread inbox, server. Run `loop overwatch sweep`, then follow section 1 of your runbook."
	if d.texts[0] != want {
		t.Fatalf("ring text\n got %q\nwant %q", d.texts[0], want)
	}
	for _, leak := range []string{"factorio-x1y", "panel/7", key, "#12", "orca is down", "boom", "factorio-jam"} {
		if strings.Contains(d.texts[0], leak) {
			t.Fatalf("the ring text holds %q", leak)
		}
	}
}

// C4: a failed records read adds records to the unread list, and a list
// that is empty is left out.
func TestC4RingTextNamesRecordsAndLeavesOutAnEmptyList(t *testing.T) {
	r := Report{Code: Unread, Records: "UNREAD: beads is down", Parts: []PartReport{{Name: "stalls", Code: Work}, {Name: "bugs", Code: Quiet}}}
	text, ok := RingText(r)
	if !ok || text != "loop overwatch: work in stalls; unread records. Run `loop overwatch sweep`, then follow section 1 of your runbook." {
		t.Fatalf("RingText = %q %v", text, ok)
	}
	r = Report{Code: Unread, Parts: []PartReport{{Name: "ci", Code: Unread}}}
	if text, _ := RingText(r); text != "loop overwatch: unread ci. Run `loop overwatch sweep`, then follow section 1 of your runbook." {
		t.Fatalf("RingText = %q", text)
	}
	if text, ok := RingText(Report{Parts: []PartReport{{Name: "ci"}}}); ok || text != "" {
		t.Fatalf("a quiet report's RingText = %q %v", text, ok)
	}
}

// C6: a refused ring is never typed again in its tick. With nobody to ring
// the tick returns no error.
func TestC6ARefusedRingIsNeverTypedAgainInItsTick(t *testing.T) {
	w := newFakeWorld(t)
	parts := []Part{keyPart("stalls", Key("stalls", "a@b")), keyPart("bugs", "F-3")}
	refused := &doorbell{err: fmt.Errorf("%w: no receipt", agenthost.ErrNotifyRefused)}
	if err := (&Job{World: w, Parts: parts, Ring: refused.ring}).Tick(context.Background()); !errors.Is(err, agenthost.ErrNotifyRefused) {
		t.Fatalf("a refused ring's tick = %v", err)
	}
	if len(refused.texts) != 1 {
		t.Fatalf("a refused ring was typed %d times in its tick", len(refused.texts))
	}
	nobody := &doorbell{err: inbox.ErrNobodyToRing}
	if err := (&Job{World: w, Parts: parts, Ring: nobody.ring}).Tick(context.Background()); err != nil || len(nobody.texts) != 1 {
		t.Fatalf("with nobody to ring: %v, %d rings", err, len(nobody.texts))
	}
}
