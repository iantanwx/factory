package admission

import "github.com/iantanwx/loop/internal/cli"

// SetDraining turns the drain on or off (AE6). While it is on, Admit
// refuses every run with DRAINING and Peek admits nothing. It touches no
// lease and no run already admitted. The scheduler (U8) sets it.
func (a *Admission) SetDraining(on bool) { a.draining.Store(on) }

// Draining reports whether the drain is on.
func (a *Admission) Draining() bool { return a.draining.Load() }

func drainingRefusal() *cli.Refusal {
	return &cli.Refusal{Code: cli.CodeDraining, Rule: "draining",
		Hint: "the server is draining for an upgrade or a stop and admits no new run"}
}
