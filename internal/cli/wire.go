package cli

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	loopv1 "github.com/iantanwx/loop/gen/loop/v1"
)

// EnvelopeToProto converts a sealed envelope to its wire form.
func EnvelopeToProto(env Envelope) (*loopv1.Envelope, error) {
	pe := &loopv1.Envelope{
		Ok:            env.OK,
		Exit:          int32(env.Exit),
		Verb:          env.Verb,
		RequestId:     env.RequestID,
		ServerVersion: env.ServerVersion,
	}
	if env.Data != nil {
		raw, err := json.Marshal(env.Data)
		if err != nil {
			return nil, fmt.Errorf("encode envelope data: %w", err)
		}
		v := &structpb.Value{}
		if err := protojson.Unmarshal(raw, v); err != nil {
			return nil, fmt.Errorf("encode envelope data: %w", err)
		}
		pe.Data = v
	}
	if ref := env.Refusal; ref != nil {
		pe.Refusal = &loopv1.Refusal{
			Code: ref.Code, Rule: ref.Rule, Item: ref.Item,
			CurrentStatus: ref.CurrentStatus, ExpectedStatus: ref.ExpectedStatus,
			AllowedNext: ref.AllowedNext, Hint: ref.Hint,
			Retryable: ref.Retryable, RetryAfterSeconds: ref.RetryAfterSeconds,
		}
	}
	return pe, nil
}

// ResultFromProto turns a server's envelope back into a result that the
// CLI seals and prints. replayed comes from the call's trailer.
func ResultFromProto(pe *loopv1.Envelope, replayed bool) Result {
	res := Result{Exit: ExitCode(pe.GetExit()), ServerVersion: pe.GetServerVersion(), Replayed: replayed}
	if v := pe.GetData(); v != nil {
		if raw, err := protojson.Marshal(v); err == nil {
			var data any
			if json.Unmarshal(raw, &data) == nil {
				res.Data = data
			}
		}
	}
	if r := pe.GetRefusal(); r != nil {
		res.Refusal = &Refusal{
			Code: r.GetCode(), Rule: r.GetRule(), Item: r.GetItem(),
			CurrentStatus: r.GetCurrentStatus(), ExpectedStatus: r.GetExpectedStatus(),
			AllowedNext: r.GetAllowedNext(), Hint: r.GetHint(),
			Retryable: r.GetRetryable(), RetryAfterSeconds: r.GetRetryAfterSeconds(),
		}
	}
	return res
}

// EnvelopeFromJSON decodes a stored envelope.
func EnvelopeFromJSON(raw []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Envelope{}, err
	}
	if env.Refusal != nil && env.Code == "" {
		env.Refusal = nil
	}
	return env, nil
}
