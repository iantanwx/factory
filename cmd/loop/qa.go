package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/qa"
)

func registerQA(r *cli.Registry) error {
	return r.SetHandler("qa plan", qaPlanVerb)
}

// qaPlanVerb prints the plan of the QA run whose checkout holds the
// working directory: the verify skills of the checkout, and the "May
// change" entries and the design skill of its brief.
func qaPlanVerb(ctx context.Context, _ cli.Invocation) cli.Result {
	cwd, err := os.Getwd()
	if err != nil {
		return cli.Internal(err)
	}
	root, err := gitx.Run(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return cli.Reject(cli.CodeUsage, "not_a_checkout", "run loop qa plan inside a run's checkout")
	}
	dir := strings.TrimSpace(string(root))
	raw, err := os.ReadFile(filepath.Join(dir, brief.BriefJSON))
	if err != nil {
		return cli.Reject(cli.CodeUsage, "no_brief", "the checkout holds no "+brief.BriefJSON+"; run loop qa plan inside a run's checkout")
	}
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil {
		return cli.Internal(err)
	}
	apps, err := qa.Apps(os.DirFS(dir))
	if err != nil {
		return cli.Reject(cli.CodeUsage, "bad_verify_skill", err.Error())
	}
	var mayChange []string
	if b.Item != nil {
		mayChange = admission.MayChangeEntries(b.Item.Body)
	}
	return cli.Found(qa.MakePlan(apps, mayChange, b.DesignSkill))
}
