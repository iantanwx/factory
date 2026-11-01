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
	"github.com/iantanwx/loop/internal/design"
	"github.com/iantanwx/loop/internal/gitx"
)

func registerDesign(r *cli.Registry) error {
	return r.SetHandler("design capture", designCaptureVerb)
}

// pngSignature is the first eight bytes of every PNG file.
var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// DesignCapture is the JSON of `loop design capture`.
type DesignCapture struct {
	Record   string            `json:"record"`
	Pin      string            `json:"pin"`
	Pictures []CapturedPicture `json:"pictures"`
}

// CapturedPicture is one state's picture that the verb wrote.
type CapturedPicture struct {
	State   string `json:"state"`
	Target  string `json:"target"`
	Picture string `json:"picture"` // the path in the checkout
}

// designCaptureVerb captures each state of the record of the design room
// whose checkout holds the working directory. It checks that the prototype
// worktree is clean at the record's pin, runs the capture command of the
// brief's design block once for each state, and writes each picture only
// when every capture gave a PNG. So each refusal writes no picture.
func designCaptureVerb(ctx context.Context, inv cli.Invocation) cli.Result {
	cwd, err := os.Getwd()
	if err != nil {
		return cli.Internal(err)
	}
	top, err := gitx.Run(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return cli.Reject(cli.CodeUsage, "not_a_checkout", "run loop design capture inside a design room's checkout")
	}
	root := strings.TrimSpace(string(top))
	var b brief.Brief
	raw, err := os.ReadFile(filepath.Join(root, brief.BriefJSON))
	if err == nil {
		err = json.Unmarshal(raw, &b)
	}
	if err != nil || b.Item == nil {
		return cli.Reject(cli.CodeUsage, "no_brief", "the checkout holds no "+brief.BriefJSON+" that names an item; run loop design capture inside a design room's checkout")
	}
	if b.Design == nil {
		return cli.Reject(cli.CodeShapeRefused, "no_design_config", "the brief names no design block; the project adds design: with run and capture to .loop/config.yaml, or the operator skips the design stage")
	}
	dir := design.Dir(b.Item.ID)
	record := dir + design.RecordFile
	text, err := os.ReadFile(filepath.Join(root, record))
	if err != nil {
		return cli.Reject(cli.CodeShapeRefused, "record_invalid", "the checkout holds no "+record+"; write the record before the capture")
	}
	rec, problems := design.Parse(dir, string(text))
	if len(problems) > 0 {
		var faults []string
		for _, p := range problems {
			faults = append(faults, fmt.Sprintf("%s:%d: %s: %s", record, p.Line, p.Rule, p.Msg))
		}
		return cli.Reject(cli.CodeShapeRefused, "record_invalid", strings.Join(faults, "; "))
	}
	var states []design.State
	for _, s := range rec.Screens {
		for _, st := range s.States {
			if one := inv.Flags["state"]; one == "" || one == st.ID {
				states = append(states, st)
			}
		}
	}
	if len(states) == 0 {
		return cli.Reject(cli.CodeUsage, "unknown_state", "the record holds no state "+inv.Flags["state"])
	}

	proto := inv.Flags["prototype"]
	if !filepath.IsAbs(proto) {
		proto = filepath.Join(cwd, proto)
	}
	head, err := gitx.Run(ctx, proto, "rev-parse", "HEAD")
	if err != nil {
		return cli.Reject(cli.CodeUsage, "not_a_checkout", "--prototype names "+proto+", which is no git worktree")
	}
	if h := strings.TrimSpace(string(head)); h != rec.Pin.Commit {
		return cli.Reject(cli.CodeShapeRefused, "pin_mismatch", fmt.Sprintf("the prototype's HEAD is %s and the record pins %s; check out the pin, or write the new commit on the **Prototype:** line", h, rec.Pin.Commit))
	}
	status, err := gitx.Run(ctx, proto, "status", "--porcelain")
	if err != nil {
		return cli.Internal(err)
	}
	if len(bytes.TrimSpace(status)) > 0 {
		return cli.Reject(cli.CodeShapeRefused, "prototype_dirty", "the prototype holds uncommitted changes; commit them and pin the new commit, or discard them:\n"+string(status))
	}

	tmp, err := os.MkdirTemp("", "loop-design-")
	if err != nil {
		return cli.Internal(err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	pngs := make([][]byte, len(states))
	for i, st := range states {
		out := filepath.Join(tmp, fmt.Sprintf("%d.png", i))
		cmd := exec.CommandContext(ctx, "sh", "-c", b.Design.Capture)
		cmd.Dir = proto
		cmd.Env = append(os.Environ(), "LOOP_DESIGN_TARGET="+st.Target, "LOOP_DESIGN_OUT="+out)
		// The envelope owns stdout, so the command's output goes to stderr.
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return cli.Reject(cli.CodeShapeRefused, "capture_failed", fmt.Sprintf("%s: the capture command of target %s failed: %v", st.ID, st.Target, err))
		}
		pngs[i], err = os.ReadFile(out)
		if err != nil || !bytes.HasPrefix(pngs[i], pngSignature) {
			return cli.Reject(cli.CodeShapeRefused, "capture_failed", fmt.Sprintf("%s: the capture command of target %s wrote no PNG to LOOP_DESIGN_OUT", st.ID, st.Target))
		}
	}
	res := DesignCapture{Record: record, Pin: rec.Pin.Commit}
	for i, st := range states {
		picture := dir + st.Picture
		if err := os.WriteFile(filepath.Join(root, picture), pngs[i], 0o644); err != nil {
			return cli.Internal(err)
		}
		res.Pictures = append(res.Pictures, CapturedPicture{State: st.ID, Target: st.Target, Picture: picture})
	}
	return cli.Found(res)
}
