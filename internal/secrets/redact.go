package secrets

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// MinRedactLen is the shortest value the redactor matches. A shorter value,
// such as a port, a flag or a short PIN, appears by chance in ordinary text,
// so matching it would mangle logs and teach readers to ignore redactions.
//
// Residual risk (KTD27): a stored value shorter than MinRedactLen is never
// redacted. A value an agent splits, reverses, re-encodes in another way
// (for example mixed-case hex, base32 or a partial URL escape) or encrypts
// is not detected either. A base64 match removes the characters that encode
// the value alone, so up to four bits of the value at each end can remain in
// the neighbouring characters.
const MinRedactLen = 8

// ValueSource supplies the stored values to match. *Store is one.
type ValueSource interface {
	RedactionValues(ctx context.Context) ([]NamedValue, error)
}

// Redactor finds and replaces live secret values in outbound text (KTD27).
// It matches each value in plain, base64 (standard and URL alphabets, padded
// or not, at any byte offset), hex (lower and upper case), URL-escaped (query
// and path escaping, either hex case) and JSON-escaped forms. It precomputes
// every form and searches all of them in one pass. It is safe for concurrent
// use, and a search never waits for a rebuild.
type Redactor struct {
	mu     sync.Mutex // serialises rebuilds
	stored []NamedValue
	live   map[string]string
	m      atomic.Pointer[matcher]
}

// NewRedactor returns a redactor that matches nothing yet.
func NewRedactor() *Redactor {
	r := &Redactor{live: map[string]string{}}
	r.m.Store(buildMatcher(nil))
	return r
}

// Refresh replaces the stored values with the values every source supplies
// now. The server calls it after a secret set, a secret import and a start.
func (r *Redactor) Refresh(ctx context.Context, sources ...ValueSource) error {
	var all []NamedValue
	for _, s := range sources {
		vals, err := s.RedactionValues(ctx)
		if err != nil {
			return err
		}
		all = append(all, vals...)
	}
	r.SetStored(all)
	return nil
}

// SetStored replaces the stored values.
func (r *Redactor) SetStored(vals []NamedValue) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stored = append([]NamedValue(nil), vals...)
	r.rebuild()
}

// SetLive adds or replaces one short-lived value, such as the live App
// installation token or a live run token.
func (r *Redactor) SetLive(name, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.live[name] = value
	r.rebuild()
}

// DropLive removes a short-lived value, for example when a run settles.
func (r *Redactor) DropLive(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.live, name)
	r.rebuild()
}

func (r *Redactor) rebuild() {
	vals := append([]NamedValue(nil), r.stored...)
	for name, v := range r.live {
		vals = append(vals, NamedValue{Name: name, Value: v})
	}
	r.m.Store(buildMatcher(vals))
}

// Find reports whether text holds any live value, and the sorted names of
// the values it holds.
func (r *Redactor) Find(text string) (bool, []string) {
	m := r.m.Load()
	seen := map[string]bool{}
	m.scan(text, func(_, _ int, name string) { seen[name] = true })
	if len(seen) == 0 {
		return false, nil
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return true, names
}

// Redact replaces each match with [REDACTED:<name>]. Overlapping matches
// merge into one replacement that carries the first match's name.
func (r *Redactor) Redact(text string) string {
	type span struct {
		start, end int
		name       string
	}
	var spans []span
	r.m.Load().scan(text, func(start, end int, name string) {
		spans = append(spans, span{start, end, name})
	})
	if len(spans) == 0 {
		return text
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})
	var b strings.Builder
	pos := 0
	for i := 0; i < len(spans); {
		cur := spans[i]
		i++
		for i < len(spans) && spans[i].start < cur.end {
			cur.end = max(cur.end, spans[i].end)
			i++
		}
		b.WriteString(text[pos:cur.start])
		b.WriteString("[REDACTED:" + cur.name + "]")
		pos = cur.end
	}
	b.WriteString(text[pos:])
	return b.String()
}

// encodedForms returns every form of v the redactor matches, without
// duplicates and without a form shorter than MinRedactLen.
func encodedForms(v string) []string {
	if len(v) < MinRedactLen {
		return nil
	}
	forms := []string{v, hex.EncodeToString([]byte(v)), strings.ToUpper(hex.EncodeToString([]byte(v)))}
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		forms = append(forms, base64Cores(enc, []byte(v))...)
	}
	for _, e := range []string{url.QueryEscape(v), url.PathEscape(v)} {
		forms = append(forms, e, lowerPercent(e))
	}
	for _, html := range []bool{true, false} {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(html)
		if enc.Encode(v) == nil {
			j := strings.TrimSuffix(buf.String(), "\n")
			forms = append(forms, j[1:len(j)-1])
		}
	}
	seen := map[string]bool{}
	out := forms[:0]
	for _, f := range forms {
		if len(f) >= MinRedactLen && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// base64Cores returns, for each of the three byte offsets at which v can sit
// inside a longer base64 stream, the characters that encode v's bits alone.
// Character i encodes bits [6i, 6i+6). With k bytes before v, v holds bits
// [8k, 8k+8n), so the characters from ceil(8k/6) up to floor((8k+8n)/6)
// depend on v only. Each core is a substring of every padded or unpadded
// encoding of v at that offset.
func base64Cores(enc *base64.Encoding, v []byte) []string {
	var cores []string
	for k := 0; k < 3; k++ {
		buf := make([]byte, k+len(v))
		copy(buf[k:], v)
		s := enc.EncodeToString(buf)
		start := (8*k + 5) / 6
		end := min(8*(k+len(v))/6, len(s))
		if end > start {
			cores = append(cores, s[start:end])
		}
	}
	return cores
}

// lowerPercent lowers the hex digits of each %XX escape. Go writes upper
// case, and other encoders write lower case.
func lowerPercent(s string) string {
	b := []byte(s)
	for i := 0; i+2 < len(b); i++ {
		if b[i] == '%' {
			b[i+1] = toLowerASCII(b[i+1])
			b[i+2] = toLowerASCII(b[i+2])
			i += 2
		}
	}
	return string(b)
}

func toLowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// matcher is an Aho-Corasick automaton over every encoded form. One pass
// over the text finds every occurrence of every form.
type matcher struct {
	nodes []acNode
	root  [256]int32 // the root's transitions, dense for speed
	pats  []pattern
}

type pattern struct {
	name   string
	length int
}

type acEdge struct {
	c  byte
	to int32
}

type acNode struct {
	edges []acEdge
	fail  int32
	out   []int32 // patterns that end here, including through fail links
}

func (m *matcher) child(n int32, c byte) int32 {
	if n == 0 {
		return m.root[c]
	}
	for _, e := range m.nodes[n].edges {
		if e.c == c {
			return e.to
		}
	}
	return -1
}

func buildMatcher(vals []NamedValue) *matcher {
	m := &matcher{nodes: []acNode{{}}}
	for i := range m.root {
		m.root[i] = -1
	}
	for _, nv := range vals {
		for _, form := range encodedForms(nv.Value) {
			id := int32(len(m.pats))
			m.pats = append(m.pats, pattern{name: nv.Name, length: len(form)})
			n := int32(0)
			for i := 0; i < len(form); i++ {
				c := form[i]
				next := m.child(n, c)
				if next < 0 {
					next = int32(len(m.nodes))
					m.nodes = append(m.nodes, acNode{})
					if n == 0 {
						m.root[c] = next
					} else {
						m.nodes[n].edges = append(m.nodes[n].edges, acEdge{c, next})
					}
				}
				n = next
			}
			m.nodes[n].out = append(m.nodes[n].out, id)
		}
	}
	// Breadth-first fail links. A missing root transition loops to the root.
	var queue []int32
	for c := range m.root {
		if m.root[c] < 0 {
			m.root[c] = 0
		} else {
			queue = append(queue, m.root[c])
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, e := range m.nodes[n].edges {
			f := m.nodes[n].fail
			for f != 0 && m.child(f, e.c) < 0 {
				f = m.nodes[f].fail
			}
			target := m.child(f, e.c)
			if target < 0 || target == e.to {
				target = 0
			}
			m.nodes[e.to].fail = target
			m.nodes[e.to].out = append(m.nodes[e.to].out, m.nodes[target].out...)
			queue = append(queue, e.to)
		}
	}
	return m
}

// scan calls hit for every occurrence of every pattern in text.
func (m *matcher) scan(text string, hit func(start, end int, name string)) {
	if len(m.pats) == 0 {
		return
	}
	n := int32(0)
	for i := 0; i < len(text); i++ {
		c := text[i]
		for n != 0 && m.child(n, c) < 0 {
			n = m.nodes[n].fail
		}
		n = m.child(n, c)
		for _, id := range m.nodes[n].out {
			p := m.pats[id]
			hit(i+1-p.length, i+1, p.name)
		}
	}
}
