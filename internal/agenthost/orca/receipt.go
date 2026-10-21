package orca

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"

	"github.com/iantanwx/loop/internal/agenthost"
)

// ModelReceipt reads the model that the harness itself recorded for the
// session, and compares it with the requested model. Ported from
// claudeModel and codexModel in alpha's run-model.ts.
//
// The model reaches the harness as a --model argument in the terminal's
// command, and orca never sees it. So only the harness's own record proves
// the model: Claude Code's message.model on the first real assistant
// record, Codex's payload.model on the first turn_context. A record that
// names no model yet, another model, or a model alias that differs from the
// request reads Unverified. The comparison is exact, so stage config must
// name the full model id the harness records.
func (h *Host) ModelReceipt(_ context.Context, s agenthost.Session) (agenthost.ModelReceipt, error) {
	rc := agenthost.ModelReceipt{Status: agenthost.Unverified, Requested: s.Model}
	ts, err := h.transcripts(s)
	if err != nil {
		rc.Reason = err.Error()
		return rc, err
	}
	slices.SortFunc(ts, func(a, b transcript) int { return b.newest.Compare(a.newest) })
	for _, t := range ts {
		if m := recordedModel(s.Harness, t.text, filepath.Clean(s.Checkout)); m != "" {
			rc.Observed = m
			break
		}
	}
	switch rc.Observed {
	case "":
		rc.Reason = "no transcript of this session names a model yet"
	case s.Model:
		rc.Status = agenthost.Verified
	default:
		rc.Reason = "the harness records another model"
	}
	return rc, nil
}

func recordedModel(h agenthost.Harness, text []byte, dir string) string {
	for line := range bytes.Lines(text) {
		var r struct {
			Type    string `json:"type"`
			Cwd     string `json:"cwd"`
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
			Payload struct {
				Cwd   string `json:"cwd"`
				Model string `json:"model"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		switch {
		case h == agenthost.Claude && r.Type == "assistant" && inside(r.Cwd, dir):
			if m := r.Message.Model; m != "" && m != "<synthetic>" {
				return m
			}
		case h == agenthost.Codex && r.Type == "turn_context" && inside(r.Payload.Cwd, dir):
			if r.Payload.Model != "" {
				return r.Payload.Model
			}
		}
	}
	return ""
}
