package server

import (
	"context"
	"errors"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

// C5: review submit reads the QA stage as on exactly when the project's
// config enables the stage, and as off when the config cannot be read.
func TestC5ReviewSubmitReadsTheProjectsQAStage(t *testing.T) {
	cases := []struct {
		name, doc string
		err       error
		want      bool
	}{
		{"listed", "stages:\n  qa: {}\n", nil, true},
		{"listed and off", "stages:\n  qa:\n    enabled: false\n", nil, false},
		{"not listed", "stages:\n  implementer: {}\n", nil, true},
		{"no config", "stages:\n  qa: {}\n", errors.New("no config at origin/main"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pc, err := config.ParseProjectConfig([]byte("merge:\n  mechanism: direct\n" + c.doc))
			if err != nil {
				t.Fatal(err)
			}
			var asked string
			source := func(_ context.Context, project string) (*config.ProjectSnapshot, error) {
				asked = project
				return &config.ProjectSnapshot{Config: pc}, c.err
			}
			if got := qaEnabled(source)(context.Background(), "alpha"); got != c.want || asked != "alpha" {
				t.Fatalf("qa stage of alpha = %v, read for %q; want %v", got, asked, c.want)
			}
		})
	}
}
