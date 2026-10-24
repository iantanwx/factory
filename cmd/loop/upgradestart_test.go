package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/server"
)

// factorio-8qm: on Linux the server runs `loop upgrade` as a transient
// systemd user unit, outside its own service, whose stop would kill it,
// and in the home directory, outside every checkout.
func TestUpgradeStarterRunsATransientUnitOutsideEveryCheckout(t *testing.T) {
	var got []string
	u := upgradeStarter{instance: "/state/loop", goos: "linux",
		self: func() (string, error) { return "/state/loop/current/loop", nil },
		home: func() (string, error) { return "/home/op", nil },
		env:  func(k string) string { return map[string]string{"PATH": "/usr/bin", "HOME": "/home/op"}[k] },
		run: func(_ context.Context, name string, args ...string) error {
			got = append([]string{name}, args...)
			return nil
		},
		unitName: func(string) string { return "loop-upgrade-abcd" }}
	st, err := u.Start(context.Background(), "v0.0.45")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"systemd-run", "--user", "--collect", "--quiet", "--unit", "loop-upgrade-abcd",
		"--working-directory", "/home/op", "--setenv", "LOOP_INSTANCE=/state/loop", "--setenv", "PATH", "--setenv", "HOME",
		"/state/loop/current/loop", "upgrade", "v0.0.45"}
	if !slices.Equal(got, want) || !slices.Equal(st.Command, want) || st.Unit != "loop-upgrade-abcd" {
		t.Fatalf("ran %q, answered %+v\nwant %q", got, st, want)
	}
	// The latest release when no version is named.
	if _, err := u.Start(context.Background(), ""); err != nil || got[len(got)-1] != "upgrade" {
		t.Fatalf("ran %q, %v", got, err)
	}
	// No value of the environment lands in the command line.
	if strings.Contains(strings.Join(got, " "), "/usr/bin") {
		t.Fatalf("the command line holds an environment value: %q", got)
	}
}

func TestUpgradeStarterRefusesAVersionThatNamesNoRelease(t *testing.T) {
	u := upgradeStarter{instance: "/state/loop", goos: "linux",
		run: func(context.Context, string, ...string) error { t.Fatal("ran a command"); return nil }}
	for _, v := range []string{"--help", "latest", "v1"} {
		if _, err := u.Start(context.Background(), v); !errors.Is(err, server.ErrBadVersion) {
			t.Fatalf("%q: %v, want ErrBadVersion", v, err)
		}
	}
}

func TestProductionOptionsStartUpgradesOnTheCoordinatorsRequest(t *testing.T) {
	opts, cleanup, err := productionOptions(context.Background(), t.TempDir(), wiring{})
	defer cleanup()
	if err != nil || opts.Upgrade == nil {
		t.Fatalf("the production server cannot start an upgrade: %v", err)
	}
}
