package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/design"
	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/machine"
)

func registerCaptures(r *cli.Registry) error {
	return r.SetHandler("captures take", capturesTakeVerb)
}

// CapturesTake is the JSON of `loop captures take`.
type CapturesTake struct {
	Record   string    `json:"record"`
	Head     string    `json:"head"`
	Captures []Capture `json:"captures"`
}

// Capture is one cited state's capture that the verb wrote.
type Capture struct {
	State  string `json:"state"`
	Target string `json:"target"`
	File   string `json:"file"` // <out>/<state id>.png
}

func capturesTakeVerb(ctx context.Context, inv cli.Invocation) cli.Result {
	cwd, err := os.Getwd()
	if err != nil {
		return cli.Internal(err)
	}
	top, err := gitx.Run(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return cli.Reject(cli.CodeUsage, "not_a_checkout", "run loop captures take inside the checkout of a ticket's run or room")
	}
	root := strings.TrimSpace(string(top))
	var b brief.Brief
	raw, err := os.ReadFile(filepath.Join(root, brief.BriefJSON))
	if err == nil {
		err = json.Unmarshal(raw, &b)
	}
	if err != nil || b.Item == nil {
		return cli.Reject(cli.CodeUsage, "no_brief", "the checkout holds no "+brief.BriefJSON+" that names an item; run loop captures take inside the checkout of a ticket's run or room")
	}
	var cited []string
	for _, id := range strings.Split(b.Item.Facts[machine.FactScreens], ",") {
		if id = strings.TrimSpace(id); id != "" {
			cited = append(cited, id)
		}
	}
	if len(cited) == 0 {
		return cli.Reject(cli.CodeShapeRefused, "no_screens", b.Item.ID+" cites no state; its pushes take no captures")
	}
	var cfg *config.ProjectConfig
	raw, err = os.ReadFile(filepath.Join(root, config.ProjectConfigPath))
	if err == nil {
		cfg, err = config.ParseProjectConfig(raw)
	}
	if err != nil {
		return cli.Reject(cli.CodeShapeRefused, "no_design_config", "the checkout's "+config.ProjectConfigPath+" does not load: "+err.Error())
	}
	if cfg.Design == nil {
		return cli.Reject(cli.CodeShapeRefused, "no_design_config", "the checkout's "+config.ProjectConfigPath+" holds no design block, so it names no capture command")
	}
	dir := design.Dir(b.Item.Facts[machine.FactEpic])
	record := dir + design.RecordFile
	text, err := os.ReadFile(filepath.Join(root, record))
	if err != nil {
		return cli.Reject(cli.CodeShapeRefused, "record_invalid", "the checkout holds no "+record)
	}
	rec, problems := design.Parse(dir, string(text))
	if len(problems) > 0 {
		var faults []string
		for _, p := range problems {
			faults = append(faults, fmt.Sprintf("%s:%d: %s: %s", record, p.Line, p.Rule, p.Msg))
		}
		return cli.Reject(cli.CodeShapeRefused, "record_invalid", strings.Join(faults, "; "))
	}
	byID := map[string]design.State{}
	for _, s := range rec.Screens {
		for _, st := range s.States {
			byID[st.ID] = st
		}
	}
	var missing []string
	for _, id := range cited {
		if _, ok := byID[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return cli.Reject(cli.CodeShapeRefused, "state_not_in_record", fmt.Sprintf("%s holds no state %s", record, strings.Join(missing, ", ")))
	}

	status, err := gitx.Run(ctx, root, "status", "--porcelain")
	if err != nil {
		return cli.Internal(err)
	}
	if len(bytes.TrimSpace(status)) > 0 {
		return cli.Reject(cli.CodeShapeRefused, "checkout_dirty", "the checkout holds uncommitted changes, so a capture would not show the head; commit them first:\n"+string(status))
	}
	head, err := gitx.Run(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return cli.Internal(err)
	}

	out, err := filepath.Abs(inv.Flags["out"])
	if err != nil {
		return cli.Internal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return cli.Internal(err)
	}
	res := CapturesTake{Record: record, Head: strings.TrimSpace(string(head))}
	refuse := func(hint string) cli.Result {
		for _, c := range res.Captures {
			_ = os.Remove(c.File)
		}
		return cli.Reject(cli.CodeShapeRefused, "capture_failed", hint)
	}
	for _, id := range cited {
		st := byID[id]
		file := filepath.Join(out, id+".png")
		res.Captures = append(res.Captures, Capture{State: id, Target: st.Target, File: file})
		cmd := exec.CommandContext(ctx, "sh", "-c", cfg.Design.Capture)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "LOOP_DESIGN_TARGET="+st.Target, "LOOP_DESIGN_OUT="+file)
		// The envelope owns stdout, so the command's output goes to stderr.
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return refuse(fmt.Sprintf("%s: the capture command of target %s failed: %v", id, st.Target, err))
		}
		if png, err := os.ReadFile(file); err != nil || !bytes.HasPrefix(png, pngSignature) {
			return refuse(fmt.Sprintf("%s: the capture command of target %s wrote no PNG to LOOP_DESIGN_OUT", id, st.Target))
		}
	}
	return cli.Found(res)
}
