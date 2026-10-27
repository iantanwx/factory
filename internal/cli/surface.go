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
	// readers read work items. A room reads its item too, and the CLI
	// reads the item's status before each write of a room (KTD13).
	readers  = []string{"run", "room", "coordinator"}
	hostOnly = []string{"host"}
	// hostOrCoordinator call a host verb: the operator's shell, and the
	// coordinator of the loop from its own checkout (factorio-obc). The
	// verb stays a host verb, so a run, a room and a tokenless caller
	// inside a checkout stay refused.
	hostOrCoordinator = []string{"host", "host coordinator"}
	// coordinatorOrHost read the instance's state: the coordinator, and
	// the operator's own shell.
	coordinatorOrHost = []string{"coordinator", "host"}
	// faultCallers may report and read loop faults: every agent, and the
	// operator's own shell.
	faultCallers = []string{"run", "room", "coordinator", "host"}
	anyone       = []string{"anyone"}
)

// Run finish outcomes: the values of `run finish --outcome`.
const (
	OutcomeDone    = "done"
	OutcomeFailed  = "failed"
	OutcomeBlocked = "blocked"
)

func arg(name, description string) Arg {
	return Arg{Name: name, Required: true, Description: description}
}

var (
	itemArg       = arg("item", "the work item id")
	runArg        = arg("run", "the run id")
	decisionArg   = arg("decision", "the decision id")
	entryArg      = arg("entry", "the inbox entry id")
	escalationArg = arg("escalation", "the escalation id")
	roomArg       = arg("room", "the room id")
	stageArg      = arg("stage", "the stage name")
	secretArg     = arg("name", "the secret name; the value never prints")
	projectArg    = arg("project", "the project id")
	faultArg      = arg("fault", "the fault id, F-<n>")
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
	// scopeFlag picks where secret set stores a value. A global value
	// reaches a run only when the project's manifest names it, and the
	// project's own value wins.
	scopeFlag = Flag{Name: "scope", Type: FlagString, Enum: []string{"project", "global"},
		Description: "project when absent; global stores one value that every project whose manifest names it uses"}
	bodyFlag = Flag{Name: "body", Type: FlagString, Required: true, Description: "the text to write"}
	// fixFlag makes loop doctor remove each .env that no live run holds (KTD17).
	fixFlag = Flag{Name: "fix", Type: FlagBool, Description: "remove each .env in a checkout with no live run"}
	// blockedByFlag names the items a new ticket waits on in backlog.
	blockedByFlag = Flag{Name: "blocked-by", Type: FlagString,
		Description: "item ids the ticket waits on, comma-separated; a ticket or a decision each"}
	// fileFlag names a file the CLI reads and sends with the call (ExtraFile).
	fileFlag = Flag{Name: "file", Type: FlagString, Required: true, Description: "the file to send, read by the CLI"}
)

var surface = []Verb{
	server("work", "peek", "name the next item a stage would take", runOrCoordinator, nil,
		Flag{Name: "stage", Type: FlagString, Required: true, Description: "the stage whose work query to run"}, projectFlag),
	server("work", "show", "show one work item", readers, []Arg{itemArg}, projectFlag),
	server("work", "list", "list work items", readers, nil, projectFlag,
		Flag{Name: "kind", Type: FlagString, Enum: []string{"ticket", "epic", "decision", "bug"}, Description: "only items of this kind"},
		Flag{Name: "status", Type: FlagString, Description: "only items in this status"},
		Flag{Name: "epic", Type: FlagString, Description: "only items whose epic fact names this id"}),
	write(server("work", "edit", "replace a work item's title or body; the change enters the item's retry identity", runOrCoordinator, []Arg{itemArg},
		Flag{Name: "title", Type: FlagString, Description: "the new title"},
		Flag{Name: "body", Type: FlagString, Description: "the new body, whole; it replaces the old body"},
		projectFlag)),
	write(server("work", "note", "add a note to a work item", runOrCoordinator, []Arg{itemArg}, bodyFlag, projectFlag)),
	write(server("work", "block", "park a todo ticket in backlog on a blocker, or add a blocker to a backlog ticket", runOrCoordinator, []Arg{itemArg},
		Flag{Name: "by", Type: FlagString, Required: true, Description: "the ticket or decision the item waits on"},
		projectFlag)),
	write(server("work", "unblock", "delete one blocks edge of a ticket, then move it from backlog to todo when no open blocker remains", runOrCoordinator, []Arg{itemArg},
		Flag{Name: "by", Type: FlagString, Description: "the blocker whose edge to delete; absent only for a ticket with no open blocker"},
		projectFlag)),
	write(server("work", "cancel", "cancel an escalated work item, or a bug report the reproducer routes as a loop fault", runOrCoordinator, []Arg{itemArg},
		Flag{Name: "reason", Type: FlagString, Required: true, Description: "why the item closes; the item keeps it, and a person reads it"},
		projectFlag)),
	write(server("work", "shape", "shape a bug into a ticket and close the bug as shaped; the answer names the ticket", runOrCoordinator, []Arg{itemArg},
		Flag{Name: "title", Type: FlagString, Required: true, Description: "the ticket title"},
		Flag{Name: "body", Type: FlagString, Required: true, Description: "the shaped ticket body"},
		Flag{Name: "priority", Type: FlagString, Required: true, Enum: []string{"1", "2", "3", "4"}, Description: "the ticket's priority by the reproducer runbook, section 4; the ticket keeps it as the fact priority"},
		blockedByFlag,
		projectFlag)),
	write(server("work", "ask", "ask a bug's reporter for a fact only the reporter holds; the report waits in asked, the inbox relays the question, and the answer returns it to reported; a report asks at most twice", runOrCoordinator, []Arg{itemArg},
		Flag{Name: "body", Type: FlagString, Required: true, Description: "one to three questions, each a top-level list item, in plain words for a person"},
		projectFlag)),
	write(server("work", "duplicate", "close a bug as a duplicate of another item; with --unbuilt, the bug waits for that item to build it instead", runOrCoordinator, []Arg{itemArg},
		Flag{Name: "of", Type: FlagString, Required: true, Description: "the item the bug repeats"},
		Flag{Name: "unbuilt", Type: FlagBool, Description: "the --of item, a ticket or an epic in flight, builds what the report asks for; the bug waits for it, and closes as its duplicate when it lands"},
		projectFlag)),
	write(server("work", "supersede", "close a ticket that a commit on the base, a done item or an ADR already answers; the server checks the evidence, and refuses a ticket with an open pull request or another run's lease", runOrCoordinator, []Arg{itemArg},
		Flag{Name: "commit", Type: FlagString, Description: "a commit on the base branch that already makes the ticket's change"},
		Flag{Name: "done", Type: FlagString, Description: "a done item that already answers the ticket"},
		Flag{Name: "adr", Type: FlagString, Description: "an ADR under docs/adr/ on the base branch that makes the ticket moot"},
		Flag{Name: "reason", Type: FlagString, Required: true, Description: "how the evidence answers the ticket; the ticket keeps it, and a person reads it"},
		projectFlag)),
	write(server("work", "create", "create a ticket: under an epic whose PRD merged, or PRD-less when the operator asked for it; or file the operator's bug report for the reproducer", runOrCoordinator, nil,
		Flag{Name: "kind", Type: FlagString, Enum: []string{"ticket", "bug"}, Description: "ticket when absent; bug files a raw report that the reproducer shapes into a ticket, the coordinator only, with --request"},
		Flag{Name: "title", Type: FlagString, Required: true, Description: "the ticket title, or the bug's one-line summary"},
		Flag{Name: "body", Type: FlagString, Description: "the ticket body, or the bug report: what the operator saw and how to see it again"},
		Flag{Name: "epic", Type: FlagString, Description: "the epic whose merged PRD the ticket implements; or, for the coordinator with --request and no --story, the architecture epic whose ADR the ticket writes"},
		Flag{Name: "story", Type: FlagString, Description: "the PRD story labels the ticket cites, comma-separated, for example US-01,US-02; needs --epic"},
		Flag{Name: "request", Type: FlagString, Description: "the operator's own words that ask for PRD-less work; the coordinator only"},
		blockedByFlag,
		projectFlag)),

	write(server("run", "finish", "report the run's outcome and release its lease", runOnly, nil,
		Flag{Name: "outcome", Type: FlagString, Required: true, Enum: []string{OutcomeDone, OutcomeFailed, OutcomeBlocked}, Description: "how the run ended"},
		Flag{Name: "fault", Type: FlagString, Description: "the loop fault that stopped the run, F-<n>; the run's record keeps it"})),
	server("run", "show", "show one run", coordinator, []Arg{runArg}),
	server("run", "list", "list runs, the newest first", coordinator, nil,
		Flag{Name: "project", Type: FlagString, Description: "only this project's runs; every project when absent"},
		Flag{Name: "stage", Type: FlagString, Description: "only runs of this stage"},
		Flag{Name: "live", Type: FlagBool, Description: "only runs that have not ended"},
		Flag{Name: "limit", Type: FlagInt, Description: "the most runs to list, 1 to 500; 50 when absent"}),
	server("run", "events", "list one run's events", coordinator, []Arg{runArg}),

	write(server("review", "submit", "submit a review verdict", []string{"reviewer run"}, []Arg{itemArg},
		Flag{Name: "verdict", Type: FlagString, Required: true, Enum: []string{"pass", "reject"}, Description: "pass or reject"},
		Flag{Name: "head", Type: FlagString, Description: "the PR head sha the review read; every pass needs it"},
		Flag{Name: "body", Type: FlagString, Description: "the review comment; the server posts it on the item's pull request and records it with the verdict"})),
	server("review", "threads", "list the pull request's comments, each review comment with its thread's resolved, outdated and awaiting-us state", []string{"reviewer run"}, []Arg{itemArg}),
	server("review", "rounds", "list review rounds", []string{"reviewer run"}, []Arg{itemArg}),
	client("review", "proof", "run the pull request's tests at the merge base in a throwaway worktree, remove the worktree whatever happens, and check that the reviewed checkout is still clean at its head", []string{"reviewer run"}, nil,
		Flag{Name: "test", Type: FlagString, Required: true, Description: "the test files to bring from the head, comma-separated paths inside the checkout"},
		Flag{Name: "bring", Type: FlagString, Description: "other files the tests need from the head, such as a fixture the pull request added; comma-separated"},
		Flag{Name: "base", Type: FlagString, Description: "the base ref; the proof runs at its merge base with HEAD; origin/<the brief's base branch> when absent"},
		Flag{Name: "run", Type: FlagString, Description: "the shell command that runs the tests, with the test paths in \"$@\"; the base config's test gate when absent"}),

	write(server("branch", "push", "push the item's branch through the server", runOrRoom, nil)),
	write(server("branch", "sync", "fetch the base and the item's branch into the mirror, and name the mirror path, each head sha and the refs a checkout fetches with no credential", runOrRoom, nil)),

	write(server("pr", "open", "open the item's pull request, or update the title and body of the one open on its branch, and hand the item to review", runOrRoom, nil,
		Flag{Name: "title", Type: FlagString, Required: true, Description: "the pull request title"},
		Flag{Name: "body", Type: FlagString, Description: "the pull request body; absent, the open pull request keeps its body"})),
	server("pr", "show", "show the item's pull request", runOrRoom, nil),
	server("pr", "files", "list the pull request's changed files", runOrRoom, nil),
	server("pr", "diff", "print the pull request's diff", runOrRoom, nil),
	server("pr", "threads", "list the pull request's comments, each review comment with its thread's resolved, outdated and awaiting-us state", runOrRoom, nil),
	write(server("pr", "reply", "reply on a pull request thread, or comment on the pull request", runOrRoom, nil, bodyFlag,
		Flag{Name: "thread", Type: FlagInt, Description: "the review comment id to reply to; a conversation comment when absent"})),
	server("pr", "checks", "list the pull request's checks", runOrRoom, nil),
	server("pr", "logs", "print the tail of each failed CI job's log", runOrRoom, nil,
		Flag{Name: "job", Type: FlagInt, Description: "one CI job id; every failed job of the head when absent"},
		Flag{Name: "queue", Type: FlagBool, Description: "read the failed jobs of the newest merge-queue draft that holds the head, not of the head"}),
	write(server("pr", "rerun", "rerun the pull request's failed checks", runOrRoom, nil)),
	server("pr", "pending", "list each open item pull request with a person's comment the App has not answered, oldest first; a project whose GitHub read fails answers one row with its error", coordinator, nil,
		Flag{Name: "project", Type: FlagString, Description: "only this project's pull requests; every project when absent"}),

	server("merge", "status", "list each pull request a merge mechanism holds, longest wait first: its state, since when, its hand-offs and its last dequeue reason", coordinatorOrHost, nil,
		Flag{Name: "project", Type: FlagString, Description: "only this project's pull requests; every project when absent"},
		Flag{Name: "all", Type: FlagBool, Description: "list the pull requests that left the merge pipeline too"}),

	write(server("escalation", "raise", "raise an escalation on the run's item, scored against the triage rubric", runOnly, nil,
		Flag{Name: "score", Type: FlagString, Required: true, Description: "the rubric level: 1 computed, 2 measured, 3 derived, 4 preference or 5 owner-only"},
		Flag{Name: "choice", Type: FlagString, Enum: []string{"inside", "outside"}, Description: "outside when one answer acts outside the repository; inside when absent"},
		Flag{Name: "body", Type: FlagString, Required: true, Description: "the finding and the evidence behind the score"})),
	server("escalation", "decisions", "list the operator's decisions recorded on the escalation's item: inbox answers, coordinator notes and answered questions", []string{"fixer"}, []Arg{escalationArg}),
	client("escalation", "judge", "run the triage panel in the fixer's process, with the operator's decisions recorded on the escalation's item, and write the panel file", []string{"fixer"}, []Arg{escalationArg},
		Flag{Name: "finding", Type: FlagString, Required: true, Description: "one finding, in one sentence"},
		Flag{Name: "out", Type: FlagString, Description: "the panel file to write; a new temporary file when absent"},
		Flag{Name: "rubric", Type: FlagString, Description: "the triage rubric; the pack's sdlc-triage skill in the checkout when absent"}),
	write(server("escalation", "route", "submit the panel file and route the escalation; a loop route may release one path out of the ticket's \"Must not change\" list", []string{"fixer"}, []Arg{escalationArg}, fileFlag,
		Flag{Name: "release", Type: FlagString, Description: "the one path a loop route moves out of \"Must not change\" and into \"May change\" in the item's body; it enters the retry identity"},
		Flag{Name: "forbids", Type: FlagString, Description: "with --release: the \"Must not change\" entry that covers the path, spelled as the body spells it"},
		Flag{Name: "condition", Type: FlagString, Description: "with --release: what the implementer may do at the path, and nothing wider"})),

	write(server("decision", "file", "file a decision that waits for its trigger", runOrCoordinator, nil, projectFlag,
		Flag{Name: "title", Type: FlagString, Required: true, Description: "the question"},
		Flag{Name: "trigger", Type: FlagString, Required: true, Description: "the event that forces the answer, in one sentence"},
		Flag{Name: "by", Type: FlagString, Description: "a date the trigger names, YYYY-MM-DD; decision-timers fires the decision on that day; exactly one of this and --waits-for-a-person"},
		Flag{Name: "waits-for-a-person", Type: FlagString, Description: "who or what the decision waits for; exactly one of --by and this"},
		Flag{Name: "now", Type: FlagBool, Description: "the trigger already fired: the decision waits on a person in the inbox at once; not with --by"},
		Flag{Name: "body", Type: FlagString, Description: "the evidence behind the question"})),
	write(server("decision", "fire", "fire a decision", runOrCoordinator, []Arg{decisionArg}, projectFlag,
		Flag{Name: "why", Type: FlagString, Required: true, Description: "why the trigger fired; the decision keeps it, and a person reads it"})),
	write(server("decision", "defer", "defer a decision", runOrCoordinator, []Arg{decisionArg}, projectFlag,
		Flag{Name: "why", Type: FlagString, Required: true, Description: "why the trigger did not fire; the decision keeps it"})),
	write(server("decision", "settle", "settle a decision", runOrCoordinator, []Arg{decisionArg}, projectFlag,
		Flag{Name: "note", Type: FlagString, Required: true, Description: "the answer, or why it was dropped with --dropped"},
		Flag{Name: "dropped", Type: FlagBool, Description: "close it as dropped, not answered"})),
	write(server("decision", "moot", "mark a decision moot", runOrCoordinator, []Arg{decisionArg}, projectFlag,
		Flag{Name: "note", Type: FlagString, Required: true, Description: "why the question no longer exists"})),

	server("overwatch", "sweep", "run every overwatch part once and print the report; it rings nobody", coordinatorOrHost, nil),

	write(server("fault", "report", "report a fault in the loop itself, not in the project; the answer names its fault id", faultCallers, nil,
		Flag{Name: "file", Type: FlagString, Required: true, Description: "the report file, at any path, read by the CLI: the command, its envelope and what you expected; 64 KiB at most"})),
	server("fault", "show", "show one loop fault: its status, its resolution and the release this host runs", faultCallers, []Arg{faultArg}),
	server("fault", "list", "list loop faults, newest first", faultCallers, nil,
		Flag{Name: "project", Type: FlagString, Description: "only faults filed from this project"},
		Flag{Name: "stage", Type: FlagString, Description: "only faults filed from this stage"},
		Flag{Name: "status", Type: FlagString, Enum: []string{"open", "acknowledged", "fixed", "duplicate", "not-a-fault"}, Description: "only faults in this status"}),
	write(server("fault", "ack", "acknowledge an open loop fault", coordinator, []Arg{faultArg})),
	write(server("fault", "resolve", "resolve a loop fault with exactly one resolution", coordinator, []Arg{faultArg},
		Flag{Name: "fixed-in", Type: FlagString, Description: "the release that fixes the fault"},
		Flag{Name: "duplicate-of", Type: FlagString, Description: "the fault this one repeats, F-<n>"},
		Flag{Name: "not-a-fault", Type: FlagBool, Description: "the report names no fault in the loop; needs --reason"},
		Flag{Name: "reason", Type: FlagString, Description: "why the report is not a fault"})),

	write(server("coordinator", "send", "type a message into the coordinator's terminal, as the operator would", hostOnly, nil,
		Flag{Name: "body", Type: FlagString, Required: true, Description: "the message"})),
	write(server("coordinator", "upgrade", "on the operator's decision, have the server run loop upgrade outside every checkout; it drains and restarts the server", coordinator,
		[]Arg{{Name: "version", Description: "the release to install; the latest when absent"}},
		Flag{Name: "body", Type: FlagString, Required: true, Description: "the operator's decision, in the operator's own words"})),

	server("inbox", "list", "list inbox entries", coordinator, nil,
		Flag{Name: "awaits", Type: FlagString, Enum: []string{"person", "coordinator"}, Description: "only entries that wait on this actor"}),
	write(server("inbox", "answer", "answer an inbox entry", coordinator, []Arg{entryArg},
		Flag{Name: "body", Type: FlagString, Required: true, Description: "the answer, as the operator gave it"},
		Flag{Name: "amend", Type: FlagBool, Description: "the answer amends the task, so it enters the item's inputs"})),
	write(server("inbox", "defer", "defer an inbox entry until a time", coordinator, []Arg{entryArg},
		Flag{Name: "until", Type: FlagTime, Required: true, Description: "when the entry returns, RFC 3339"},
		Flag{Name: "reason", Type: FlagString, Required: true, Description: "the act or the event the entry waits for"})),
	write(server("inbox", "prepare", "prepare a question for the operator", coordinator, nil,
		Flag{Name: "body", Type: FlagString, Required: true, Description: "the question, its options and their costs"},
		Flag{Name: "entry", Type: FlagString, Description: "the inbox entry the question is about"})),
	server("inbox", "wait", "wait for a new inbox entry; exit 4 means call again", coordinator, nil,
		Flag{Name: "since", Type: FlagString, Description: "the cursor of the last entry seen"}),
	write(server("inbox", "note", "record the root cause of an open alert; it stays open, and a repeat of it rings no one", coordinator, []Arg{entryArg},
		Flag{Name: "body", Type: FlagString, Required: true, Description: "the finding: the root cause, its evidence and the act that ends it"})),

	write(server("room", "open", "open a room for a finding the triage rubric routes to a person, or reopen an idle or interrupted one", coordinator, nil,
		Flag{Name: "kind", Type: FlagString, Required: true, Enum: []string{"intake", "decision", "review", "architect"}, Description: "the kind of room; an architect room serves one architecture epic"},
		Flag{Name: "project", Type: FlagString, Required: true, Description: "the project id"},
		Flag{Name: "item", Type: FlagString, Description: "the work item the room serves; an intake room may name none, and an architect room names an epic in architecture"},
		Flag{Name: "reason", Type: FlagString, Required: true, Description: "why the room opens; the room agent tells the operator first"},
		Flag{Name: "score", Type: FlagString, Required: true, Description: "the rubric level: 1 computed, 2 measured, 3 derived, 4 preference or 5 owner-only"},
		Flag{Name: "choice", Type: FlagString, Enum: []string{"inside", "outside"}, Description: "outside when one answer acts outside the repository; inside when absent"})),
	write(server("room", "apply", "apply the room's decision file", []string{"room"}, []Arg{roomArg}, fileFlag)),
	write(server("room", "close", "close a room", coordinator, []Arg{roomArg})),
	server("room", "list", "list rooms", coordinator, nil,
		Flag{Name: "all", Type: FlagBool, Description: "list closed rooms too"}),

	write(server("prd", "submit", "push the PRD commit, open its pull request and move its epic to review: an intake room's new epic, or the epic a prd-author run holds", []string{"intake room", "run"}, nil,
		Flag{Name: "path", Type: FlagString, Required: true, Description: "the PRD's path in the repository, committed at the checkout's HEAD"},
		Flag{Name: "title", Type: FlagString, Required: true, Description: "the epic and pull request title"},
		Flag{Name: "body", Type: FlagString, Description: "the pull request body"})),
	write(server("epic", "create", "create a PRD epic that waits for its PRD author, or an architecture epic that waits until an ADR whose Ticket line names it lands on the base branch; with blockers either waits in backlog", coordinator, nil,
		Flag{Name: "architecture", Type: FlagBool, Description: "the epic follows an ADR, not a PRD; with none, the epic is a PRD epic"},
		Flag{Name: "request", Type: FlagString, Description: "the operator's own words that ask for the work, kept verbatim; required with no --architecture"},
		Flag{Name: "blocked-by", Type: FlagString, Description: "item ids the epic waits on, comma-separated: tickets, epics or decisions of the project"},
		Flag{Name: "title", Type: FlagString, Required: true, Description: "the epic title"},
		Flag{Name: "body", Type: FlagString, Description: "what the epic delivers or the ADR decides, and why the operator asked for it"},
		projectFlag)),
	write(server("epic", "decompose", "create an epic's tickets in one batch", []string{"decomposer"}, nil,
		Flag{Name: "file", Type: FlagString, Required: true, Description: "the decomposition file: a JSON object whose tickets each hold key, title, body, stories and blocked_by"})),

	write(server("stage", "pause", "pause a run stage or an agent job: no new run of it starts, and a live run continues", coordinator, []Arg{stageArg},
		Flag{Name: "project", Type: FlagString, Description: "the project id; every project when absent"},
		Flag{Name: "reason", Type: FlagString, Description: "required: why the stage pauses"})),
	write(server("stage", "resume", "end the pause of one scope", coordinator, []Arg{stageArg},
		Flag{Name: "project", Type: FlagString, Description: "the project id of the pause; the pause of every project when absent"})),
	server("stage", "list", "list each run stage, agent job and native job of each project, enabled or not, and each instance job once: its schedule, limits, profiles, pauses, last and next fire and live runs", coordinatorOrHost, nil,
		Flag{Name: "project", Type: FlagString, Description: "only this project's rows; every project and the instance jobs when absent"}),

	server("secret", "check", "say whether a secret is present: the project's own value, or a global one", runOnly, []Arg{secretArg}, projectFlag),
	write(server("secret", "set", "store a secret; the value comes from stdin only", hostOnly, []Arg{secretArg},
		scopeFlag,
		Flag{Name: "project", Type: FlagString, Description: "the project id, or _host for a platform credential; needed with --scope project"})),
	write(server("secret", "import", "import secrets from the declared vaults", hostOnly, nil,
		Flag{Name: "project", Type: FlagString, Required: true, Description: "the project id"},
		Flag{Name: "branch", Type: FlagString, Description: "the branch that holds the manifest; the project's default branch when absent"})),

	write(server("project", "add", "register a project", hostOnly, []Arg{projectArg},
		Flag{Name: "repo", Type: FlagString, Required: true, Description: "the GitHub repository, owner/name"},
		Flag{Name: "path", Type: FlagString, Required: true, Description: "the project's local directory, an absolute path"},
		Flag{Name: "branch", Type: FlagString, Description: "the default branch, the base of every run; main when absent"})),
	server("project", "list", "list projects", hostOrCoordinator, nil),
	server("project", "show", "show one project", hostOrCoordinator, []Arg{projectArg}),
	write(server("project", "remove", "remove a project", hostOnly, []Arg{projectArg})),
	write(server("project", "move", "point a project at another repository or default branch, as after a transfer or a rename on GitHub; it keeps the project's secrets and history", hostOnly, []Arg{projectArg},
		Flag{Name: "repo", Type: FlagString, Required: true, Description: "the new GitHub repository, owner/name"},
		Flag{Name: "branch", Type: FlagString, Description: "the new default branch; the recorded one when absent"})),

	topLevel(client("host", "install", "install the service, set up the host and create the loop GitHub App", hostOnly, nil,
		Flag{Name: "check", Type: FlagBool, Description: "report drift and change nothing"},
		Flag{Name: "github-owner", Type: FlagString, Description: "the organisation that owns the new GitHub App; the operator's own account when absent"},
		Flag{Name: "no-github-app", Type: FlagBool, Description: "create no GitHub App"})),
	topLevel(client("host", "register", "tell the loop about the git clone in the current directory: its ruleset, its registration and the loop skills", hostOnly,
		[]Arg{{Name: "project", Description: "the project id; the repository name when absent"}})),
	topLevel(client("host", "upgrade", "install a signed release", hostOrCoordinator,
		[]Arg{{Name: "version", Description: "the release to install; the latest when absent"}})),
	topLevel(client("host", "rollback", "return to an earlier installed version", hostOrCoordinator, nil)),
	topLevel(client("host", "restore", "restore the database from a snapshot", hostOnly,
		[]Arg{{Name: "snapshot", Description: "the snapshot to restore; the newest when absent"}})),
	topLevel(write(server("host", "drain", "admit no new run and wait for in-flight runs", hostOnly, nil))),
	topLevel(client("host", "serve", "run the server", hostOnly, nil,
		Flag{Name: "instance", Type: FlagString, Description: "the instance directory; ~/.local/state/loop when absent"})),
	topLevel(client("host", "doctor", "check the host's setup", hostOrCoordinator, nil, fixFlag)),
	topLevel(server("host", "doctor-github", "run the GitHub checks of loop doctor on the server, which holds the credentials", hostOrCoordinator, nil)),
	topLevel(server("host", "doctor-env", "list each .env in a checkout with no live run; the server knows which runs live", hostOrCoordinator, nil, fixFlag)),

	topLevel(client("meta", "gates", "run the gates of the project checkout in the current directory: the loop's own gates, then the stack commands of .loop/config.yaml", anyone, nil,
		Flag{Name: "fast", Type: FlagBool, Description: "run every gate except test and build"},
		Flag{Name: "docs", Type: FlagBool, Description: "run only the gates that read documents, for a stage that writes only Markdown"},
		Flag{Name: "only", Type: FlagString, Description: "run one gate"},
		Flag{Name: "update", Type: FlagBool, Description: "lower the baseline of the one gate --only names to what it found; never raise a count"},
		Flag{Name: "list", Type: FlagBool, Description: "name the selected gates and run none"},
		Flag{Name: "base", Type: FlagString, Description: "the ref the change is compared with; the pull request's base in CI, else the remote's default branch"})),
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
// The machine package calls it from init. It panics on a duplicate
// kind, the same mistake RegisterTransitionTable refuses.
func RegisterSurfaceTable(kind string, table any) {
	for _, t := range surfaceTables {
		if t.kind == kind {
			panic(fmt.Sprintf("cli: transition table %q is registered twice", kind))
		}
	}
	surfaceTables = append(surfaceTables, surfaceTable{kind, table})
}
