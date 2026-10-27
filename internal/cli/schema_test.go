package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// runJSON runs one command line and decodes its envelope.
func runJSON(t *testing.T, r *Registry, args ...string) (map[string]any, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := r.Run(context.Background(), args, &out, &errOut)
	if errOut.Len() > 0 {
		t.Fatalf("stderr is for write failures only, got %q", errOut.String())
	}
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("loop %v printed no JSON envelope: %v\n%s", args, err, out.String())
	}
	if env["exit"] != float64(code) {
		t.Fatalf("envelope exit %v differs from process exit %d", env["exit"], code)
	}
	return env, code
}

func TestSchemaListsEveryVerbFlagExitAndRefusalCode(t *testing.T) {
	r := New()
	env, code := runJSON(t, r, "schema", "--json")
	if code != 0 || env["ok"] != true {
		t.Fatalf("schema failed: %v", env)
	}
	data := env["data"].(map[string]any)

	var paths []string
	for _, v := range data["verbs"].([]any) {
		verb := v.(map[string]any)
		paths = append(paths, verb["path"].(string))
		for _, key := range []string{"noun", "verb", "summary", "callers", "side", "rpc", "args", "flags", "top_level"} {
			if _, ok := verb[key]; !ok {
				t.Errorf("verb %s lacks %q", verb["path"], key)
			}
		}
	}
	for _, v := range r.verbs {
		if !slices.Contains(paths, v.Path) {
			t.Errorf("schema lacks verb %s", v.Path)
		}
	}
	if len(paths) != len(surface) {
		t.Errorf("schema lists %d verbs, surface holds %d", len(paths), len(surface))
	}

	peek := findVerb(t, data, "work peek")
	if flags := peek["flags"].([]any); len(flags) != 2 || flags[0].(map[string]any)["name"] != "stage" ||
		flags[1].(map[string]any)["name"] != "project" {
		t.Errorf("work peek flags = %v", flags)
	}
	if g := data["global_flags"].([]any); len(g) != len(globalFlags) {
		t.Errorf("global_flags = %v", g)
	}
	if e := data["exit_codes"].([]any); len(e) != 5 {
		t.Errorf("exit_codes = %v", e)
	}
	var listed []string
	for _, c := range data["refusal_codes"].([]any) {
		rc := c.(map[string]any)
		listed = append(listed, rc["code"].(string))
		if rc["grpc"] == "" || rc["exit"] == nil {
			t.Errorf("refusal code without mapping: %v", rc)
		}
	}
	for _, c := range r.RefusalCodes() {
		if !slices.Contains(listed, c.Code) {
			t.Errorf("schema lacks refusal code %s", c.Code)
		}
	}
	for _, key := range []string{"services", "transition_tables", "capabilities", "envelope", "version"} {
		if _, ok := data[key]; !ok {
			t.Errorf("schema lacks %q", key)
		}
	}
}

func findVerb(t *testing.T, schema map[string]any, path string) map[string]any {
	t.Helper()
	for _, v := range schema["verbs"].([]any) {
		if v.(map[string]any)["path"] == path {
			return v.(map[string]any)
		}
	}
	t.Fatalf("schema lacks %s", path)
	return nil
}

func TestHelpEqualsTheVerbsSliceOfTheSchema(t *testing.T) {
	r := New()
	schema, _ := runJSON(t, r, "schema")
	for _, v := range r.verbs {
		args := append(strings.Fields(v.Path), "--help")
		help, code := runJSON(t, r, args...)
		if code != 0 {
			t.Fatalf("%v exited %d: %v", args, code, help)
		}
		want := findVerb(t, schema["data"].(map[string]any), v.Path)
		gotJSON, _ := json.Marshal(help["data"])
		wantJSON, _ := json.Marshal(want)
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Errorf("%v help differs from its schema slice:\n got %s\nwant %s", args, gotJSON, wantJSON)
		}
	}
}

func TestHelpIgnoresMissingRequiredFlagsButNotUnknownOnes(t *testing.T) {
	r := New()
	if env, code := runJSON(t, r, "work", "peek", "--help"); code != 0 {
		t.Fatalf("help must not need --stage: %v", env)
	}
	if env, code := runJSON(t, r, "work", "peek", "--help", "--bogus"); code != 2 || env["code"] != CodeUsage {
		t.Fatalf("an unknown flag wins over --help: %v", env)
	}
}

func TestNounHelpAndRootHelp(t *testing.T) {
	r := New()
	env, code := runJSON(t, r, "pr", "--help")
	if code != 0 || len(env["data"].([]any)) != 9 {
		t.Fatalf("pr --help should list 9 verbs: %v", env)
	}
	env, code = runJSON(t, r, "--help")
	if code != 0 || env["data"].(map[string]any)["verbs"] == nil {
		t.Fatalf("--help alone prints the schema: %v", env)
	}
}

func TestEveryServerVerbHasExactlyOneRPC(t *testing.T) {
	if err := New().CheckRPCs(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRPCsCatchesAVerbWithoutAnRPC(t *testing.T) {
	r := New()
	must(r.RegisterVerb(Verb{Noun: "work", Name: "teleport", Summary: "s", Side: SideServer}))
	err := r.CheckRPCs()
	if err == nil || !strings.Contains(err.Error(), "WorkService/Teleport") {
		t.Fatalf("want a fault naming the missing RPC, got %v", err)
	}

	r = NewRegistry()
	err = r.CheckRPCs()
	if err == nil || !strings.Contains(err.Error(), "claimed by 0") {
		t.Fatalf("want a fault for RPCs no verb claims, got %v", err)
	}
}

func TestClientVerbsHaveNoRPC(t *testing.T) {
	r := New()
	for _, path := range []string{"escalation judge", "review proof", "install", "upgrade", "rollback", "restore", "serve", "doctor"} {
		v, ok := r.Verb(path)
		if !ok || v.Side != SideClient || v.RPC != "" {
			t.Errorf("%s should be a client verb without an RPC: %+v", path, v)
		}
	}
	if v, _ := r.Verb("drain"); v.RPC != "/loop.v1.HostService/Drain" {
		t.Errorf("drain RPC = %q", v.RPC)
	}
}

func TestRegisterVerbRefusals(t *testing.T) {
	cases := map[string]Verb{
		"duplicate":         {Noun: "work", Name: "peek", Summary: "s", Side: SideServer},
		"no side":           {Noun: "work", Name: "fly", Summary: "s"},
		"no summary":        {Noun: "work", Name: "fly", Side: SideClient},
		"upper case":        {Noun: "Work", Name: "fly", Summary: "s", Side: SideClient},
		"bad flag type":     {Noun: "work", Name: "fly", Summary: "s", Side: SideClient, Flags: []Flag{{Name: "x", Type: "float"}}},
		"shadows global":    {Noun: "work", Name: "fly", Summary: "s", Side: SideClient, Flags: []Flag{{Name: "text", Type: FlagBool}}},
		"noun is a verb":    {Noun: "schema", Name: "fly", Summary: "s", Side: SideClient},
		"verb is a noun":    {Noun: "meta", Name: "work", TopLevel: true, Summary: "s", Side: SideClient},
		"optional then req": {Noun: "work", Name: "fly", Summary: "s", Side: SideClient, Args: []Arg{{Name: "a"}, {Name: "b", Required: true}}},
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			if err := New().RegisterVerb(v); err == nil {
				t.Fatalf("RegisterVerb(%+v) succeeded", v)
			}
		})
	}
}

func TestLaterUnitsFillTransitionTablesAndCapabilities(t *testing.T) {
	r := New()
	table := map[string][]string{"todo": {"in_progress"}}
	must(r.RegisterTransitionTable("ticket", table))
	if err := r.RegisterTransitionTable("ticket", table); err == nil {
		t.Fatal("a second table for one kind must be refused")
	}
	if err := r.RegisterTransitionTable("bad", func() {}); err == nil {
		t.Fatal("a table that does not marshal must be refused")
	}
	must(r.SetCapabilityMatrix(map[string][]string{"run": {"work show"}}))
	if err := r.SetCapabilityMatrix(map[string]string{}); err == nil {
		t.Fatal("the capability matrix is set once")
	}
	env, _ := runJSON(t, r, "schema")
	data := env["data"].(map[string]any)
	if got := data["transition_tables"].(map[string]any)["ticket"]; got == nil {
		t.Fatalf("transition table missing: %v", data["transition_tables"])
	}
	if got := data["capabilities"].(map[string]any)["run"]; got == nil {
		t.Fatalf("capability matrix missing: %v", data["capabilities"])
	}
}

func TestEmptyRegistriesPrintStableKeys(t *testing.T) {
	var out bytes.Buffer
	New().Run(context.Background(), []string{"schema"}, &out, &bytes.Buffer{})
	for _, want := range []string{`"transition_tables":{}`, `"capabilities":null`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("schema lacks %s", want)
		}
	}
}

// C14: work list --help names the epic flag.
func TestC14WorkListHelpListsEpic(t *testing.T) {
	env, code := runJSON(t, New(), "work", "list", "--help")
	if code != 0 {
		t.Fatalf("work list --help exited %d: %v", code, env)
	}
	for _, f := range env["data"].(map[string]any)["flags"].([]any) {
		if f.(map[string]any)["name"] == "epic" {
			return
		}
	}
	t.Fatalf("work list --help lists no epic flag: %v", env["data"])
}
