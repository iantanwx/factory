package cli_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/inbox"
)

// helpTexts lists each help text of the surface with where it sits: each
// verb's summary and callers, and each description of an argument or a
// flag.
func helpTexts(r *cli.Registry) map[string]string {
	out := map[string]string{}
	for _, v := range r.Verbs() {
		out[v.Path+" summary"] = v.Summary
		out[v.Path+" callers"] = strings.Join(v.Callers, ", ")
		for _, a := range v.Args {
			out[v.Path+" <"+a.Name+">"] = a.Description
		}
		for _, f := range v.Flags {
			out[v.Path+" --"+f.Name] = f.Description
		}
	}
	return out
}

// inboxKinds lists each kind of an inbox entry.
var inboxKinds = []string{
	inbox.KindEscalation, inbox.KindPanel, inbox.KindDecision, inbox.KindSignoff, inbox.KindQuestion,
	inbox.KindAsk, inbox.KindArchitecture, inbox.KindRoom, inbox.KindAlert, inbox.KindFault,
	inbox.KindPause, inbox.KindNotice, inbox.KindLearning,
}

// retiredIn names each text of texts that holds a retired word, matched
// whole in any case.
func retiredIn(texts map[string]string, words []string) []string {
	var hits []string
	for where, text := range texts {
		for _, w := range words {
			if regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(w) + `\b`).MatchString(text) {
				hits = append(hits, fmt.Sprintf("%s: %s", where, w))
			}
		}
	}
	return hits
}

// C9: no help text of the CLI surface and no inbox kind holds a retired
// word.
func TestC9HelpAndInboxKindsHoldNoRetiredWord(t *testing.T) {
	texts := helpTexts(cli.Surface())
	for _, k := range inboxKinds {
		texts["inbox kind "+k] = k
	}
	if hits := retiredIn(texts, config.RetiredWords()); len(hits) > 0 {
		t.Fatalf("a help text or an inbox kind holds a retired word:\n%s", strings.Join(hits, "\n"))
	}
}

// The check reads each flag description, and names the text that holds a
// retired word.
func TestRetiredInNamesTheHelpText(t *testing.T) {
	if d := helpTexts(cli.Surface())["work peek --stage"]; d != "the stage whose work query to run" {
		t.Fatalf("work peek --stage = %q, want its flag description", d)
	}
	texts := map[string]string{"x summary": "Ask the Old-Stage.", "y summary": "The old-stager is fine."}
	if got := retiredIn(texts, []string{"old-stage"}); len(got) != 1 || got[0] != "x summary: old-stage" {
		t.Fatalf("hits = %q, want x summary only", got)
	}
}
