package orca

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// codexCommand is one command an exec wrapper starts: a launch of a shell
// command, or a poll of a running command session.
type codexCommand struct{ launch, poll string }

// wrapperCommands enumerates the commands of a Codex exec wrapper. It ports
// a strict subset of the wrapper language of alpha's codex-wrappers.ts and
// codex-quiet.ts. Each statement is one of:
//
//	text(await tools.exec_command({...}));
//	text(await tools.write_stdin({...}));
//	text((await tools.exec_command({...})).output);
//	text(await tools.apply_patch("..."));
//	image((await tools.view_image({...})).image_url);
//
// and every argument is a literal. apply_patch and view_image start no
// process. Any other source answers false, and the ledger keeps the session.
// The source is data. It is never run.
func wrapperCommands(src string) ([]codexCommand, bool) {
	p := &wrapperParser{src: src}
	out := []codexCommand{}
	statements := 0
	p.space()
	for p.i < len(p.src) {
		c, ok := p.statement()
		if !ok {
			return nil, false
		}
		statements++
		out = append(out, c...)
		gap := p.space()
		switch {
		case p.eat(";"):
			p.space()
		case p.i < len(p.src) && !strings.ContainsAny(gap, "\n\r"):
			return nil, false
		}
	}
	return out, statements > 0
}

type wrapperParser struct {
	src string
	i   int
}

// space skips white space and comments. It returns what it skipped, so a
// caller can tell whether a line ended.
func (p *wrapperParser) space() string {
	start := p.i
	for p.i < len(p.src) {
		rest := p.src[p.i:]
		switch {
		case strings.IndexByte(" \t\n\r", rest[0]) >= 0:
			p.i++
		case strings.HasPrefix(rest, "//"):
			end := strings.IndexAny(rest, "\n\r")
			if end < 0 {
				end = len(rest)
			}
			p.i += end
		case strings.HasPrefix(rest, "/*"):
			end := strings.Index(rest[2:], "*/")
			if end < 0 {
				return p.src[start:p.i]
			}
			p.i += end + 4
		default:
			return p.src[start:p.i]
		}
	}
	return p.src[start:p.i]
}

func (p *wrapperParser) eat(s string) bool {
	if strings.HasPrefix(p.src[p.i:], s) {
		p.i += len(s)
		return true
	}
	return false
}

// token eats s after optional space.
func (p *wrapperParser) token(s string) bool {
	p.space()
	return p.eat(s)
}

func identStart(b byte) bool {
	return b == '_' || b == '$' || (b|0x20 >= 'a' && b|0x20 <= 'z')
}

func identPart(b byte) bool { return identStart(b) || (b >= '0' && b <= '9') }

func (p *wrapperParser) ident() string {
	p.space()
	start := p.i
	if p.i < len(p.src) && identStart(p.src[p.i]) {
		for p.i < len(p.src) && identPart(p.src[p.i]) {
			p.i++
		}
	}
	return p.src[start:p.i]
}

// statement reads one statement and returns the commands it starts.
func (p *wrapperParser) statement() ([]codexCommand, bool) {
	printer := p.ident()
	if (printer != "text" && printer != "image") || !p.token("(") {
		return nil, false
	}
	projected := p.token("(")
	method, args, ok := p.toolCall()
	if !ok {
		return nil, false
	}
	if projected {
		field := "output"
		if printer == "image" {
			field = "image_url"
		}
		if !p.token(")") || !p.token(".") || p.ident() != field {
			return nil, false
		}
	}
	if !p.token(")") {
		return nil, false
	}
	switch {
	case printer == "image":
		return nil, projected && method == "view_image"
	case method == "apply_patch":
		return nil, true
	case method == "exec_command":
		if cmd, ok := args["cmd"].(string); ok {
			return []codexCommand{{launch: cmd}}, true
		}
	case method == "write_stdin":
		if id, ok := args["session_id"].(float64); ok && id == float64(int64(id)) && id > -1e15 && id < 1e15 {
			return []codexCommand{{poll: strconv.FormatInt(int64(id), 10)}}, true
		}
	}
	return nil, false
}

// toolCall reads await tools.<method>(<literal>). apply_patch takes one
// string. Every other method takes one object.
func (p *wrapperParser) toolCall() (string, map[string]any, bool) {
	if p.ident() != "await" || p.ident() != "tools" || !p.token(".") {
		return "", nil, false
	}
	method := p.ident()
	if !p.token("(") {
		return "", nil, false
	}
	var args map[string]any
	ok := false
	if method == "apply_patch" {
		p.space()
		if p.i < len(p.src) && strings.IndexByte("\"'`", p.src[p.i]) >= 0 {
			_, ok = p.str()
		}
	} else {
		args, ok = p.object()
	}
	return method, args, ok && p.token(")")
}

// object reads an object literal. It returns the string and number values
// of its top-level keys. A nested literal is checked and not returned.
func (p *wrapperParser) object() (map[string]any, bool) {
	if !p.token("{") {
		return nil, false
	}
	out := map[string]any{}
	for !p.token("}") {
		var key string
		p.space()
		if p.i < len(p.src) && (p.src[p.i] == '"' || p.src[p.i] == '\'') {
			s, ok := p.str()
			if !ok {
				return nil, false
			}
			key = s
		} else if key = p.ident(); key == "" {
			return nil, false
		}
		if _, dup := out[key]; dup || key == "__proto__" || !p.token(":") {
			return nil, false
		}
		v, ok := p.value()
		if !ok {
			return nil, false
		}
		out[key] = v
		if !p.token(",") {
			if !p.token("}") {
				return nil, false
			}
			break
		}
	}
	return out, true
}

func (p *wrapperParser) array() bool {
	if !p.token("[") {
		return false
	}
	for !p.token("]") {
		if _, ok := p.value(); !ok {
			return false
		}
		if !p.token(",") {
			return p.token("]")
		}
	}
	return true
}

// value reads a literal. A string or a number comes back; any other
// literal comes back as nil.
func (p *wrapperParser) value() (any, bool) {
	p.space()
	if p.i >= len(p.src) {
		return nil, false
	}
	switch b := p.src[p.i]; {
	case b == '"' || b == '\'' || b == '`':
		s, ok := p.str()
		return s, ok
	case b == '{':
		_, ok := p.object()
		return nil, ok
	case b == '[':
		return nil, p.array()
	case b == '-' || b == '+':
		p.i++
		_, ok := p.number()
		return nil, ok
	case b == '.' || (b >= '0' && b <= '9'):
		return p.number()
	}
	switch p.ident() {
	case "true", "false", "null":
		return nil, true
	}
	return nil, false
}

// number reads a decimal literal. Hex, octal, binary, separators and BigInt
// answer false.
func (p *wrapperParser) number() (any, bool) {
	start := p.i
	digits := func() int {
		n := 0
		for p.i < len(p.src) && p.src[p.i] >= '0' && p.src[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	whole := digits()
	if whole > 1 && p.src[start] == '0' {
		return nil, false
	}
	frac := 0
	if p.eat(".") {
		frac = digits()
	}
	if whole == 0 && frac == 0 {
		return nil, false
	}
	if p.i < len(p.src) && (p.src[p.i] == 'e' || p.src[p.i] == 'E') {
		p.i++
		if !p.eat("+") {
			p.eat("-")
		}
		if digits() == 0 {
			return nil, false
		}
	}
	if p.i < len(p.src) && (identPart(p.src[p.i]) || p.src[p.i] == '.') {
		return nil, false
	}
	v, err := strconv.ParseFloat(p.src[start:p.i], 64)
	return v, err == nil
}

// str reads a string or template literal and cooks its escapes. A template
// with a substitution, an escape JavaScript reads loosely, or a lone
// surrogate answers false.
func (p *wrapperParser) str() (string, bool) {
	quote := p.src[p.i]
	p.i++
	var b strings.Builder
	for p.i < len(p.src) {
		c := p.src[p.i]
		switch {
		case c == quote:
			p.i++
			return b.String(), true
		case c == '\\':
			p.i++
			if !p.escape(&b) {
				return "", false
			}
		case quote == '`' && c == '$' && strings.HasPrefix(p.src[p.i:], "${"):
			return "", false
		case c == '\r' && quote == '`':
			p.i++
			p.eat("\n")
			b.WriteByte('\n')
		case (c == '\n' || c == '\r') && quote != '`':
			return "", false
		default:
			r, n := utf8.DecodeRuneInString(p.src[p.i:])
			if r == utf8.RuneError && n <= 1 {
				return "", false
			}
			b.WriteString(p.src[p.i : p.i+n])
			p.i += n
		}
	}
	return "", false
}

var simpleEscapes = map[byte]string{
	'n': "\n", 't': "\t", 'r': "\r", 'b': "\b", 'f': "\f", 'v': "\v",
	'\'': "'", '"': "\"", '\\': "\\", '`': "`", '$': "$", '\n': "",
}

func (p *wrapperParser) escape(b *strings.Builder) bool {
	if p.i >= len(p.src) {
		return false
	}
	c := p.src[p.i]
	p.i++
	if s, ok := simpleEscapes[c]; ok {
		b.WriteString(s)
		return true
	}
	switch c {
	case '\r':
		p.eat("\n")
		return true
	case '0':
		if p.i < len(p.src) && p.src[p.i] >= '0' && p.src[p.i] <= '9' {
			return false
		}
		b.WriteByte(0)
		return true
	case 'x':
		v, ok := p.hex(2)
		if ok {
			b.WriteRune(rune(v))
		}
		return ok
	case 'u':
		r, ok := p.unicode()
		if !ok {
			return false
		}
		if r >= 0xd800 && r <= 0xdbff {
			if !p.eat(`\u`) {
				return false
			}
			lo, ok := p.unicode()
			if !ok || lo < 0xdc00 || lo > 0xdfff {
				return false
			}
			r = 0x10000 + (r-0xd800)<<10 + (lo - 0xdc00)
		} else if r >= 0xdc00 && r <= 0xdfff {
			return false
		}
		b.WriteRune(r)
		return true
	}
	return false
}

func (p *wrapperParser) unicode() (rune, bool) {
	if !p.eat("{") {
		v, ok := p.hex(4)
		return rune(v), ok
	}
	end := strings.IndexByte(p.src[p.i:], '}')
	if end <= 0 {
		return 0, false
	}
	v, err := strconv.ParseUint(p.src[p.i:p.i+end], 16, 32)
	p.i += end + 1
	return rune(v), err == nil && v <= utf8.MaxRune
}

func (p *wrapperParser) hex(n int) (uint64, bool) {
	if p.i+n > len(p.src) {
		return 0, false
	}
	v, err := strconv.ParseUint(p.src[p.i:p.i+n], 16, 32)
	p.i += n
	return v, err == nil
}
