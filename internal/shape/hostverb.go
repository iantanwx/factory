package shape

import (
	"slices"
	"strings"
)

// HostVerbCall is one command in a ticket body that calls a host verb.
type HostVerbCall struct {
	Verb    string // the verb's path, such as "doctor" or "secret set"
	Section string // the heading the command is under
	Line    int    // the command's line in the body
}

// commandSections are the ticket sections that tell a run what to do. A host
// verb in any other section is what the ticket changes or quotes, since a
// ticket may change a host verb.
var commandSections = []string{"Done means", ticketContracts.Name}

// HostVerbCalls returns each command under Done means or Contracts to prove
// that calls one of verbs as `loop <verb>`, in a code span or a fenced block.
func HostVerbCalls(body string, verbs []string) []HostVerbCall {
	var out []HostVerbCall
	for _, sec := range sections(body) {
		if !slices.ContainsFunc(commandSections, func(n string) bool { return strings.EqualFold(sec.Heading, n) }) {
			continue
		}
		for _, l := range sec.Lines {
			code := []string{l.Text}
			if !l.Fenced {
				code = codeSpan.FindAllString(l.Text, -1)
			}
			for _, c := range code {
				for _, v := range called(c, verbs) {
					out = append(out, HostVerbCall{Verb: v, Section: sec.Heading, Line: l.N})
				}
			}
		}
	}
	return out
}

// called returns each of verbs whose words follow the word loop in code.
func called(code string, verbs []string) []string {
	words := strings.Fields(strings.Trim(code, "`"))
	var out []string
	for i, w := range words {
		if w != "loop" {
			continue
		}
		for _, v := range verbs {
			path := strings.Fields(v)
			if len(words) > i+len(path) && slices.Equal(words[i+1:i+1+len(path)], path) {
				out = append(out, v)
			}
		}
	}
	return out
}
