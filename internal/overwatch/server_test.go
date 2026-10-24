package overwatch

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/iantanwx/loop/internal/github"
)

// sweepServer sweeps the server part alone over a fake doctor, and
// returns its report.
func sweepServer(t *testing.T, lines []github.CheckLine, err error) PartReport {
	t.Helper()
	w := newReachWorld(t)
	w.lines, w.doctorErr = lines, err
	r := Sweep(context.Background(), w, []Part{serverPart()})
	if len(r.Parts) != 1 || r.Parts[0].Name != "server" {
		t.Fatalf("report = %+v", r)
	}
	return r.Parts[0]
}

// C19: a failed doctor line, from the install drift check or from the
// GitHub checks, makes server hold work, and the line quotes the check's
// detail. All lines passed read code 0.
func TestC19HostDriftIsWork(t *testing.T) {
	passed := []github.CheckLine{
		{Check: "install", OK: true, Detail: "the service, binaries, host config, key file and credential match loop install"},
		{Check: "factorio/installation_active", OK: true, Detail: "the installation is active"},
		{Check: "factorio/project_config", OK: true, Detail: ".loop/config.yaml exists on main"},
	}
	cases := []struct {
		name  string
		lines []github.CheckLine
		want  PartReport
	}{
		{name: "all passed", lines: passed, want: PartReport{Name: "server", Code: Quiet, Command: "loop doctor", Lines: []string{
			"all 3 doctor checks passed",
		}}},
		{name: "install drift", lines: []github.CheckLine{
			{Check: "install", Detail: "run loop install: /srv/loop/bin/bd is not the pinned bd v1.4.0"},
			passed[1], passed[2],
		}, want: PartReport{Name: "server", Code: Work, Command: "loop doctor", Lines: []string{
			"install failed: run loop install: /srv/loop/bin/bd is not the pinned bd v1.4.0 (overwatch:server:install)",
		}}},
		{name: "a GitHub check", lines: []github.CheckLine{
			passed[0],
			{Check: "factorio/installation_required_permissions", Detail: "grant the loop App workflows:write"},
			{Check: "alpha/project_config", Detail: "merge .loop/config.yaml to main; loop project add proposes it"},
		}, want: PartReport{Name: "server", Code: Work, Command: "loop doctor", Lines: []string{
			"factorio/installation_required_permissions failed: grant the loop App workflows:write (overwatch:server:factorio/installation_required_permissions)",
			"alpha/project_config failed: merge .loop/config.yaml to main; loop project add proposes it (overwatch:server:alpha/project_config)",
		}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sweepServer(t, c.lines, nil); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("server =\n%#v\nwant\n%#v", got, c.want)
			}
		})
	}
}

// C19: a question that names the key ends the finding.
func TestC19AQuestionThatNamesTheKeyFilesIt(t *testing.T) {
	w := newReachWorld(t)
	w.lines = []github.CheckLine{{Check: "install", Detail: "run loop install: the key file cannot be used"}}
	w.question(t, "Run loop install for overwatch:server:install?", false)
	r := Sweep(context.Background(), w, []Part{serverPart()})
	if r.Code != Quiet || len(r.Parts[0].Lines) == 0 || r.Parts[0].Lines[0] !=
		"install failed: run loop install: the key file cannot be used (overwatch:server:install) (filed in question 1)" {
		t.Fatalf("server = %#v", r.Parts[0])
	}
}

// C19: a doctor that cannot run makes the part unread, not quiet.
func TestC19AnUnreadDoctorIsUnread(t *testing.T) {
	got := sweepServer(t, nil, errors.New("the install check could not run"))
	if got.Code != Unread || len(got.Lines) != 1 || got.Lines[0] != "UNREAD: the install check could not run" {
		t.Fatalf("server = %#v", got)
	}
}
