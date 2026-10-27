package gates

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// project is what the root Makefile and the root package.json define. A
// runbook gives no working directory for its commands, so the gate reads
// these two files and no other.
type project struct {
	hasMakefile bool
	// open is a Makefile that includes another file or keeps a $ in a
	// target, so a name that matches no target can still be one.
	open     bool
	targets  map[string]bool
	patterns []string // targets that hold a %

	hasPackage bool
	scripts    map[string]string
}

// readProject reads the root Makefile and the root package.json. A
// missing file is a project without it.
func readProject(root string) (*project, error) {
	p := &project{targets: map[string]bool{}}
	raw, err := os.ReadFile(filepath.Join(root, "Makefile"))
	switch {
	case err == nil:
		p.hasMakefile = true
		p.readMakefile(string(raw))
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	raw, err = os.ReadFile(filepath.Join(root, "package.json"))
	switch {
	case err == nil:
		p.hasPackage = true
		var pkg struct{ Scripts map[string]string }
		if err := json.Unmarshal(raw, &pkg); err != nil {
			return nil, fmt.Errorf("package.json: %w", err)
		}
		p.scripts = pkg.Scripts
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	return p, nil
}

var (
	makeInclude = regexp.MustCompile(`^\s*-?s?include\s`)
	makeAssign  = regexp.MustCompile(`^(?:(?:export|override)\s+)*([A-Za-z0-9_.-]+)\s*([:?+!]*=)\s*(.*)$`)
	makeVarRef  = regexp.MustCompile(`\$[({]([A-Za-z0-9_.-]+)[)}]`)
)

// readMakefile collects each target and each .PHONY name. A variable in a
// target expands when the Makefile assigns it once, with =, := or ::=, to
// plain words.
func (p *project) readMakefile(text string) {
	type assign struct {
		n     int
		value string
		plain bool
	}
	vars := map[string]*assign{}
	var heads []string
	for _, l := range strings.Split(strings.ReplaceAll(text, "\\\n", " "), "\n") {
		if strings.HasPrefix(l, "\t") {
			continue // a recipe line
		}
		l, _, _ = strings.Cut(l, "#")
		if makeInclude.MatchString(l) {
			p.open = true
			continue
		}
		if m := makeAssign.FindStringSubmatch(l); m != nil {
			a := vars[m[1]]
			if a == nil {
				a = &assign{}
				vars[m[1]] = a
			}
			a.n++
			a.value = m[3]
			a.plain = (m[2] == "=" || m[2] == ":=" || m[2] == "::=") && !strings.Contains(m[3], "$")
			continue
		}
		if head, rest, ok := strings.Cut(l, ":"); ok {
			if strings.TrimSpace(head) == ".PHONY" {
				head = rest
			}
			heads = append(heads, head)
		}
	}
	for _, h := range heads {
		h = makeVarRef.ReplaceAllStringFunc(h, func(ref string) string {
			if a := vars[makeVarRef.FindStringSubmatch(ref)[1]]; a != nil && a.n == 1 && a.plain {
				return a.value
			}
			return ref
		})
		for _, t := range strings.Fields(h) {
			switch {
			case strings.Contains(t, "$"):
				p.open = true
			case strings.Contains(t, "%"):
				p.patterns = append(p.patterns, t)
			default:
				p.targets[t] = true
			}
		}
	}
}

// hasTarget reports whether make can build name. An open Makefile can
// build any name.
func (p *project) hasTarget(name string) bool {
	if p.open || p.targets[name] {
		return true
	}
	for _, pat := range p.patterns {
		pre, suf, _ := strings.Cut(pat, "%")
		if len(name) > len(pre)+len(suf) && strings.HasPrefix(name, pre) && strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

var (
	// commandSep starts a new command on a line: &&, ||, ;, | or (.
	commandSep = regexp.MustCompile(`&&|\|\||[;|()]`)
	// commandEnd ends the words of a command: a redirect, a & or a comment.
	commandEnd = regexp.MustCompile(`\s(?:\d*[<>&]|#)`)
)

// projectFindings reports each make target and each package script that a
// command at command position names and the project does not define.
// Command position is the start of a fenced line or a code span, after an
// optional `$ `, or after a command separator. Only the name is checked,
// never a flag or an argument after it.
func projectFindings(file, text string, p *project) []Finding {
	var out []Finding
	forEachCode(text, func(line int, code string) {
		for i, cmd := range commandSep.Split(code, -1) {
			if end := commandEnd.FindStringIndex(cmd); end != nil {
				cmd = cmd[:end[0]]
			}
			words := strings.Fields(cmd)
			if i == 0 && len(words) > 0 && words[0] == "$" {
				words = words[1:]
			}
			if len(words) == 0 {
				continue
			}
			switch words[0] {
			case "make":
				for _, t := range makeTargets(words[1:]) {
					if !p.hasMakefile {
						out = append(out, Finding{File: file, Line: line, Rule: "make_target",
							Message: fmt.Sprintf("`make %s` needs a root Makefile, and the repository has none", t)})
					} else if !p.hasTarget(t) {
						out = append(out, Finding{File: file, Line: line, Rule: "make_target",
							Message: fmt.Sprintf("`make %s` names no target of the root Makefile", t)})
					}
				}
			case "npm", "pnpm", "yarn", "bun":
				s, ok := scriptOf(words[0], words[1:])
				if !ok {
					continue
				}
				if !p.hasPackage {
					out = append(out, Finding{File: file, Line: line, Rule: "package_script",
						Message: fmt.Sprintf("`%s` calls the script %q, and the repository has no root package.json", words[0], s)})
				} else if _, ok := p.scripts[s]; !ok {
					out = append(out, Finding{File: file, Line: line, Rule: "package_script",
						Message: fmt.Sprintf("`%s` calls the script %q, and the root package.json names no such script", words[0], s)})
				}
			}
		}
	})
	return out
}

// isName reports whether a word is a name to check. A word with a
// placeholder character is not.
func isName(w string) bool {
	return w != "" && !strings.ContainsAny(w, "<>${}")
}

// makeTargets returns the targets of a make call. A call that names another
// directory or Makefile returns none.
func makeTargets(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := strings.Trim(args[i], `'"`)
		switch {
		case strings.HasPrefix(a, "-C") || strings.HasPrefix(a, "-f") ||
			strings.HasPrefix(a, "--directory") || strings.HasPrefix(a, "--file") || strings.HasPrefix(a, "--makefile"):
			return nil
		case (a == "-j" || a == "-l") && i+1 < len(args):
			if _, err := strconv.Atoi(args[i+1]); err == nil {
				i++
			}
		case strings.HasPrefix(a, "-") || strings.Contains(a, "="):
		case isName(a):
			out = append(out, a)
		}
	}
	return out
}

// elsewhereFlags make a package call run in another package, so the root
// package.json does not hold its script.
var elsewhereFlags = []string{"-C", "--dir", "--filter", "-F", "-r", "--recursive", "--prefix", "--cwd", "-w", "--workspace", "--workspaces"}

// builtins are the commands of pnpm and yarn that a bare `pnpm <x>` or
// `yarn <x>` runs in place of a script. test and start run a script of
// that name, as npm test does, and like npm test are not checked.
var builtins = map[string]map[string]bool{
	"pnpm": setOf("add", "approve-builds", "audit", "bin", "c", "cache", "cat-file", "cat-index", "config", "create",
		"dedupe", "deploy", "dlx", "doctor", "env", "exec", "fetch", "find-hash", "get", "help", "i", "ignored-builds",
		"import", "init", "install", "install-test", "it", "licenses", "link", "list", "ln", "login", "logout", "ls",
		"outdated", "pack", "patch", "patch-commit", "patch-remove", "prune", "publish", "rb", "rebuild", "remove",
		"restart", "rm", "root", "run", "self-update", "server", "set", "setup", "start", "store", "t", "test", "tst",
		"un", "uninstall", "unlink", "up", "update", "upgrade", "version", "why"),
	"yarn": setOf("add", "audit", "autoclean", "bin", "cache", "check", "config", "constraints", "create", "dedupe",
		"dlx", "exec", "explain", "generate-lock-entry", "global", "help", "import", "info", "init", "install",
		"licenses", "link", "list", "login", "logout", "node", "npm", "outdated", "owner", "pack", "patch",
		"patch-commit", "plugin", "policies", "publish", "rebuild", "remove", "run", "search", "set", "start", "tag",
		"team", "test", "unlink", "unplug", "up", "upgrade", "upgrade-interactive", "version", "versions", "why",
		"workspace", "workspaces"),
}

func setOf(words ...string) map[string]bool {
	m := map[string]bool{}
	for _, w := range words {
		m[w] = true
	}
	return m
}

// scriptOf returns the script that a package call runs. npm and bun run a
// script only through run. pnpm and yarn also run one bare, when the word
// is no built-in.
func scriptOf(tool string, args []string) (string, bool) {
	i := 0
	next := func() (string, bool) {
		for ; i < len(args); i++ {
			a := strings.Trim(args[i], `'"`)
			for _, f := range elsewhereFlags {
				if a == f || strings.HasPrefix(a, f+"=") {
					return "", false
				}
			}
			if !strings.HasPrefix(a, "-") {
				i++
				return a, true
			}
		}
		return "", false
	}
	w, ok := next()
	switch {
	case !ok:
		return "", false
	case w == "run" || w == "run-script":
		w, ok = next()
	case builtins[tool] == nil || builtins[tool][w]:
		return "", false
	}
	// A name with a / is a pnpm pattern or a file that bun runs. So is a
	// bun name with an extension, such as index.ts.
	if !ok || !isName(w) || strings.Contains(w, "/") || tool == "bun" && filepath.Ext(w) != "" {
		return "", false
	}
	return w, true
}
