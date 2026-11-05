package coord_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/release"
)

// C1: coord.LoopRepo is the repository the loop releases from.
func TestC1LoopRepoIsTheReleaseRepo(t *testing.T) {
	if coord.LoopRepo != release.ReleaseRepo {
		t.Fatalf("coord.LoopRepo = %s, release.ReleaseRepo = %s", coord.LoopRepo, release.ReleaseRepo)
	}
}

// C1: the loop project is the one registered project of the release
// repository, matched without case. None, or two, name no loop project.
func TestC1TheLoopProjectIsTheOneProjectOfTheReleaseRepo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ps     []config.Project
		id     string
		wantOK bool
	}{
		{"one match", []config.Project{{ID: "home", Repo: "slothware-labs/factorio"}, {ID: "app", Repo: "acme/app"}}, "home", true},
		{"one match after another project", []config.Project{{ID: "app", Repo: "acme/app"}, {ID: "home", Repo: "slothware-labs/factorio"}}, "home", true},
		{"case differs", []config.Project{{ID: "app", Repo: "acme/app"}, {ID: "home", Repo: "Slothware-Labs/Factorio"}}, "home", true},
		{"no match", []config.Project{{ID: "app", Repo: "acme/app"}, {ID: "fork", Repo: "acme/factorio"}}, "", false},
		{"no project", nil, "", false},
		{"two matches", []config.Project{{ID: "home", Repo: "slothware-labs/factorio"}, {ID: "app", Repo: "acme/app"}, {ID: "copy", Repo: "SLOTHWARE-LABS/FACTORIO"}}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := coord.LoopProject(tc.ps)
			if id != tc.id || ok != tc.wantOK {
				t.Fatalf("LoopProject = %q, %v, want %q, %v", id, ok, tc.id, tc.wantOK)
			}
		})
	}
}

// C2: Dest names the coordinator of each kind of entry and finding.
func TestC2DestNamesOneDestination(t *testing.T) {
	both := coord.Topology{Loop: "home", Homes: map[string]bool{"home": true, "a": true}, Old: "x"}
	for _, tc := range []struct {
		name         string
		t            coord.Topology
		project      string
		instanceWide bool
		want         string
	}{
		{"instance-wide work rings the loop project", both, "a", true, "home"},
		{"instance-wide work with no project", both, "", true, "home"},
		{"a project with a home rings its own", both, "a", false, "a"},
		{"the old session's project rings it", both, "x", false, "x"},
		{"a project with no coordinator rings the loop project", both, "b", false, "home"},
		{"no loop project: instance-wide work rings nobody", coord.Topology{Homes: map[string]bool{"a": true}}, "a", true, ""},
		{"no loop project: a project with no coordinator rings nobody", coord.Topology{Homes: map[string]bool{"a": true}}, "b", false, ""},
		{"no loop project: a home still rings its own", coord.Topology{Homes: map[string]bool{"a": true}}, "a", false, "a"},
		{"no old session: its former project rings the loop project", coord.Topology{Loop: "home"}, "x", false, "home"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.t.Dest(tc.project, tc.instanceWide); got != tc.want {
				t.Fatalf("Dest(%q, %v) over %+v = %q, want %q", tc.project, tc.instanceWide, tc.t, got, tc.want)
			}
		})
	}
}

// C2: for any generated topology and any project, Dest returns the one
// destination the rule names, and "" only when the host has no loop
// project.
func TestC2EveryEntryAndFindingHasExactlyOneDestination(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 15))
	names := []string{"", "home", "a", "b", "c", "x"}
	pick := func() string { return names[rng.IntN(len(names))] }
	for i := range 1000 {
		top := coord.Topology{Loop: pick(), Homes: map[string]bool{}, Old: pick()}
		for _, n := range names[1:] {
			if n != top.Old && rng.IntN(2) == 0 {
				top.Homes[n] = true
			}
		}
		for _, p := range names {
			for _, wide := range []bool{true, false} {
				want := top.Loop
				switch {
				case wide:
				case top.Homes[p]:
					want = p
				case top.Old != "" && p == top.Old:
					want = p
				}
				got := top.Dest(p, wide)
				if got != want {
					t.Fatalf("topology %d %+v: Dest(%q, %v) = %q, want %q", i, top, p, wide, got, want)
				}
				if got == "" && top.Loop != "" {
					t.Fatalf("topology %d %+v: Dest(%q, %v) = \"\" with loop project %q", i, top, p, wide, top.Loop)
				}
			}
		}
	}
}

// C3: a token with no project is unscoped, the loop project's token
// reaches as the loop project, and any other token reaches its project.
func TestC3CallerOfNamesATokensReach(t *testing.T) {
	for _, tc := range []struct {
		name    string
		project string
		t       coord.Topology
		want    coord.Reach
	}{
		{"no project is the old session", "", coord.Topology{Loop: "home"}, coord.ReachUnscoped},
		{"no project with no loop project", "", coord.Topology{}, coord.ReachUnscoped},
		{"the loop project", "home", coord.Topology{Loop: "home"}, coord.ReachLoop},
		{"another project", "a", coord.Topology{Loop: "home"}, coord.ReachProject},
		{"the former loop project after its removal", "home", coord.Topology{Homes: map[string]bool{"home": true}}, coord.ReachProject},
		{"the old session's project", "x", coord.Topology{Loop: "home", Old: "x"}, coord.ReachProject},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := coord.CallerOf(tc.project, tc.t, nil)
			if c.Reach != tc.want || c.Project != tc.project {
				t.Fatalf("CallerOf(%q) = reach %d, project %q, want reach %d, project %q", tc.project, c.Reach, c.Project, tc.want, tc.project)
			}
		})
	}
}

// C3: what each reach acts on. The loop project's token reaches its own
// project, every registration, and each record an entry routed to it
// names.
func TestC3CallerReachesItsRecords(t *testing.T) {
	top := coord.Topology{Loop: "home", Old: "x"}
	routedFault := coord.Record{Kind: coord.KindRun, ID: "r-1", Project: "b"}
	routed := func(_ context.Context, r coord.Record) (bool, error) { return r == routedFault, nil }
	for _, tc := range []struct {
		name    string
		project string
		r       coord.Record
		want    bool
	}{
		{"the old session reaches another project's item", "", coord.Record{Kind: coord.KindItem, ID: "b-1", Project: "b"}, true},
		{"the old session reaches a registration", "", coord.Record{Kind: coord.KindRegistration, ID: "b", Project: "b"}, true},
		{"a project reaches its own item", "a", coord.Record{Kind: coord.KindItem, ID: "a-1", Project: "a"}, true},
		{"a project reaches its own registration", "a", coord.Record{Kind: coord.KindRegistration, ID: "a", Project: "a"}, true},
		{"a project does not reach another project's item", "a", coord.Record{Kind: coord.KindItem, ID: "b-1", Project: "b"}, false},
		{"a project does not reach another registration", "a", coord.Record{Kind: coord.KindRegistration, ID: "b", Project: "b"}, false},
		{"a project does not reach a record routed to the loop project", "a", routedFault, false},
		{"a project does not reach another --project", "a", coord.Record{Kind: coord.KindProject, ID: "b", Project: "b"}, false},
		{"the loop project reaches its own item", "home", coord.Record{Kind: coord.KindItem, ID: "home-1", Project: "home"}, true},
		{"the loop project reaches every registration", "home", coord.Record{Kind: coord.KindRegistration, ID: "b", Project: "b"}, true},
		{"the loop project reaches a record an entry routed to it names", "home", routedFault, true},
		{"the loop project does not reach another record of that project", "home", coord.Record{Kind: coord.KindRun, ID: "r-2", Project: "b"}, false},
		{"the loop project does not reach another project's setting", "home", coord.Record{Kind: coord.KindSetting, ID: "b", Project: "b"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := coord.CallerOf(tc.project, top, routed).Reaches(context.Background(), tc.r)
			if err != nil || got != tc.want {
				t.Fatalf("token %q reaches %+v = %v, %v, want %v", tc.project, tc.r, got, err, tc.want)
			}
		})
	}
}

// C3: the loop project's token reports a failed read of the routed
// entries, and reaches nothing routed with no reader.
func TestC3LoopReachReportsTheRoutedReadsError(t *testing.T) {
	r := coord.Record{Kind: coord.KindRun, ID: "r-1", Project: "b"}
	fail := errors.New("the inbox is closed")
	_, err := coord.CallerOf("home", coord.Topology{Loop: "home"}, func(context.Context, coord.Record) (bool, error) { return false, fail }).
		Reaches(context.Background(), r)
	if !errors.Is(err, fail) {
		t.Fatalf("err = %v, want %v", err, fail)
	}
	got, err := coord.CallerOf("home", coord.Topology{Loop: "home"}, nil).Reaches(context.Background(), r)
	if got || err != nil {
		t.Fatalf("with no reader: reaches = %v, %v, want false", got, err)
	}
}
