package orca

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The background-resource ledger of a Codex rollout ports CodexResources
// (codex-resources.ts) and CodexCommands (codex-commands.ts) in alpha. It
// matches each tool call with its result, each yielded execution cell with
// its final wait, and each command launch with the native terminal item of
// its own process. A completed turn with a command still running is not a
// finished session. An unknown tool, item or shape keeps the session.

// codexTools are the tools whose lifetimes the ledger reads. Any other tool
// keeps the session.
var codexTools = []string{
	"exec", "wait", "exec_command", "write_stdin", "spawn_agent", "followup_task",
	"send_message", "wait_agent", "list_agents", "sleep",
}

var (
	codexChildTools      = []string{"spawn_agent", "followup_task", "send_message", "wait_agent", "list_agents"}
	codexPassiveItems    = []string{"Reasoning", "AgentMessage", "UserMessage", "ImageView"}
	codexPassiveExts     = []string{"clock.sleep", "web.search"}
	codexToolPrefix      = regexp.MustCompile(`^(?:functions|collaboration|clock)\.`)
	codexCellRunning     = regexp.MustCompile(`^Script running with cell ID (\S+)\s*$`)
	codexCellRunningFull = regexp.MustCompile(`^Script running with cell ID \S+\s*(?:\nWall time [0-9.]+ seconds\nOutput:\n)?$`)
	codexScriptCompleted = regexp.MustCompile(`^Script completed\n(?:Wall time [0-9.]+ seconds\nOutput:\n)?$`)
)

type codexCall struct {
	name    string
	input   any
	started int
}

type codexResources struct {
	calls    map[string]codexCall
	seen     map[string]bool
	cells    map[string]int
	commands codexCommands
	reason   string
}

func newCodexResources() *codexResources {
	return &codexResources{
		calls:    map[string]codexCall{},
		seen:     map[string]bool{},
		cells:    map[string]int{},
		commands: codexCommands{seen: map[string]bool{}, sessions: map[string]bool{}, ended: map[string]int{}},
	}
}

// refuse keeps the first failure, even when a later record looks complete.
func (r *codexResources) refuse(reason string) {
	if r.reason == "" {
		r.reason = reason
	}
}

// jsonObject reads a tool's structured argument or result envelope.
func jsonObject(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	s, ok := v.(string)
	if !ok {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil
	}
	return m
}

func isInteger(v any) bool {
	f, ok := v.(float64)
	return ok && !math.IsInf(f, 0) && f == math.Trunc(f)
}

// either is JavaScript's a ?? b.
func either(a, b any) any {
	if a != nil {
		return a
	}
	return b
}

// response reads one call or result of a response_item. Messages and
// reasoning belong to the turn reader.
func (r *codexResources) response(p map[string]any) {
	kind, id := str(p["type"]), str(p["call_id"])
	if kind == "function_call" || kind == "custom_tool_call" {
		name := codexToolPrefix.ReplaceAllString(str(p["name"]), "")
		if id == "" || r.seen[id] || !slices.Contains(codexTools, name) {
			r.refuse("Codex has an unknown or duplicate tool call")
			return
		}
		r.seen[id] = true
		if slices.Contains(codexChildTools, name) {
			r.refuse("Codex child resources require independently verified child rollouts")
		}
		input := either(p["arguments"], p["input"])
		args := jsonObject(input)
		switch name {
		case "exec":
			r.commands.wrapper(input)
		case "exec_command":
			cmd, ok := args["cmd"].(string)
			r.commands.start([]codexCommand{{launch: cmd}}, ok)
		case "write_stdin":
			session := args["session_id"]
			r.commands.start([]codexCommand{{poll: jsNumber(session)}}, isInteger(session))
		}
		r.calls[id] = codexCall{name: name, input: input, started: r.commands.order()}
		return
	}
	call, ok := r.calls[id]
	if (kind != "function_call_output" && kind != "custom_tool_call_output") || !ok {
		r.refuse("Codex has an unmatched or unknown tool result")
		return
	}
	delete(r.calls, id)
	r.result(call, p["output"])
}

func (r *codexResources) result(call codexCall, output any) {
	args := jsonObject(call.input)
	switch call.name {
	case "exec", "wait":
		cell := ""
		if call.name == "wait" {
			cell = str(args["cell_id"])
			if _, ok := r.cells[cell]; !ok {
				r.refuse("Codex waited on an unknown execution cell")
				return
			}
		}
		started, ok := r.cells[cell]
		if !ok {
			started = call.started
		}
		if text, ok := output.(string); ok {
			first, _, _ := strings.Cut(text, "\n")
			if m := codexCellRunning.FindStringSubmatch(first); m != nil {
				id := m[1]
				if !codexCellRunningFull.MatchString(text) {
					r.refuse("Codex has unstructured partial execution output")
				}
				if cell != "" && id != cell {
					r.refuse("Codex changed execution cell identity")
				}
				if _, live := r.cells[id]; cell == "" && live {
					r.refuse("Codex reused a live execution cell identity")
				}
				r.cells[id] = started
				return
			}
		}
		blocks, _ := output.([]any)
		var head map[string]any
		if len(blocks) > 0 {
			head, _ = blocks[0].(map[string]any)
		}
		if head == nil || head["type"] != "input_text" || !codexScriptCompleted.MatchString(str(head["text"])) {
			r.refuse("Codex has no terminal execution wrapper result")
			return
		}
		if cell != "" {
			delete(r.cells, cell)
		}
		for _, b := range blocks[1:] {
			block, _ := b.(map[string]any)
			typ := str(block["type"])
			switch {
			case block == nil || (typ != "input_text" && typ != "image" && typ != "input_image"):
				r.refuse("Codex has malformed execution result metadata")
			case typ == "input_text":
				if text, ok := block["text"].(string); ok {
					r.commands.observed(jsonObject(text), started)
				} else {
					r.refuse("Codex has malformed execution result metadata")
				}
			}
		}
	case "exec_command", "write_stdin":
		res := jsonObject(output)
		if _, session := res["session_id"]; res == nil || (!session && !isInteger(res["exit_code"])) {
			r.refuse("Codex has an unreadable command result")
			return
		}
		r.commands.observed(res, call.started)
	}
}

// item reads one native completed item. It describes the resource itself,
// not text a tool printed.
func (r *codexResources) item(v any, thread, session string) {
	item, ok := v.(map[string]any)
	if !ok {
		r.refuse("Codex has an unreadable native item")
		return
	}
	switch kind := str(item["type"]); {
	case slices.Contains(codexPassiveItems, kind):
	case kind == "CommandExecution":
		r.commands.complete(item, thread, session)
	case kind == "FileChange":
		if item["status"] != "completed" {
			r.refuse("Codex has an incomplete file change")
		}
	case kind == "SubAgentActivity" || kind == "CollabAgentToolCall":
		r.refuse("Codex child resources require independently verified child rollouts")
	case kind != "Extension" || !slices.Contains(codexPassiveExts, str(item["kind"])):
		r.refuse("Codex has an unknown native resource or monitor")
	}
}

// pending names what still runs, or "" when every resource settled.
func (r *codexResources) pending() string {
	switch {
	case r.reason != "":
		return r.reason
	case len(r.calls) > 0:
		return "Codex has a tool call without its result"
	case len(r.cells) > 0:
		return "Codex has an execution cell without its terminal result"
	}
	return r.commands.pending()
}

// codexCommands needs a native terminal execution identity for each
// enumerated command launch.
type codexCommands struct {
	launches    []string
	seen        map[string]bool
	sessions    map[string]bool
	ended       map[string]int
	reason      string
	unsupported bool
	unmatched   bool
}

func (c *codexCommands) fail(reason string) {
	if c.reason == "" {
		c.reason = reason
	}
}

// start registers each launch before any native item can settle it.
func (c *codexCommands) start(cmds []codexCommand, ok bool) {
	if !ok {
		c.fail("Codex has unreadable command arguments")
		return
	}
	for _, cmd := range cmds {
		if cmd.poll != "" {
			c.sessions[cmd.poll] = true
		} else {
			c.launches = append(c.launches, cmd.launch)
		}
	}
}

func (c *codexCommands) wrapper(source any) {
	src, ok := source.(string)
	if !ok {
		c.fail("Codex has an execution wrapper whose source could not be classified")
		return
	}
	cmds, ok := wrapperCommands(src)
	if !ok {
		c.unsupported = true
		return
	}
	c.start(cmds, true)
}

// complete settles one launch with a native terminal item. A poll
// notification or another thread's item cannot pay for a launch.
func (c *codexCommands) complete(item map[string]any, thread, session string) {
	id, process := str(item["id"]), str(item["process_id"])
	var command string
	isCommand := false
	if argv, ok := item["command"].([]any); ok && len(argv) > 0 {
		command, isCommand = argv[len(argv)-1].(string)
	}
	status := str(item["status"])
	if id == "" || c.seen[id] || process == "" || session == "" || !isCommand || thread != session ||
		item["source"] != "unified_exec_startup" || (status != "completed" && status != "failed") ||
		!isInteger(item["exit_code"]) {
		c.fail("Codex has an unmatched or duplicate native command outcome")
		return
	}
	c.seen[id] = true
	if at := slices.Index(c.launches, command); at >= 0 {
		c.launches = slices.Delete(c.launches, at, at+1)
	} else {
		c.unmatched = true
	}
	// A valid terminal item settles only its own process, even when syntax
	// hid its launch.
	c.ended[process] = len(c.seen)
}

// order is the native completion order when a tool call starts, before it
// can buffer output.
func (c *codexCommands) order() int { return len(c.seen) }

// observed accounts a returned session id. Truncated text proves nothing.
func (c *codexCommands) observed(res map[string]any, started int) {
	v, ok := res["session_id"]
	if !ok {
		return
	}
	if !isInteger(v) {
		c.fail("Codex has an unreadable returned command session")
		return
	}
	id := jsNumber(v)
	// Buffered output cannot reopen a command completed during its
	// originating call. A call started after completion can still report a
	// newly running session.
	if c.ended[id] <= started {
		delete(c.ended, id)
	}
	c.sessions[id] = true
}

func (c *codexCommands) pending() string {
	switch {
	case c.reason != "":
		return c.reason
	case len(c.launches) > 0:
		return "Codex has a command launch without its native terminal outcome"
	}
	for id := range c.sessions {
		if _, ok := c.ended[id]; !ok {
			return "Codex has a command session with no accounted native outcome"
		}
	}
	switch {
	case c.unmatched && !c.unsupported:
		return "Codex has an unmatched or duplicate native command outcome"
	case c.unsupported:
		return "Codex has an execution wrapper whose launches cannot be enumerated"
	}
	return ""
}

// jsNumber is JavaScript's String(n) for an integer that JSON decoded.
func jsNumber(v any) string {
	f, _ := v.(float64)
	return strconv.FormatFloat(f, 'f', -1, 64)
}
