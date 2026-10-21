package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// globalFlags apply to every verb. Each one is a bool, so a global flag
// never takes the next word as its value and may come before the verb.
var globalFlags = []Flag{
	{Name: "help", Type: FlagBool, Enum: []string{}, Description: "print this verb's slice of the schema instead of running it"},
	{Name: "json", Type: FlagBool, Enum: []string{}, Description: "print one JSON envelope; this is the default"},
	{Name: "text", Type: FlagBool, Enum: []string{}, Description: "print a human-readable form instead of JSON"},
}

// call is one parsed command line.
type call struct {
	verb  *Verb
	path  string // the name the envelope prints
	text  bool
	args  []string
	flags map[string]string
}

func usage(rule, hint string, next ...string) *Result {
	res := Refuse(Refusal{Code: CodeUsage, Rule: rule, Hint: hint, AllowedNext: next})
	return &res
}

func isFlagToken(t string) bool { return len(t) > 1 && t[0] == '-' }

func flagName(t string) string {
	if t == "-h" {
		return "help"
	}
	if !strings.HasPrefix(t, "--") {
		return "" // a single-dash flag other than -h is never valid
	}
	name, _, _ := strings.Cut(t[2:], "=")
	return name
}

func findFlag(flags []Flag, name string) (Flag, bool) {
	i := slices.IndexFunc(flags, func(f Flag) bool { return f.Name == name })
	if i < 0 {
		return Flag{}, false
	}
	return flags[i], true
}

// wantsText lets a usage fault honour --text before the line fully parses.
func wantsText(tokens []string) bool {
	for _, t := range tokens {
		if t == "--" {
			return false
		}
		if t == "--text" || t == "--text=true" {
			return true
		}
	}
	return false
}

// parse resolves the verb and its flags. A non-nil result is the answer:
// a usage fault or a --help slice. Unknown flags are refused before
// anything else is judged, so a mistyped flag never runs a verb.
func (r *Registry) parse(tokens []string) (call, *Result) {
	c := call{text: wantsText(tokens)}

	var words, rest []string
	for i, t := range tokens {
		if t == "--" || len(words) == 2 || len(words) == 1 && r.topLevel(words[0]) {
			rest = append(rest, tokens[i:]...)
			break
		}
		if isFlagToken(t) {
			if f, ok := findFlag(globalFlags, flagName(t)); !ok || f.Type != FlagBool {
				return c, usage("unknown_flag", fmt.Sprintf("unknown flag %s before the verb; name the verb first", t), "schema")
			}
			rest = append(rest, t)
			continue
		}
		words = append(words, t)
	}

	help := slices.ContainsFunc(rest, func(t string) bool { return t == "--help" || t == "-h" || t == "--help=true" })
	switch {
	case len(words) == 0:
		if help {
			s, err := r.Schema()
			if err != nil {
				return c, internal(err)
			}
			res := Found(s)
			return c, &res
		}
		return c, usage("missing_noun", "name a noun or a top-level verb; loop schema lists them", "schema")
	case r.topLevel(words[0]):
		c.verb = r.byPath[words[0]]
	case !slices.Contains(r.nouns, words[0]):
		return c, usage("unknown_noun", fmt.Sprintf("unknown noun %q; loop schema lists the nouns", words[0]), "schema")
	case len(words) == 1:
		c.path = words[0]
		verbs := r.nounVerbs(words[0])
		if help {
			res := Found(verbs)
			return c, &res
		}
		return c, usage("missing_verb", fmt.Sprintf("noun %q needs a verb", words[0]), verbPaths(verbs)...)
	default:
		v, ok := r.byPath[words[0]+" "+words[1]]
		if !ok {
			c.path = words[0]
			return c, usage("unknown_verb", fmt.Sprintf("noun %q has no verb %q", words[0], words[1]), verbPaths(r.nounVerbs(words[0]))...)
		}
		c.verb = v
	}
	c.path = c.verb.Path

	args, flags, fault := parseFlags(c.verb, rest)
	if fault != nil {
		return c, fault
	}
	if flags["json"] == "true" && flags["text"] == "true" {
		return c, usage("flag_conflict", "--json and --text exclude each other", c.path)
	}
	c.text = flags["text"] == "true"
	if flags["help"] == "true" {
		res := Found(c.verb)
		return c, &res
	}
	for _, g := range globalFlags {
		delete(flags, g.Name)
	}

	for _, f := range c.verb.Flags {
		if _, ok := flags[f.Name]; f.Required && !ok {
			return c, usage("missing_flag", fmt.Sprintf("%s needs --%s", c.path, f.Name), c.path)
		}
	}
	if len(args) > len(c.verb.Args) {
		return c, usage("extra_argument", fmt.Sprintf("%s takes at most %d arguments; got %q", c.path, len(c.verb.Args), args), c.path)
	}
	for i, a := range c.verb.Args {
		if a.Required && i >= len(args) {
			return c, usage("missing_argument", fmt.Sprintf("%s needs <%s>", c.path, a.Name), c.path)
		}
	}
	c.args, c.flags = args, flags
	return c, nil
}

// parseFlags splits rest into positional arguments and canonical flag
// values. An unknown flag wins over a bad value anywhere on the line.
func parseFlags(v *Verb, rest []string) ([]string, map[string]string, *Result) {
	specs := append(slices.Clone(globalFlags), v.Flags...)
	args := []string{}
	flags := map[string]string{}
	var valueFault *Result
	for i := 0; i < len(rest); i++ {
		t := rest[i]
		if t == "--" {
			args = append(args, rest[i+1:]...)
			break
		}
		if !isFlagToken(t) {
			args = append(args, t)
			continue
		}
		f, ok := findFlag(specs, flagName(t))
		if !ok {
			return nil, nil, usage("unknown_flag", fmt.Sprintf("%s has no flag %s", v.Path, t), v.Path)
		}
		_, value, hasValue := strings.Cut(t, "=")
		if !hasValue && f.Type != FlagBool {
			if i+1 == len(rest) || strings.HasPrefix(rest[i+1], "--") {
				if valueFault == nil {
					valueFault = usage("missing_value", fmt.Sprintf("--%s needs a value", f.Name), v.Path)
				}
				continue
			}
			i++
			value, hasValue = rest[i], true
		}
		if !hasValue {
			value = "true"
		}
		if _, dup := flags[f.Name]; dup && valueFault == nil {
			valueFault = usage("repeated_flag", fmt.Sprintf("--%s is given twice", f.Name), v.Path)
		}
		canon, err := canonical(f, value)
		if err != nil && valueFault == nil {
			valueFault = usage("bad_flag_value", fmt.Sprintf("--%s: %v", f.Name, err), v.Path)
		}
		flags[f.Name] = canon
	}
	if valueFault != nil {
		return nil, nil, valueFault
	}
	return args, flags, nil
}

// canonical checks a value against its flag type and returns the one
// spelling the server sees, so equal calls carry equal flags.
func canonical(f Flag, value string) (string, error) {
	switch f.Type {
	case FlagBool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return "", fmt.Errorf("want true or false, got %q", value)
		}
		return strconv.FormatBool(b), nil
	case FlagInt:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return "", fmt.Errorf("want an integer, got %q", value)
		}
		return strconv.FormatInt(n, 10), nil
	case FlagDuration:
		d, err := time.ParseDuration(value)
		if err != nil {
			return "", fmt.Errorf("want a duration such as 90s, got %q", value)
		}
		return d.String(), nil
	case FlagTime:
		ts, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return "", fmt.Errorf("want an RFC 3339 time, got %q", value)
		}
		return ts.UTC().Format(time.RFC3339), nil
	}
	if value == "" {
		return "", fmt.Errorf("want a value, got an empty string")
	}
	if len(f.Enum) > 0 && !slices.Contains(f.Enum, value) {
		return "", fmt.Errorf("want one of %s, got %q", strings.Join(f.Enum, ", "), value)
	}
	return value, nil
}

// ReservedPrefix starts each transport field that a VerbRequest carries in
// its flags map beside the verb's flags. No flag name can start with it, so
// no command line can set one.
const ReservedPrefix = "@"

// Reserved transport fields.
const (
	// ExtraStdin is the base64 of the bytes a verb read from stdin, such as
	// the value of secret set. The server never logs or echoes it.
	ExtraStdin = ReservedPrefix + "stdin"
	// ExtraContentHash is the hash of the content a write carries (KTD13).
	ExtraContentHash = ReservedPrefix + "content_hash"
	// ExtraExpectedStatus is the item status the CLI read for the write's
	// key. The server answers STATUS_CHANGED when the item moved since
	// (AE4), and never applies the write over another actor's move.
	ExtraExpectedStatus = ReservedPrefix + "expected_status"
)

// Validate checks a call that arrives over the wire against the verb's
// schema, the way parse checks a command line. Each flag value must already
// be canonical, so the server sees exactly what an honest CLI sends.
// Reserved fields move to Invocation.Extra.
func (r *Registry) Validate(path string, args []string, flags map[string]string, requestID string) (Invocation, *Result) {
	v, ok := r.byPath[path]
	if !ok {
		return Invocation{}, usage("unknown_verb", fmt.Sprintf("this server has no verb %q", path), "schema")
	}
	inv := Invocation{Verb: v, Args: slices.Clone(args), Flags: map[string]string{}, Extra: map[string]string{}, RequestID: requestID}
	if inv.Args == nil {
		inv.Args = []string{}
	}
	names := make([]string, 0, len(flags))
	for name := range flags {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		value := flags[name]
		if strings.HasPrefix(name, ReservedPrefix) {
			inv.Extra[name] = value
			continue
		}
		f, ok := findFlag(v.Flags, name)
		if !ok {
			return inv, usage("unknown_flag", fmt.Sprintf("%s has no flag --%s", path, name), path)
		}
		canon, err := canonical(f, value)
		if err != nil || canon != value {
			return inv, usage("bad_flag_value", fmt.Sprintf("--%s: the value is not canonical", name), path)
		}
		inv.Flags[name] = value
	}
	for _, f := range v.Flags {
		if _, ok := inv.Flags[f.Name]; f.Required && !ok {
			return inv, usage("missing_flag", fmt.Sprintf("%s needs --%s", path, f.Name), path)
		}
	}
	if len(inv.Args) > len(v.Args) {
		return inv, usage("extra_argument", fmt.Sprintf("%s takes at most %d arguments", path, len(v.Args)), path)
	}
	for i, a := range v.Args {
		if a.Required && i >= len(inv.Args) {
			return inv, usage("missing_argument", fmt.Sprintf("%s needs <%s>", path, a.Name), path)
		}
	}
	return inv, nil
}
