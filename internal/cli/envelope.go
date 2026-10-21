package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

// Refusal is the reason for a non-zero answer. Callers branch on Code and
// Rule, never on Hint. AllowedNext names verbs, never states.
type Refusal struct {
	Code              string   `json:"code"`
	Rule              string   `json:"rule"`
	Item              string   `json:"item"`
	CurrentStatus     string   `json:"current_status"`
	ExpectedStatus    string   `json:"expected_status"`
	AllowedNext       []string `json:"allowed_next"`
	Hint              string   `json:"hint"`
	Retryable         bool     `json:"retryable"`
	RetryAfterSeconds float64  `json:"retry_after_seconds"`
}

// Result is what a handler returns. The registry seals it into an Envelope,
// so the refusal-code table, not the handler, decides exit and retryable.
type Result struct {
	Exit          ExitCode
	Data          any
	Refusal       *Refusal
	ServerVersion string
}

// Found is a result that did work or found work.
func Found(data any) Result { return Result{Exit: ExitDone, Data: data} }

// Nothing is an empty result: data [] with exit 1.
func Nothing() Result { return Result{Exit: ExitNothing, Data: []any{}} }

// List is exit 0 with the items, or Nothing when there are none.
func List[T any](items []T) Result {
	if len(items) == 0 {
		return Nothing()
	}
	return Found(items)
}

// Refuse is a refusal. Seal fills its exit code and retryable from the table.
func Refuse(r Refusal) Result { return Result{Refusal: &r} }

// Envelope is the one JSON object every invocation prints. A refusal's
// fields sit at the top level beside the envelope fields (KTD11).
type Envelope struct {
	OK            bool     `json:"ok"`
	Exit          ExitCode `json:"exit"`
	Verb          string   `json:"verb"`
	RequestID     string   `json:"request_id"`
	ServerVersion string   `json:"server_version"`
	Data          any      `json:"data"`
	*Refusal
}

// seal turns a handler's result into the envelope it prints. It keeps the
// refusal-code set closed: an unregistered code, or exit 2 or 3 without a
// code, becomes INTERNAL rather than reaching the caller.
func (r *Registry) seal(verb, requestID string, res Result) Envelope {
	env := Envelope{
		Verb:          verb,
		RequestID:     requestID,
		ServerVersion: res.ServerVersion,
		Data:          res.Data,
	}
	ref := res.Refusal
	if ref == nil && (res.Exit == ExitUndecided || res.Exit == ExitRefused) {
		ref = &Refusal{Code: CodeInternal, Rule: "missing_refusal",
			Hint: fmt.Sprintf("the handler answered exit %d without a refusal code", res.Exit)}
	}
	if ref == nil && (res.Exit < ExitDone || res.Exit > ExitWait) {
		ref = &Refusal{Code: CodeInternal, Rule: "bad_exit",
			Hint: fmt.Sprintf("the handler answered exit %d, which the table does not hold", res.Exit)}
	}
	if ref == nil {
		env.OK = true
		env.Exit = res.Exit
		if env.Exit == ExitNothing && isNilOrEmpty(env.Data) {
			env.Data = []any{}
		}
		return env
	}

	sealed := *ref
	code, ok := r.refusals[sealed.Code]
	if !ok {
		sealed = Refusal{Code: CodeInternal, Rule: "unregistered_code", Item: ref.Item,
			Hint: fmt.Sprintf("the handler refused with unregistered code %q: %s", ref.Code, ref.Hint)}
		code = r.refusals[CodeInternal]
	}
	sealed.Retryable = code.Retryable
	if sealed.AllowedNext == nil {
		sealed.AllowedNext = []string{}
	}
	env.Exit = code.Exit
	env.Data = nil
	env.Refusal = &sealed
	return env
}

func isNilOrEmpty(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		return rv.Len() == 0
	}
	return false
}

// writeEnvelope prints env as one line in a single write. os.Stdout is
// unbuffered, so the bytes have left the process when the write returns
// and the caller may exit at once.
func writeEnvelope(w io.Writer, env Envelope, text bool) error {
	var buf bytes.Buffer
	if text {
		renderText(&buf, env)
	} else {
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(env); err != nil {
			return err
		}
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// renderText is the human form behind --text. Agents never read it.
func renderText(buf *bytes.Buffer, env Envelope) {
	state := "ok"
	if !env.OK {
		state = "refused"
	}
	fmt.Fprintf(buf, "%s: %s (exit %d)\n", state, env.Verb, env.Exit)
	if ref := env.Refusal; ref != nil {
		fields := map[string]string{
			"code": ref.Code, "rule": ref.Rule, "item": ref.Item, "hint": ref.Hint,
			"current status": ref.CurrentStatus, "expected status": ref.ExpectedStatus,
			"allowed next": strings.Join(ref.AllowedNext, ", "),
		}
		keys := make([]string, 0, len(fields))
		for k, v := range fields {
			if v != "" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(buf, "%s: %s\n", k, fields[k])
		}
		if ref.Retryable {
			buf.WriteString("retryable: yes\n")
		}
		return
	}
	data, err := json.MarshalIndent(env.Data, "", "  ")
	if err != nil {
		fmt.Fprintf(buf, "%v\n", env.Data)
		return
	}
	buf.Write(data)
	buf.WriteByte('\n')
}
