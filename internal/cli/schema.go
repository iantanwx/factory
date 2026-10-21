package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/iantanwx/loop/gen"
)

// Version is the binary's version. The release build sets it with
// -ldflags "-X github.com/iantanwx/loop/internal/cli.Version=<v>".
var Version = "dev"

// protoPackage is the proto package whose services carry the verbs.
const protoPackage = "loop.v1"

// Side says where a verb runs. A server verb has exactly one RPC.
type Side string

const (
	SideServer Side = "server"
	SideClient Side = "client"
)

// FlagType names how a flag value parses.
type FlagType string

const (
	FlagString   FlagType = "string"
	FlagInt      FlagType = "int"
	FlagBool     FlagType = "bool"
	FlagDuration FlagType = "duration" // time.ParseDuration
	FlagTime     FlagType = "time"     // RFC 3339
)

// Flag describes one --flag of a verb.
type Flag struct {
	Name        string   `json:"name"`
	Type        FlagType `json:"type"`
	Required    bool     `json:"required"`
	Enum        []string `json:"enum"` // [] when any value is allowed
	Description string   `json:"description"`
}

// Arg describes one positional argument of a verb, in order.
type Arg struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// Verb is one entry of the CLI surface, and the slice of the schema that
// `loop <noun> <verb> --help` prints.
type Verb struct {
	Path     string   `json:"path"` // "work peek", or "schema" for a top-level verb
	Noun     string   `json:"noun"` // "host" or "meta" for a top-level verb
	Name     string   `json:"verb"`
	TopLevel bool     `json:"top_level"`
	Summary  string   `json:"summary"`
	Callers  []string `json:"callers"`
	Side     Side     `json:"side"`
	RPC      string   `json:"rpc"` // full gRPC method name; "" for a client verb
	Args     []Arg    `json:"args"`
	Flags    []Flag   `json:"flags"`
	// Write marks a verb that changes state. A write from a run, a room or
	// the coordinator carries an idempotency key (KTD13).
	Write bool `json:"write"`

	handler Handler
}

// Invocation is one parsed call. Args and Flags hold canonical strings, the
// same shape the VerbRequest carries to the server.
type Invocation struct {
	Verb      *Verb
	Args      []string
	Flags     map[string]string
	RequestID string
	// Extra holds the reserved transport fields of a VerbRequest, keyed with
	// ReservedPrefix: a value read from stdin and a content hash. The CLI
	// parser never fills it, so no command-line flag can set one.
	Extra map[string]string
}

// Arg returns the positional argument with the given schema name, or "".
func (inv Invocation) Arg(name string) string {
	for i, a := range inv.Verb.Args {
		if a.Name == name && i < len(inv.Args) {
			return inv.Args[i]
		}
	}
	return ""
}

// Handler answers one verb.
type Handler func(ctx context.Context, inv Invocation) Result

// Registry holds the CLI surface and every table the schema prints. Later
// units fill it: verbs and handlers, refusal codes, transition tables and
// the capability matrix.
type Registry struct {
	verbs        []*Verb
	byPath       map[string]*Verb
	nouns        []string
	refusals     map[string]RefusalCode
	refusalOrder []string
	transitions  map[string]json.RawMessage
	capabilities json.RawMessage
	matrix       *CapabilityMatrix
	remote       Handler
}

// NewRegistry returns a registry that holds the seeded refusal codes and no
// verbs. New adds the whole surface.
func NewRegistry() *Registry {
	r := &Registry{
		byPath:      map[string]*Verb{},
		refusals:    map[string]RefusalCode{},
		transitions: map[string]json.RawMessage{},
	}
	for _, c := range baseRefusals {
		if err := r.RegisterRefusal(c); err != nil {
			panic(err)
		}
	}
	return r
}

// RegisterRefusal adds a code to the closed set. It refuses a duplicate and
// a code without a full mapping.
func (r *Registry) RegisterRefusal(c RefusalCode) error {
	if err := c.validate(); err != nil {
		return err
	}
	if _, dup := r.refusals[c.Code]; dup {
		return fmt.Errorf("refusal code %s is already registered", c.Code)
	}
	r.refusals[c.Code] = c
	r.refusalOrder = append(r.refusalOrder, c.Code)
	return nil
}

// Refusal returns the mapping of one code.
func (r *Registry) Refusal(code string) (RefusalCode, bool) {
	c, ok := r.refusals[code]
	return c, ok
}

// RefusalCodes returns every registered code in registration order.
func (r *Registry) RefusalCodes() []RefusalCode {
	out := make([]RefusalCode, 0, len(r.refusalOrder))
	for _, code := range r.refusalOrder {
		out = append(out, r.refusals[code])
	}
	return out
}

// GRPCStatus is the one gRPC status of a code. An unregistered code is
// Internal, the same as seal treats it.
func (r *Registry) GRPCStatus(code string) codes.Code {
	if c, ok := r.refusals[code]; ok {
		return c.GRPC
	}
	return codes.Internal
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// RegisterVerb adds one verb. Noun, Name and TopLevel identify it. Path and,
// for a server verb, RPC are derived: noun "work" verb "peek" is
// "/loop.v1.WorkService/Peek".
func (r *Registry) RegisterVerb(v Verb) error {
	if !namePattern.MatchString(v.Noun) || !namePattern.MatchString(v.Name) {
		return fmt.Errorf("verb %q %q: noun and verb must be lower-case words", v.Noun, v.Name)
	}
	v.Path = v.Noun + " " + v.Name
	if v.TopLevel {
		v.Path = v.Name
	}
	if _, dup := r.byPath[v.Path]; dup {
		return fmt.Errorf("verb %q is already registered", v.Path)
	}
	if v.TopLevel && slices.Contains(r.nouns, v.Name) || !v.TopLevel && r.topLevel(v.Noun) {
		return fmt.Errorf("verb %q: a top-level verb and a noun share a name", v.Path)
	}
	switch v.Side {
	case SideServer:
		v.RPC = fmt.Sprintf("/%s.%sService/%s", protoPackage, pascal(v.Noun), pascal(v.Name))
	case SideClient:
		v.RPC = ""
	default:
		return fmt.Errorf("verb %q: side must be server or client", v.Path)
	}
	if v.Summary == "" {
		return fmt.Errorf("verb %q has no summary", v.Path)
	}
	seen := map[string]bool{}
	for _, f := range globalFlags {
		seen[f.Name] = true
	}
	v.Flags = slices.Clone(v.Flags)
	for i, f := range v.Flags {
		if f.Enum == nil {
			v.Flags[i].Enum = []string{}
		}
		if !namePattern.MatchString(f.Name) || seen[f.Name] {
			return fmt.Errorf("verb %q: flag %q is malformed or repeated", v.Path, f.Name)
		}
		seen[f.Name] = true
		switch f.Type {
		case FlagString, FlagInt, FlagBool, FlagDuration, FlagTime:
		default:
			return fmt.Errorf("verb %q: flag %q has unknown type %q", v.Path, f.Name, f.Type)
		}
	}
	for i, a := range v.Args {
		if a.Required && i > 0 && !v.Args[i-1].Required {
			return fmt.Errorf("verb %q: required argument %q follows an optional one", v.Path, a.Name)
		}
	}
	if v.Callers == nil {
		v.Callers = []string{}
	}
	if v.Args == nil {
		v.Args = []Arg{}
	}
	if v.Flags == nil {
		v.Flags = []Flag{}
	}
	r.verbs = append(r.verbs, &v)
	r.byPath[v.Path] = &v
	if !v.TopLevel && !slices.Contains(r.nouns, v.Noun) {
		r.nouns = append(r.nouns, v.Noun)
	}
	return nil
}

// SetHandler plugs the implementation of one verb in by its path.
func (r *Registry) SetHandler(path string, h Handler) error {
	v, ok := r.byPath[path]
	if !ok {
		return fmt.Errorf("no verb %q", path)
	}
	v.handler = h
	return nil
}

// SetRemote sets the handler of every server verb that has no handler of
// its own. The client sets it to the call that sends a VerbRequest to the
// server.
func (r *Registry) SetRemote(h Handler) { r.remote = h }

// RegisterTransitionTable adds the transition table of one item kind. The
// schema prints it as given, so it must marshal to JSON.
func (r *Registry) RegisterTransitionTable(kind string, table any) error {
	if _, dup := r.transitions[kind]; dup {
		return fmt.Errorf("transition table %q is already registered", kind)
	}
	raw, err := json.Marshal(table)
	if err != nil {
		return fmt.Errorf("transition table %q: %w", kind, err)
	}
	r.transitions[kind] = raw
	return nil
}

// SetCapabilityMatrix sets the capability matrix once. The schema prints it
// as given, so it must marshal to JSON.
func (r *Registry) SetCapabilityMatrix(matrix any) error {
	if r.capabilities != nil {
		return fmt.Errorf("the capability matrix is already set")
	}
	raw, err := json.Marshal(matrix)
	if err != nil {
		return fmt.Errorf("capability matrix: %w", err)
	}
	r.capabilities = raw
	switch m := matrix.(type) {
	case CapabilityMatrix:
		r.matrix = &m
	case *CapabilityMatrix:
		r.matrix = m
	}
	return nil
}

// CapabilityMatrix returns the typed matrix, when SetCapabilityMatrix was
// given a CapabilityMatrix.
func (r *Registry) CapabilityMatrix() (*CapabilityMatrix, bool) {
	return r.matrix, r.matrix != nil
}

// Verbs returns every registered verb in registration order.
func (r *Registry) Verbs() []*Verb { return slices.Clone(r.verbs) }

// VerbByRPC returns the server verb whose RPC is the full method name.
func (r *Registry) VerbByRPC(method string) (*Verb, bool) {
	for _, v := range r.verbs {
		if v.Side == SideServer && v.RPC == method {
			return v, true
		}
	}
	return nil, false
}

// Verb returns one verb by path.
func (r *Registry) Verb(path string) (*Verb, bool) {
	v, ok := r.byPath[path]
	return v, ok
}

func (r *Registry) topLevel(name string) bool {
	v, ok := r.byPath[name]
	return ok && v.TopLevel
}

func (r *Registry) nounVerbs(noun string) []*Verb {
	var out []*Verb
	for _, v := range r.verbs {
		if !v.TopLevel && v.Noun == noun {
			out = append(out, v)
		}
	}
	return out
}

func verbPaths(vs []*Verb) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Path)
	}
	return out
}

// Schema is what `loop schema` prints.
type Schema struct {
	Name             string                     `json:"name"`
	Version          string                     `json:"version"`
	Envelope         EnvelopeSchema             `json:"envelope"`
	GlobalFlags      []Flag                     `json:"global_flags"`
	ExitCodes        []ExitInfo                 `json:"exit_codes"`
	RefusalCodes     []RefusalView              `json:"refusal_codes"`
	Verbs            []*Verb                    `json:"verbs"`
	Services         []Service                  `json:"services"`
	TransitionTables map[string]json.RawMessage `json:"transition_tables"`
	Capabilities     json.RawMessage            `json:"capabilities"`
}

// EnvelopeSchema names the keys of every envelope and of a refusal.
type EnvelopeSchema struct {
	Fields        []string `json:"fields"`
	RefusalFields []string `json:"refusal_fields"`
}

// RefusalView is one refusal code as the schema prints it.
type RefusalView struct {
	Code      string   `json:"code"`
	Exit      ExitCode `json:"exit"`
	GRPC      string   `json:"grpc"`
	Retryable bool     `json:"retryable"`
	Meaning   string   `json:"meaning"`
}

// Service is one gRPC service read from the embedded descriptor set.
type Service struct {
	Name    string   `json:"name"`
	Methods []string `json:"methods"` // full method names
}

// Schema builds the whole schema from the registry and the descriptor set.
func (r *Registry) Schema() (Schema, error) {
	services, err := Services()
	if err != nil {
		return Schema{}, err
	}
	s := Schema{
		Name:    "loop",
		Version: Version,
		Envelope: EnvelopeSchema{
			Fields:        jsonKeys(reflect.TypeFor[Envelope]()),
			RefusalFields: jsonKeys(reflect.TypeFor[Refusal]()),
		},
		GlobalFlags:      globalFlags,
		ExitCodes:        ExitCodes(),
		Verbs:            r.verbs,
		Services:         services,
		TransitionTables: r.transitions,
		Capabilities:     r.capabilities,
	}
	if s.Capabilities == nil {
		s.Capabilities = json.RawMessage("null")
	}
	for _, c := range r.RefusalCodes() {
		s.RefusalCodes = append(s.RefusalCodes, RefusalView{
			Code: c.Code, Exit: c.Exit, GRPC: c.GRPC.String(), Retryable: c.Retryable, Meaning: c.Meaning,
		})
	}
	return s, nil
}

// Services lists the services of package loop.v1 in the embedded
// descriptor set.
func Services() ([]Service, error) {
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(gen.DescriptorSet, &set); err != nil {
		return nil, fmt.Errorf("read embedded descriptor set: %w", err)
	}
	var out []Service
	for _, f := range set.GetFile() {
		if f.GetPackage() != protoPackage {
			continue
		}
		for _, s := range f.GetService() {
			name := protoPackage + "." + s.GetName()
			svc := Service{Name: name, Methods: []string{}}
			for _, m := range s.GetMethod() {
				svc.Methods = append(svc.Methods, "/"+name+"/"+m.GetName())
			}
			out = append(out, svc)
		}
	}
	return out, nil
}

// CheckRPCs reports every server verb without its RPC in the descriptor
// set, and every RPC that no server verb claims.
func (r *Registry) CheckRPCs() error {
	services, err := Services()
	if err != nil {
		return err
	}
	rpcs := map[string]int{}
	for _, s := range services {
		for _, m := range s.Methods {
			rpcs[m] = 0
		}
	}
	var faults []string
	for _, v := range r.verbs {
		if v.Side != SideServer {
			continue
		}
		if _, ok := rpcs[v.RPC]; !ok {
			faults = append(faults, fmt.Sprintf("verb %q names %s, which the descriptor set lacks", v.Path, v.RPC))
			continue
		}
		rpcs[v.RPC]++
	}
	for m, n := range rpcs {
		if n != 1 {
			faults = append(faults, fmt.Sprintf("RPC %s is claimed by %d server verbs, want 1", m, n))
		}
	}
	if len(faults) > 0 {
		slices.Sort(faults)
		return fmt.Errorf("verbs and RPCs disagree:\n%s", strings.Join(faults, "\n"))
	}
	return nil
}

func pascal(word string) string {
	var b strings.Builder
	for _, part := range strings.Split(word, "-") {
		if part != "" {
			b.WriteString(strings.ToUpper(part[:1]) + part[1:])
		}
	}
	return b.String()
}

// jsonKeys lists the JSON keys of a struct's own tagged fields. Envelope's
// embedded Refusal has no tag, so its keys stay in the refusal list.
func jsonKeys(t reflect.Type) []string {
	var keys []string
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" {
			keys = append(keys, name)
		}
	}
	return keys
}
