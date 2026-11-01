package sched

import (
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// designSkill is the kernel's UI design skill. A run that holds it names
// it in its brief.
const designSkill = "loop-impeccable"

// Level is how far a checkout's work reaches, as the kind of the item it
// holds says.
type Level int

// The three levels. NoLevel holds no conditional skill.
const (
	NoLevel Level = iota
	EpicLevel
	TicketLevel
)

// Scope is the level of one checkout. May holds the "May change" entries
// of a ticket, and only TicketLevel reads it.
type Scope struct {
	Level Level
	May   []string
}

// ScopeOf is the scope of a run that holds an item of kind with body. An
// epic is the epic level, a ticket the ticket level, and any other kind,
// or no item, no level.
func ScopeOf(kind machine.Kind, body string) Scope {
	switch kind {
	case machine.KindEpic:
		return Scope{Level: EpicLevel}
	case machine.KindTicket:
		return Scope{Level: TicketLevel, May: admission.MayChangeEntries(body)}
	}
	return Scope{}
}

// Select withholds from p each conditional skill whose condition fails for
// the project config cfg and the scope s, and names the design skill when
// the run holds it. Where the run would hold loop-impeccable and
// ui.design_skill is set, the override takes its place. A nil cfg holds no
// condition. p holds the run's user skills, so the override is named only
// when the run holds it.
func Select(p brief.Pack, cfg *config.ProjectConfig, s Scope) brief.Pack {
	var withheld []string
	for skill, key := range p.Conditional {
		if !holdsCondition(cfg, key, s) {
			withheld = append(withheld, skill)
		}
	}
	p = p.Without(withheld...)
	if skills, err := p.Skills(); err != nil || !slices.Contains(skills, designSkill) {
		return p
	}
	if cfg == nil || cfg.UI == nil || cfg.UI.DesignSkill == "" {
		p.Design = designSkill
		return p
	}
	p = p.Without(designSkill)
	if holdsOverride(p, cfg) {
		p.Design = cfg.UI.DesignSkill
	}
	return p
}

// holdsOverride reports whether the run holds the skill ui.design_skill
// names. A pin is held when the store gave the run its tree. A name that
// is no pin is a skill root entry that the default branch tracks
// (config.DesignSkill), so the checkout holds it from git.
func holdsOverride(p brief.Pack, cfg *config.ProjectConfig) bool {
	name := cfg.UI.DesignSkill
	if !slices.ContainsFunc(cfg.Skills, func(pin config.SkillPin) bool { return pin.Name == name }) {
		return true
	}
	return slices.ContainsFunc(p.User, func(u brief.UserSkill) bool { return u.Pin.Name == name })
}

// holdsCondition reports whether a skill conditional on key holds for the
// scope. The epic level needs the key declared. The ticket level needs it
// declared, and one of the key's paths to meet a "May change" entry when
// its block holds paths.
func holdsCondition(cfg *config.ProjectConfig, key string, s Scope) bool {
	if cfg == nil || s.Level == NoLevel {
		return false
	}
	d, ok := cfg.Declares(key)
	if !ok || s.Level == EpicLevel || len(d.Paths) == 0 {
		return ok
	}
	for _, g := range d.Paths {
		for _, e := range s.May {
			if meets(g, e) {
				return true
			}
		}
	}
	return false
}

// meets reports whether the glob g meets one "May change" entry. A literal
// path matches by g. A glob or a directory entry meets g when g reaches
// its literal directory: the entry cut at the last / before its first
// glob character.
func meets(g config.Glob, entry string) bool {
	entry = strings.TrimPrefix(entry, "./")
	i := strings.IndexAny(entry, "*?[")
	if i < 0 && !strings.HasSuffix(entry, "/") {
		return g.Match(entry)
	}
	if i < 0 {
		i = len(entry)
	}
	return g.Reaches(entry[:max(strings.LastIndex(entry[:i], "/"), 0)])
}
