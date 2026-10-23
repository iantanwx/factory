package orca

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// These cases port alpha's codex-resource-settlement.test.ts. Each wrapper
// source here is one the Go classifier admits, or one alpha's classifier
// also rejects.

var codexAt = time.Date(2026, 10, 16, 7, 0, 0, 0, time.UTC)

type rec = map[string]any

func codexRow(typ string, payload rec) rec {
	return rec{"timestamp": codexAt.Format(time.RFC3339), "type": typ, "payload": payload}
}

func codexResponse(typ string, change rec) rec {
	p := rec{"type": typ}
	for k, v := range change {
		p[k] = v
	}
	return codexRow("response_item", p)
}

const echoOne = `text(await tools.exec_command({cmd:"echo one"}));`

func execCall(input, id string) rec {
	return codexResponse("custom_tool_call", rec{"name": "exec", "call_id": id, "input": input})
}

func blocks(texts ...string) []any {
	out := []any{rec{"type": "input_text", "text": "Script completed\n"}}
	for _, t := range texts {
		out = append(out, rec{"type": "input_text", "text": t})
	}
	return out
}

func execResult(output any, id string) rec {
	return codexResponse("custom_tool_call_output", rec{"call_id": id, "output": output})
}

func completedItem(item rec, thread string) rec {
	return codexRow("event_msg", rec{"type": "item_completed", "thread_id": thread, "item": item})
}

func native(change rec) rec {
	item := rec{
		"type":       "CommandExecution",
		"id":         "exec-one",
		"process_id": "7",
		"source":     "unified_exec_startup",
		"command":    []any{"/bin/zsh", "-lc", "echo one"},
		"status":     "completed",
		"exit_code":  0,
	}
	thread := "thread"
	for k, v := range change {
		if k == "thread" {
			thread = v.(string)
			continue
		}
		item[k] = v
	}
	return completedItem(item, thread)
}

func codexRun(t *testing.T, records ...rec) finish {
	t.Helper()
	all := []rec{
		codexRow("session_meta", rec{"id": "thread", "cwd": "/work"}),
		codexRow("event_msg", rec{"type": "task_started", "turn_id": "turn"}),
	}
	all = append(all, records...)
	all = append(all, codexRow("event_msg", rec{"type": "task_complete", "turn_id": "turn"}))
	var b strings.Builder
	for _, r := range all {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return codexFinished([]byte(b.String()), "/work")
}

func wantSettled(t *testing.T, f finish) {
	t.Helper()
	if !f.finished || !f.since.Equal(codexAt) {
		t.Fatalf("finish = %+v; want settled at %s", f, codexAt)
	}
}

func wantKept(t *testing.T, f finish, reason string) {
	t.Helper()
	if f.finished || (reason != "" && f.reason != reason) {
		t.Fatalf("finish = %+v; want kept with %q", f, reason)
	}
}

func TestCodexCommandRunningAfterTheTurnKeepsTheSession(t *testing.T) {
	// The turn completed, but the command it launched yielded a session and
	// has no native terminal outcome: it still runs.
	running := codexRun(t, execCall(echoOne, "call"), execResult(blocks(`{"session_id":7}`), "call"))
	wantKept(t, running, "Codex has a command launch without its native terminal outcome")
	direct := codexRun(t,
		codexResponse("function_call", rec{"name": "exec_command", "call_id": "d", "arguments": `{"cmd":"echo one"}`}),
		codexResponse("function_call_output", rec{"call_id": "d", "output": `{"session_id":7}`}))
	wantKept(t, direct, "Codex has a command launch without its native terminal outcome")
}

func TestCodexNativeOutcomeSettlesAnEnumeratedLaunch(t *testing.T) {
	wantKept(t, codexRun(t, execCall(echoOne, "call"), execResult(blocks(`{"exit_code":0}`), "call")),
		"Codex has a command launch without its native terminal outcome")
	for _, execution := range []rec{native(nil), native(rec{"status": "failed", "exit_code": 1})} {
		wantSettled(t, codexRun(t, execCall(echoOne, "call"), execution, execResult(blocks(`{"exit_code":0}`), "call")))
		wantSettled(t, codexRun(t, execCall(echoOne, "call"), execution, execResult(blocks(`{"outpu… truncated`), "call")))
	}
	wantSettled(t, codexRun(t,
		codexResponse("function_call", rec{"name": "exec_command", "call_id": "d", "arguments": `{"cmd":"echo one"}`}),
		native(nil),
		codexResponse("function_call_output", rec{"call_id": "d", "output": `{"exit_code":0}`})))
}

func TestCodexUnenumerableWrapperKeepsTheSession(t *testing.T) {
	for _, input := range []string{
		`store("x", await tools.exec_command({cmd:"sleep 20",yield_time_ms:1000}));text("Probe returned.");`,
		echoOne + ` await tools.exec_command({cmd:"sleep 20"});`,
		`for (let i=0;i<1;i++) text(await tools.exec_command({cmd:"echo one"}));`,
		"[redacted]",
		"",
		`text(await tools.exec_command({cmd:"echo " + x}));`,
		`text(await tools.exec_command({cmd:"echo one", cmd:"sleep 20"}));`,
		`text(await tools.exec_command({cmd:` + "`echo ${x}`" + `}));`,
		`text(await tools.exec_command({cmd:"echo one"}).output);`,
	} {
		out := codexRun(t, execCall(input, "call"), execResult(blocks(), "call"),
			execCall(echoOne, "later"), native(nil), execResult(blocks(), "later"))
		wantKept(t, out, "Codex has an execution wrapper whose launches cannot be enumerated")
	}
	out := codexRun(t, codexResponse("custom_tool_call", rec{"name": "exec", "call_id": "c", "input": rec{"x": 1}}),
		execResult(blocks(), "c"))
	wantKept(t, out, "Codex has an execution wrapper whose source could not be classified")
}

func TestCodexPrintedReceiptCannotReplaceANativeExecution(t *testing.T) {
	printed, err := json.Marshal(native(nil))
	if err != nil {
		t.Fatal(err)
	}
	out := codexRun(t, execCall(echoOne, "call"), execResult(blocks(string(printed)), "call"))
	wantKept(t, out, "Codex has a command launch without its native terminal outcome")
}

func TestCodexEachLaunchNeedsItsOwnNativeIdentity(t *testing.T) {
	twice := execCall(echoOne+"\n"+echoOne, "call")
	done := execResult(blocks(), "call")
	wantKept(t, codexRun(t, twice, native(nil), done), "")
	wantKept(t, codexRun(t, twice, native(nil), native(nil), done), "")
	wantSettled(t, codexRun(t, twice, native(nil), native(rec{"id": "exec-two"}), done))
}

func TestCodexForeignOrEarlierOutcomesCannotSettleALaunch(t *testing.T) {
	call, done := execCall(echoOne, "call"), execResult(blocks(), "call")
	for _, records := range [][]rec{
		{native(nil), call, done},
		{call, native(rec{"thread": "another-thread"}), done},
		{call, native(rec{"source": "unified_exec_poll"}), done},
		{call, native(rec{"command": []any{"/bin/zsh", "-lc", "another command"}}), done},
		{call, native(rec{"exit_code": nil}), done},
	} {
		wantKept(t, codexRun(t, records...), "")
	}
}

func TestCodexPollOfAnUnaccountedSessionKeepsTheSession(t *testing.T) {
	poll := func(id string) rec {
		return execCall(`text(await tools.write_stdin({session_id:`+id+`,chars:""}));`, "poll")
	}
	call, done := execCall(echoOne, "call"), execResult(blocks(), "call")
	wantSettled(t, codexRun(t, call, native(nil), done, poll("7"), execResult(blocks(), "poll")))
	wantKept(t, codexRun(t, call, native(nil), done, poll("8"), execResult(blocks(), "poll")),
		"Codex has a command session with no accounted native outcome")
}

func TestCodexYieldedWrapperNeedsItsFinalWait(t *testing.T) {
	call := execCall(echoOne, "call")
	running := execResult("Script running with cell ID 1\nWall time 1.0 seconds\nOutput:\n", "call")
	wait := codexResponse("function_call", rec{"name": "wait", "call_id": "wait", "arguments": `{"cell_id":"1"}`})
	waited := codexResponse("function_call_output", rec{"call_id": "wait", "output": blocks()})
	wantKept(t, codexRun(t, call, running, native(nil)), "Codex has an execution cell without its terminal result")
	wantSettled(t, codexRun(t, call, running, native(nil), wait, waited))
	wantKept(t, codexRun(t, call, running,
		execCall(echoOne, "two"), execResult("Script running with cell ID 1\n", "two"),
		native(nil), native(rec{"id": "exec-two"}), wait, waited),
		"Codex reused a live execution cell identity")
}

func TestCodexMalformedOrUnknownResourcesKeepTheSession(t *testing.T) {
	call := execCall(echoOne, "call")
	for _, records := range [][]rec{
		{call},
		{call, native(nil), execResult([]any{rec{"type": "input_text", "text": "Script failed\n"}}, "call")},
		{completedItem(rec{"type": "Extension", "kind": "monitor.start"}, "thread")},
		{call, native(nil), execResult(blocks(`{"session_id":99}`), "call")},
		{call, native(nil), execResult([]any{rec{"type": "input_text", "text": "Script completed\n"}, nil}, "call")},
		{call, native(nil), execResult([]any{
			rec{"type": "input_text", "text": "Script completed\n"},
			rec{"type": "input_text", "text": rec{"session_id": 7}},
		}, "call")},
		{
			codexResponse("function_call", rec{"name": "exec_command", "call_id": "direct", "arguments": `{"cmd":"echo one"}`}),
			native(nil),
			codexResponse("function_call_output", rec{"call_id": "direct", "output": "{}"}),
		},
		{codexResponse("function_call", rec{"name": "request_user_input_async", "call_id": "ask", "arguments": "{}"})},
		{codexResponse("function_call_output", rec{"call_id": "nobody", "output": "{}"})},
		{codexResponse("web_search_call", rec{"status": "completed"})},
		{completedItem(rec{"type": "FileChange", "status": "in_progress"}, "thread")},
	} {
		wantKept(t, codexRun(t, records...), "")
	}
	wantSettled(t, codexRun(t,
		completedItem(rec{"type": "Reasoning"}, "thread"),
		completedItem(rec{"type": "Extension", "kind": "clock.sleep"}, "thread"),
		completedItem(rec{"type": "FileChange", "status": "completed"}, "thread"),
		codexResponse("message", rec{"role": "assistant"}),
		codexResponse("reasoning", rec{}),
		codexResponse("function_call", rec{"name": "sleep", "call_id": "s", "arguments": "{}"}),
		codexResponse("function_call_output", rec{"call_id": "s", "output": "slept"})))
}

func TestCodexChildResourcesKeepTheSession(t *testing.T) {
	child := func(kind string) rec {
		return completedItem(rec{"type": "SubAgentActivity", "kind": kind, "agent_path": "/root/child", "agent_thread_id": "child-thread"}, "thread")
	}
	spawn := codexResponse("function_call", rec{"name": "spawn_agent", "call_id": "spawn", "arguments": "{}"})
	spawned := codexResponse("function_call_output", rec{"call_id": "spawn", "output": `{"task_name":"/root/child"}`})
	follow := codexResponse("function_call", rec{"name": "followup_task", "call_id": "follow", "arguments": `{"target":"/root/child"}`})
	followed := codexResponse("function_call_output", rec{"call_id": "follow", "output": "sent"})
	for _, records := range [][]rec{
		{spawn, child("started"), spawned},
		{spawn, child("started"), spawned, child("completed")},
		{child("completed")},
		{follow, child("interacted"), followed, child("completed")},
		{completedItem(rec{"type": "CollabAgentToolCall", "tool": "wait", "status": "completed"}, "thread")},
	} {
		wantKept(t, codexRun(t, records...), "Codex child resources require independently verified child rollouts")
	}
}

func TestCodexPollKeepsOutcomesNewerThanItsCall(t *testing.T) {
	poll := execCall(`text(await tools.write_stdin({session_id:7,chars:""}));`, "poll")
	delayed := blocks(`{"session_id":7}`)
	launched := []rec{execCall(echoOne, "call"), execResult(blocks(`{"session_id":7}`), "call")}
	wantSettled(t, codexRun(t, append(launched[:2:2], poll, native(nil), execResult(delayed, "poll"))...))
	wantKept(t, codexRun(t, append(launched[:2:2], native(nil), poll, execResult(delayed, "poll"))...),
		"Codex has a command session with no accounted native outcome")
	wait := codexResponse("function_call", rec{"name": "wait", "call_id": "wait", "arguments": `{"cell_id":"1"}`})
	wantSettled(t, codexRun(t, append(launched[:2:2],
		poll, execResult("Script running with cell ID 1\n", "poll"), native(nil), wait,
		codexResponse("function_call_output", rec{"call_id": "wait", "output": delayed}))...))
}

func TestCodexWrapperCommands(t *testing.T) {
	cases := []struct {
		src  string
		want []codexCommand
	}{
		{echoOne, []codexCommand{{launch: "echo one"}}},
		{`text(await tools.exec_command({"cmd":'a\'b\nA\x42',"max_output_tokens":2000,yield_time_ms:1e3,flags:[true,null,-1,{}]}));`,
			[]codexCommand{{launch: "a'b\nAB"}}},
		{"text(await tools.exec_command({cmd:`multi\nline`}));\n\n text( await tools.write_stdin( { session_id : 65924 , chars : \"\" } ) ) ;",
			[]codexCommand{{launch: "multi\nline"}, {poll: "65924"}}},
		{"// @exec: {\"yield_time_ms\": 1000}\ntext((await tools.exec_command({cmd:\"a\"})).output); /* b\n */ text(await tools.write_stdin({session_id:7,chars:\"\"}))",
			[]codexCommand{{launch: "a"}, {poll: "7"}}},
		{"text(await tools.apply_patch(\"*** Begin Patch\\n*** End Patch\"));\nimage((await tools.view_image({path:\"/tmp/a.png\"})).image_url);\n" + echoOne,
			[]codexCommand{{launch: "echo one"}}},
		{"text(await tools.apply_patch(`*** Begin Patch\n*** End Patch`));", nil},
	}
	for _, c := range cases {
		got, ok := wrapperCommands(c.src)
		if !ok || len(got) != len(c.want) {
			t.Fatalf("wrapperCommands(%q) = %+v, %v; want %+v", c.src, got, ok, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("wrapperCommands(%q)[%d] = %+v; want %+v", c.src, i, got[i], c.want[i])
			}
		}
	}
	for _, src := range []string{
		`text(await tools.write_stdin({session_id:7.5,chars:""}));`,
		`text(await tools.write_stdin({session_id:"7",chars:""}));`,
		`text(await tools.exec_command({cmd:1}));`,
		`text(await tools.exec_command({}));`,
		`text(await tools.exec_command({cmd:"a"}, {}));`,
		`text(tools.exec_command({cmd:"a"}));`,
		`text(await tools.exec_command({cmd:"a"})) text(await tools.exec_command({cmd:"b"}));`,
		`text(await tools.exec_command({cmd:"a\q"}));`,
		`text(await tools.exec_command({__proto__:"a",cmd:"b"}));`,
		`text(await tools.exec_command({[k]:"a",cmd:"b"}));`,
		`text(await tools.exec_command({cmd}));`,
		`/* c text(await tools.exec_command({cmd:"a"}));`,
		`text(await tools.exec_command({cmd:"a"}).output);`,
		`text((await tools.exec_command({cmd:"a"})).stdout);`,
		`text(await tools.apply_patch(patch));`,
		"text(await tools.apply_patch(`${patch}`));",
		`image((await tools.view_image({path:p})).image_url);`,
		`image((await tools.exec_command({cmd:"a"})).image_url);`,
		`// only a comment`,
	} {
		if got, ok := wrapperCommands(src); ok {
			t.Fatalf("wrapperCommands(%q) = %+v; want unenumerable", src, got)
		}
	}
}
