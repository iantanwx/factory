package gates

import (
	"context"
	"slices"
	"strings"
	"testing"
)

const trailer = "Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"

func msgRules(message string) []string { return rules(checkMessage(message)) }

func lines(ls ...string) string { return strings.Join(ls, "\n") }

func subjectOfLength(n int) string {
	return "feat(sdlc): " + strings.Repeat("x", n-len("feat(sdlc): "))
}

func moodClause(subject string) string {
	if m := moodFinding(subject); m != nil {
		return m.clause
	}
	return ""
}

func TestCommitsReadsTheBranch(t *testing.T) {
	ctx := context.Background()
	r := testRepo(t, nil)
	if fs, err := Commits(ctx, r); err != nil || len(fs) != 0 {
		t.Fatalf("empty range = %v %v", fs, err)
	}
	commit(t, r, "no prefix old", nil)
	commit(t, r, "feat(sdlc): refuse a short subject", nil)
	commit(t, r, "no prefix new", nil)
	fs, err := Commits(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	// History order, oldest first, and the clean commit between is no offender.
	if len(fs) != 2 || !strings.Contains(fs[0].Message, "no prefix old") || !strings.Contains(fs[1].Message, "no prefix new") {
		t.Fatalf("findings = %v", fs)
	}
}

func TestCommitsSkipsAMergeAndNotTheOffenderBesideIt(t *testing.T) {
	ctx := context.Background()
	r := testRepo(t, nil)
	git(t, r.Root, "checkout", "-q", "-b", "side")
	commit(t, r, "feat: add a side file", map[string]string{"side": "x\n"})
	git(t, r.Root, "checkout", "-q", "main")
	commit(t, r, "bad subject with no prefix", map[string]string{"main": "x\n"})
	git(t, r.Root, "merge", "-q", "--no-ff", "-m", "Merge branch 'side' into main", "side")
	fs, err := Commits(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rules(fs), []string{"prefix"}) || !strings.Contains(fs[0].Message, "bad subject") {
		t.Fatalf("findings = %v", fs)
	}
}

func TestCommitsCannotRunWithoutTheBase(t *testing.T) {
	r := testRepo(t, nil)
	r.Base = "origin/nope"
	if _, err := Commits(context.Background(), r); err == nil {
		t.Fatal("an unresolvable base passed")
	}
}

func TestParseLog(t *testing.T) {
	rec := func(sha, parents, msg string) string { return sha + "\x1f" + parents + "\x1f" + msg + "\x1e" }
	got := parseLog(rec("aaa", "p1", "feat: one\n\nbody\n\n"+trailer+"\n") + "\n" + rec("bbb", "p1 p2", "Merge x") + "\n" + rec("ccc", "", "feat: root"))
	if len(got) != 3 || got[0].sha != "aaa" || !strings.Contains(got[0].message, trailer) {
		t.Fatalf("got %+v", got)
	}
	if !slices.Equal(got[1].parents, []string{"p1", "p2"}) || len(got[2].parents) != 0 {
		t.Fatalf("parents %+v", got)
	}
	if len(parseLog("")) != 0 || len(parseLog("\n")) != 0 {
		t.Fatal("an empty range parsed to commits")
	}
	if m := parseLog(rec("aaa", "p1", "feat: one\x1ftwo"))[0].message; m != "feat: one\x1ftwo" {
		t.Fatalf("message with the field byte = %q", m)
	}
}

func TestRemedy(t *testing.T) {
	if got := remedy([]string{"aaa"}, "aaa", "origin/main"); !strings.Contains(got, "git commit --amend -F") || strings.Contains(got, "rebase") {
		t.Fatalf("one offender at HEAD: %s", got)
	}
	if got := remedy([]string{"aaa"}, "zzz", "origin/main"); !strings.Contains(got, "git rebase -i origin/main") || strings.Contains(got, "--amend") {
		t.Fatalf("one offender below HEAD: %s", got)
	}
	if got := remedy([]string{"bbb", "aaa"}, "aaa", "origin/main"); !strings.Contains(got, "git rebase -i origin/main") {
		t.Fatalf("two offenders: %s", got)
	}
}

func TestCheckMessage(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		want          []string
	}{
		{"a real commit passes", lines("feat(sdlc): refuse a package import under scripts/", "", "One paragraph of body,", "wrapped.", "", trailer, ""), nil},
		{"the trailer is a footer", lines("fix(sdlc): refuse a short subject", "", "One paragraph.", "", trailer), nil},
		{"two paragraphs", lines("fix(sdlc): refuse a short subject", "", "First.", "", "Second.", "", trailer), []string{"paragraphs"}},
		{"several footer blocks come off the end", lines("feat(sdlc)!: refuse a short subject", "", "One.", "", "BREAKING CHANGE: the flag is gone", "", "Closes #12", "", trailer), nil},
		{"a token mid-body is a paragraph", lines("fix(sdlc): refuse a short subject", "", "Note: this is prose.", "", "A second paragraph.", "", trailer), []string{"paragraphs"}},
		{"a line of spaces ends a block", lines("fix(sdlc): refuse a short subject", "", "First.", "   ", "Second.", "", trailer), []string{"paragraphs"}},
		{"carriage returns make no paragraph", strings.Join([]string{"fix(sdlc): refuse a short subject", "", "One.", "", trailer}, "\r\n"), nil},
		{"the cap", subjectOfLength(subjectMax), nil},
		{"one past the cap", subjectOfLength(subjectMax + 1), []string{"subject_length"}},
		{"the longest subject on tickertown's main", "feat(sdlc): the sweeper runs every hour, and a finding it cannot act on still costs one run a day", []string{"subject_length", "mood"}},
		{"a type outside the list", "update: refuse a short subject", []string{"prefix"}},
		{"a capital type", "Fix: capitals are a different word", []string{"prefix"}},
		{"no prefix", "a subject with no prefix at all", []string{"prefix"}},
		{"an id before the prefix", "lp-a1b2 feat(sdlc): the prefix is not first", []string{"prefix"}},
		{"wip before the prefix", "wip feat: still not first", []string{"prefix"}},
		{"no scope", "feat: refuse a short subject", nil},
		{"breaking", "feat!: refuse a short subject", nil},
		{"breaking with a scope", "feat(sdlc)!: refuse a short subject", nil},
		{"no space after the colon", "feat(sdlc):no space", []string{"prefix"}},
		{"an empty description", "feat(sdlc): ", []string{"prefix"}},
		{"a blank description", "feat(sdlc):   ", []string{"prefix"}},
		{"no separator", "feat(sdlc):", []string{"prefix"}},
		{"a space before the scope", "feat (sdlc): a space before the scope", []string{"prefix"}},
		{"a capital scope", "feat(SDLC): capitals", []string{"prefix"}},
		{"an empty scope", "feat(): empty", []string{"prefix"}},
		{"a hyphenated scope", "feat(sdlc-loop): accept a hyphen in a scope", nil},
		{"a revert git wrote", `Revert "docs(spec): lp-a1b2 whole shares, with C2, C9, C18 and C19 recomputed"`, nil},
		{"the revert exemption is anchored", `feat(sdlc): Revert "x" and ` + strings.Repeat("y", 60), []string{"subject_length", "mood"}},
		{"a revert keeps the paragraph rule", lines(`Revert "docs(spec): whole shares"`, "", "This reverts commit abc1234.", "", "A second paragraph."), []string{"paragraphs"}},
		{"two rules at once", "a subject with no prefix that runs well past the cap " + strings.Repeat("x", 40), []string{"prefix", "subject_length"}},
		{"an empty message", "", []string{"prefix"}},
	} {
		if got := msgRules(tc.message); !slices.Equal(got, tc.want) {
			t.Errorf("%s: rules = %v, want %v", tc.name, got, tc.want)
		}
	}
	for _, typ := range commitTypes {
		if got := msgRules(typ + ": refuse a short subject"); got != nil {
			t.Errorf("%s: %v", typ, got)
		}
	}
}

func TestCheckMessageDetails(t *testing.T) {
	if fs := checkMessage(subjectOfLength(97)); fs[0].Message != "the subject is 97 characters, and the cap is 72" {
		t.Errorf("length = %q", fs[0].Message)
	}
	fs := checkMessage(lines("fix(sdlc): refuse a short subject", "", "a", "", "b", "", "c", "", trailer))
	if !strings.Contains(fs[0].Message, "3 paragraphs") {
		t.Errorf("paragraphs = %q", fs[0].Message)
	}
}

func TestMessageParts(t *testing.T) {
	if n := len(bodyBlocks(lines("fix(sdlc): refuse a short subject", "", trailer))); n != 0 {
		t.Errorf("a trailer alone left %d paragraphs", n)
	}
	folded := []string{"feat(sdlc): " + strings.Repeat("x", subjectMax-20), strings.Repeat("y", 7)}
	if subjectOf(strings.Join(folded, "\r\n")) != subjectOf(strings.Join(folded, "\n")) || msgRules(strings.Join(folded, "\r\n")) != nil {
		t.Error("a carriage return changed the subject")
	}
	msg := lines("feat(sdlc): refuse a short subject", "and a line git folds into it")
	if subjectOf(msg) != "feat(sdlc): refuse a short subject and a line git folds into it" || len(bodyBlocks(msg)) != 0 {
		t.Errorf("folded subject = %q", subjectOf(msg))
	}
	if len(messageBlocks("a\n\n\n\nb")) != 2 || len(bodyBlocks("feat: s\n\n\n\nonly one paragraph")) != 1 {
		t.Error("a run of blank lines ended more than one block")
	}
	if subjectOf("") != "" {
		t.Error("an empty message has a subject")
	}
	for _, line := range []string{"Co-Authored-By: someone", "Closes #12", "BREAKING CHANGE: the flag is gone", "Reviewed-by: someone"} {
		if !footerStart.MatchString(line) {
			t.Errorf("%q is a footer", line)
		}
	}
	for _, line := range []string{"ingest() refreshes the universe", "breaking change: lowercase is prose", "Co-Authored-By:no space after the colon"} {
		if footerStart.MatchString(line) {
			t.Errorf("%q is not a footer", line)
		}
	}
}

func TestMood(t *testing.T) {
	for _, tc := range []struct{ subject, clause string }{
		// The subject a person quoted when length proved the wrong axis.
		{"feat(sdlc): pnpm intake coverage names the sources no epic covers", "tool-name"},
		{"feat(sdlc): pnpm intake apply creates one marked epic, idempotently", "tool-name"},
		{"fix(sdlc): gh pr create targets the base, like the other six scripts", "tool-name"},
		{"feat(gates): loop gates reads the project config", "tool-name"},
		{"feat(sdlc): the merger merges its whole queue", "determiner"},
		{"feat(sdlc): a finished epic closes itself", "determiner"},
		{"docs(adr): 0004 records Neon's Postgres version, which is 18", "noun-phrase"},
		{"docs(adr): 0011's split-boundary validator could not fail", "noun-phrase"},
		{"docs(spec): lp-a1b2 answers the review", "third-person"},
		{"docs(sdlc): issue.ts records the one write path no test reaches", "identifier"},
		{"fix(sdlc): agentId is synchronous", "identifier"},
		{"fix(sdlc): passes the base branch through to premerge", "third-person"},
		{"feat(db): lp-a1b2 the five market-data tables and their repository", "determiner"},
		{"feat(sdlc): the sweeper runs hourly, reports daily", "determiner"},
		{"feat(batch): lp-7x2q.3 the ingest run: universe, coverage, paths", "determiner"},
		// A bare id names no change, so the identifier clause reads it.
		{"feat(sdlc): lp-a1b2", "identifier"},
	} {
		if got := moodClause(tc.subject); got != tc.clause {
			t.Errorf("%q: clause %q, want %q", tc.subject, got, tc.clause)
		}
	}
	for _, s := range []string{
		"docs(plans): record Ian's answers to the intake plan's questions",
		"feat(sdlc): refuse a package import under scripts/",
		"feat(sdlc): fast-forward the shared checkout, because merging is not deploying",
		"feat(sdlc): reclaim the worktrees dispatched runs leave behind",
		"chore(skills): track the Neon reference skills, and say what they are",
		"feat(skills): vendor pstack `reflect`, so a repeated correction starts its own mechanism",
		"docs(spec): lp-a1b2 bootstrap strategy engine",
		"refactor(ui): lp-k3m9.1 move cn() into the app and delete packages/ui",
		"feat(engine): lp-8 backtest a deployment over a window",
		"docs: assert a vendor's outcome, never a vendor's mechanism",
		"fix(deps): raise next to 16.3.6, so audit:deps clears a critical advisory",
		"feat(sdlc): rebase-merge, and keep main auditable through it",
		"feat(gates): make the gate fail fast",
		"fix(sdlc): pass the base branch through to premerge",
		"feat(sdlc): process each ticket in its own worktree",
		"fix(sdlc): address the review in one round",
		"feat(db): lp-a1b2 add the five market-data tables and their repository",
		// Declined: the prefix rule owns these, and git's revert takes the same path.
		"a subject with no prefix at all",
		"update: the colon is not a type",
		`Revert "docs(spec): lp-a1b2 whole shares"`,
		"feat(sdlc): ",
		"feat(sdlc): ,",
	} {
		if m := moodFinding(s); m != nil {
			t.Errorf("%q refused by %s", s, m.clause)
		}
	}
}

func TestDescriptionOf(t *testing.T) {
	for in, want := range map[string]string{
		"docs(spec): lp-a1b2 adds a schema workflow":   "adds a schema workflow",
		"docs(spec): loop-7.2 adds a schema workflow":  "adds a schema workflow",
		"feat(sdlc): refuse a package import":          "refuse a package import",
		"feat(sdlc): fast-forward the shared checkout": "fast-forward the shared checkout",
		`Revert "docs(spec): lp-a1b2 whole shares"`:    "",
	} {
		if got := descriptionOf(in); got != want {
			t.Errorf("descriptionOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMoodDetail(t *testing.T) {
	d := moodFinding("feat(sdlc): the merger merges its whole queue").detail
	for _, want := range []string{`"the"`, "imperative", "run the sweeper hourly"} {
		if !strings.Contains(d, want) {
			t.Errorf("detail %q lacks %q", d, want)
		}
	}
	if d := moodFinding("docs(spec): lp-a1b2 answers the review").detail; !strings.Contains(d, "versEndingInS") || !strings.Contains(d, "commits.go") {
		t.Errorf("third-person detail %q names no escape", d)
	}
}
