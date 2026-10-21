package cli

import "fmt"

// The CLI surface: every noun and verb of the plan's surface table. A verb
// without a handler answers NOT_IMPLEMENTED until its unit lands. Later
// units add flags and arguments here as they implement each verb.

var (
	runOnly          = []string{"run"}
	coordinator      = []string{"coordinator"}
	runOrCoordinator = []string{"run", "coordinator"}
	runOrRoom        = []string{"run", "room"}
	hostOnly         = []string{"host"}
	anyone           = []string{"anyone"}
)

func arg(name, description string) Arg {
	return Arg{Name: name, Required: true, Description: description}
}

var (
	itemArg     = arg("item", "the work item id")
	runArg      = arg("run", "the run id")
	decisionArg = arg("decision", "the decision id")
	entryArg    = arg("entry", "the inbox entry id")
	roomArg     = arg("room", "the room id")
	stageArg    = arg("stage", "the stage name")
	secretArg   = arg("name", "the secret name; the value never prints")
	projectArg  = arg("project", "the project id")
)

func server(noun, name, summary string, callers []string, args []Arg, flags ...Flag) Verb {
	return Verb{Noun: noun, Name: name, Summary: summary, Callers: callers, Side: SideServer, Args: args, Flags: flags}
}

func client(noun, name, summary string, callers []string, args []Arg, flags ...Flag) Verb {
	return Verb{Noun: noun, Name: name, Summary: summary, Callers: callers, Side: SideClient, Args: args, Flags: flags}
}

func topLevel(v Verb) Verb {
	v.TopLevel = true
	return v
}

// write marks a verb that changes state (KTD13).
func write(v Verb) Verb {
	v.Write = true
	return v
}

var (
	projectFlag = Flag{Name: "project", Type: FlagString, Description: "the project id; a run's own project when absent"}
	bodyFlag    = Flag{Name: "body", Type: FlagString, Required: true, Description: "the text to write"}
)

var surface = []Verb{
	server("work", "peek", "name the next item a stage would take", runOrCoordinator, nil,
		Flag{Name: "stage", Type: FlagString, Required: true, Description: "the stage whose work query to run"}),
	server("work", "show", "show one work item", runOrCoordinator, []Arg{itemArg}, projectFlag),
	server("work", "list", "list work items", runOrCoordinator, nil, projectFlag,
		Flag{Name: "kind", Type: FlagString, Enum: []string{"ticket", "epic", "decision", "bug"}, Description: "only items of this kind"},
		Flag{Name: "status", Type: FlagString, Description: "only items in this status"}),
	write(server("work", "edit", "edit a work item's fields", runOrCoordinator, []Arg{itemArg})),
	write(server("work", "note", "add a note to a work item", runOrCoordinator, []Arg{itemArg}, bodyFlag, projectFlag)),
	write(server("work", "block", "block a work item", runOrCoordinator, []Arg{itemArg})),
	write(server("work", "unblock", "move a backlog item whose dependencies landed to todo", runOrCoordinator, []Arg{itemArg}, projectFlag)),
	write(server("work", "cancel", "cancel an escalated work item", runOrCoordinator, []Arg{itemArg}, projectFlag)),
	write(server("work", "create", "create a work item", runOrCoordinator, nil)),

	write(server("run", "finish", "report the run's outcome and release its lease", runOnly, nil,
		Flag{Name: "outcome", Type: FlagString, Required: true, Enum: []string{"done", "failed", "blocked"}, Description: "how the run ended"})),
	server("run", "show", "show one run", coordinator, []Arg{runArg}),
	server("run", "list", "list runs", coordinator, nil),
	server("run", "events", "list one run's events", coordinator, []Arg{runArg}),

	write(server("review", "submit", "submit a review verdict", []string{"reviewer run"}, []Arg{itemArg},
		Flag{Name: "verdict", Type: FlagString, Required: true, Enum: []string{"pass", "reject"}, Description: "pass or reject"},
		Flag{Name: "head", Type: FlagString, Description: "the PR head sha the review read; a ticket's pass needs it"})),
	server("review", "threads", "list review threads", []string{"reviewer run"}, []Arg{itemArg}),
	server("review", "rounds", "list review rounds", []string{"reviewer run"}, []Arg{itemArg}),

	write(server("branch", "push", "push the item's branch through the server", runOrRoom, nil)),
	write(server("branch", "sync", "bring the base branch into the checkout", runOrRoom, nil)),

	write(server("pr", "open", "open the item's pull request", runOrRoom, nil)),
	server("pr", "show", "show the item's pull request", runOrRoom, nil),
	server("pr", "files", "list the pull request's changed files", runOrRoom, nil),
	server("pr", "diff", "print the pull request's diff", runOrRoom, nil),
	server("pr", "threads", "list the pull request's threads", runOrRoom, nil),
	write(server("pr", "reply", "reply on a pull request thread", runOrRoom, nil, bodyFlag)),
	server("pr", "checks", "list the pull request's checks", runOrRoom, nil),
	server("pr", "logs", "print a failed check's log", runOrRoom, nil),
	write(server("pr", "rerun", "rerun the pull request's failed checks", runOrRoom, nil)),

	server("merge", "status", "show the merge mechanism's state", coordinator, nil),

	write(server("escalation", "raise", "raise an escalation on the run's item", runOnly, nil)),
	client("escalation", "judge", "run the triage panel in the fixer's process", []string{"fixer"}, nil),
	write(server("escalation", "route", "submit the panel's result and route the escalation", []string{"fixer"}, nil)),

	write(server("decision", "file", "file a decision", runOrCoordinator, nil)),
	write(server("decision", "fire", "fire a decision", runOrCoordinator, []Arg{decisionArg}, projectFlag)),
	write(server("decision", "defer", "defer a decision", runOrCoordinator, []Arg{decisionArg}, projectFlag)),
	write(server("decision", "settle", "settle a decision", runOrCoordinator, []Arg{decisionArg}, projectFlag,
		Flag{Name: "dropped", Type: FlagBool, Description: "close it as dropped, not answered"})),
	write(server("decision", "moot", "mark a decision moot", runOrCoordinator, []Arg{decisionArg}, projectFlag)),

	server("inbox", "list", "list inbox entries", coordinator, nil),
	write(server("inbox", "answer", "answer an inbox entry", coordinator, []Arg{entryArg})),
	write(server("inbox", "defer", "defer an inbox entry until a time", coordinator, []Arg{entryArg},
		Flag{Name: "until", Type: FlagTime, Required: true, Description: "when the entry returns, RFC 3339"})),
	write(server("inbox", "prepare", "prepare questions for the operator", coordinator, nil)),
	server("inbox", "wait", "wait for a new inbox entry; exit 4 means call again", coordinator, nil,
		Flag{Name: "since", Type: FlagString, Description: "the cursor of the last entry seen"}),

	write(server("room", "open", "open a room", coordinator, nil)),
	write(server("room", "apply", "apply the room's decision file", []string{"room"}, []Arg{roomArg})),
	write(server("room", "close", "close a room", coordinator, []Arg{roomArg})),
	server("room", "list", "list rooms", coordinator, nil),

	write(server("prd", "submit", "submit a PRD for review", []string{"intake room"}, nil)),
	write(server("epic", "decompose", "create an epic's tickets in one batch", []string{"decomposer"}, nil,
		Flag{Name: "file", Type: FlagString, Required: true, Description: "the decomposition file"})),

	write(server("stage", "pause", "pause a stage", coordinator, []Arg{stageArg})),
	write(server("stage", "resume", "resume a stage", coordinator, []Arg{stageArg})),

	server("secret", "check", "say whether a secret is present", runOnly, []Arg{secretArg}, projectFlag),
	write(server("secret", "set", "store a secret; the value comes from stdin only", hostOnly, []Arg{secretArg},
		Flag{Name: "project", Type: FlagString, Required: true, Description: "the project id, or _host for a platform credential"})),
	write(server("secret", "import", "import secrets from the declared vaults", hostOnly, nil,
		Flag{Name: "project", Type: FlagString, Required: true, Description: "the project id"},
		Flag{Name: "branch", Type: FlagString, Description: "the default branch that holds the manifest; main when absent"})),

	write(server("project", "add", "register a project", hostOnly, []Arg{projectArg},
		Flag{Name: "repo", Type: FlagString, Required: true, Description: "the GitHub repository, owner/name"},
		Flag{Name: "path", Type: FlagString, Required: true, Description: "the project's local directory, an absolute path"})),
	server("project", "list", "list projects", hostOnly, nil),
	server("project", "show", "show one project", hostOnly, []Arg{projectArg}),
	write(server("project", "remove", "remove a project", hostOnly, []Arg{projectArg})),

	topLevel(client("host", "install", "install the service unit", hostOnly, nil,
		Flag{Name: "check", Type: FlagBool, Description: "report drift and change nothing"})),
	topLevel(client("host", "upgrade", "install a signed release", hostOnly,
		[]Arg{{Name: "version", Description: "the release to install; the latest when absent"}})),
	topLevel(client("host", "rollback", "return to an earlier installed version", hostOnly, nil)),
	topLevel(client("host", "restore", "restore the database from a snapshot", hostOnly, nil)),
	topLevel(write(server("host", "drain", "admit no new run and wait for in-flight runs", hostOnly, nil))),
	topLevel(client("host", "serve", "run the server", hostOnly, nil,
		Flag{Name: "instance", Type: FlagString, Description: "the instance directory; ~/.local/state/loop when absent"})),
	topLevel(client("host", "doctor", "check the host's setup", hostOnly, nil)),

	topLevel(server("meta", "schema", "print the CLI schema", anyone, nil)),
	topLevel(server("meta", "status", "show the server's status", anyone, nil)),
	topLevel(server("meta", "version", "print the version", anyone, nil)),
}

// surfaceTable is one transition table that every Surface registers.
type surfaceTable struct {
	kind  string
	table any
}

var surfaceTables []surfaceTable

// RegisterSurfaceTable adds the transition table of one item kind to every
// registry Surface builds, so the server and the CLI print the same schema.
// The machine package (U5) calls it from init. It panics on a duplicate
// kind, the same mistake RegisterTransitionTable refuses.
func RegisterSurfaceTable(kind string, table any) {
	for _, t := range surfaceTables {
		if t.kind == kind {
			panic(fmt.Sprintf("cli: transition table %q is registered twice", kind))
		}
	}
	surfaceTables = append(surfaceTables, surfaceTable{kind, table})
}
