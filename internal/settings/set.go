package settings

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/sched"
)

// SetRequest is one call of `loop settings set`.
type SetRequest struct {
	Project string
	Key     string
	Value   *string // nil: --unset
	Reason  string
	Actor   string
}

// Set records one change of one setting as proposed, then proposes it
// through its own pull request, and the change becomes open. A refusal
// lands nothing. When the pull request does not open, Set answers the
// change in state proposed.
func (s *Service) Set(ctx context.Context, r SetRequest) (Change, *cli.Refusal) {
	reason := strings.TrimSpace(r.Reason)
	if reason == "" {
		return Change{}, &cli.Refusal{Code: cli.CodeUsage, Rule: "no_reason", Hint: "--reason says why the setting changes; a person reads it"}
	}
	if ref := s.Redactor.Refusal("secret_in_settings_reason", "reason", reason); ref != nil {
		return Change{}, ref
	}
	if c, err := s.live(ctx, r.Project); err != nil {
		return Change{}, cli.InternalRefusal(err)
	} else if c != nil {
		return Change{}, pending(*c)
	}
	repo, err := s.Repo(ctx, r.Project)
	if err != nil {
		return Change{}, cli.InternalRefusal(fmt.Errorf("settings: fetch the default branch of %s: %w", r.Project, err))
	}
	hc, err := s.Host(ctx)
	if err != nil {
		return Change{}, cli.InternalRefusal(fmt.Errorf("settings: load host config: %w", err))
	}
	raw, err := repo.read(ctx, "", config.ProjectConfigPath)
	if errors.Is(err, fs.ErrNotExist) {
		return Change{}, invalid(r.Key, fmt.Sprintf("%s holds no %s; loop-init writes it", repo.Branch, config.ProjectConfigPath))
	}
	if err != nil {
		return Change{}, cli.InternalRefusal(err)
	}
	e, ref := editSetting(hc, raw, r.Key, r.Value, s.Defaults)
	if ref != nil {
		return Change{}, ref
	}
	if r.Key == "merge.mechanism" && r.Value != nil {
		if err := s.CheckMechanism(ctx, r.Project, config.MergeMechanism(*r.Value)); err != nil {
			return Change{}, invalid(r.Key, "the mechanism's check fails: "+err.Error())
		}
	}
	c := Change{Project: r.Project, Setting: r.Key, Old: e.old.Value, OldSource: e.old.Source, New: r.Value,
		Reason: reason, Actor: r.Actor}
	if err := s.record(ctx, &c, repo.Head); errors.Is(err, errLive) {
		live, err := s.live(ctx, r.Project)
		if err != nil || live == nil {
			return Change{}, cli.InternalRefusal(fmt.Errorf("settings: read the live change of %s: %v", r.Project, err))
		}
		return Change{}, pending(*live)
	} else if err != nil {
		return Change{}, cli.InternalRefusal(fmt.Errorf("settings: record the change: %w", err))
	}
	msg := c.message()
	subject, _, _ := strings.Cut(msg, "\n")
	pr, head, err := s.Propose(ctx, r.Project, github.FileChange{Branch: c.Branch, Path: config.ProjectConfigPath,
		BaseSHA: repo.Head, Content: e.raw, Message: msg, Title: subject, Body: "```\n" + msg + "```\n"})
	if err == nil {
		err = s.opened(ctx, &c, pr, head)
	}
	if err != nil {
		c.Detail = "the pull request did not open: " + err.Error()
	}
	return c, nil
}

// message is the commit message of c: the subject, the reason, and the
// trailers that name the setting, the old value and the actor.
func (c Change) message() string {
	subject := "chore(settings): unset " + c.Setting
	if c.New != nil {
		subject = fmt.Sprintf("chore(settings): set %s to %s", c.Setting, *c.New)
	}
	return fmt.Sprintf("%s\n\n%s\n\nLoop-Setting: %s\nLoop-Old-Value: %s (%s)\nLoop-Changed-By: %s\n",
		subject, c.Reason, c.Setting, c.Old, c.OldSource, c.Actor)
}

// edit is one change of the project file that the command accepts.
type edit struct {
	old config.Setting // the named setting in force before the change
	raw []byte         // the proposed file
}

// editSetting sets key to value in raw, the file at the default branch
// head, or removes key when value is nil. It refuses a key that names no
// setting, a field other than enabled on a run stage or an agent job that
// raw does not list, a file that does not validate, and a change that
// moves neither the setting's value nor its source in force. d gives the
// loop's defaults for one config, or nil for none.
func editSetting(hc *config.HostConfig, raw []byte, key string, value *string, d func(*config.ProjectConfig) config.Defaults) (edit, *cli.Refusal) {
	known := config.KnownStages(hc)
	k, err := config.ParseSettingKey(key, known)
	if err != nil {
		return edit{}, invalid(key, err.Error())
	}
	base, err := config.ParseProjectConfig(raw)
	if err != nil {
		return edit{}, invalid(key, "the default branch's config does not load: "+err.Error())
	}
	section, rest, _ := strings.Cut(key, ".")
	stage, field, _ := strings.Cut(rest, ".")
	if section == "stages" && field != "enabled" {
		kind, _ := config.KindOf(stage, known)
		if _, listed := base.Stages[stage]; !listed && kind != config.KindIntake && kind != config.KindProjectJob {
			return edit{}, invalid(key, fmt.Sprintf("the project does not list %s, so a new entry would turn it on; set stages.%s.enabled first", stage, stage))
		}
	}
	if field == "schedule" && value != nil {
		if _, err := sched.ParseCron(*value); err != nil {
			return edit{}, invalid(key, err.Error())
		}
	}
	next, err := config.SetSetting(raw, k, value)
	if err != nil {
		return edit{}, invalid(key, err.Error())
	}
	pc, err := config.ParseProjectConfig(next)
	if err == nil {
		err = pc.CheckStages(known)
	}
	if err != nil {
		return edit{}, invalid(key, err.Error())
	}
	old, now := named(hc, base, known, d, key), named(hc, pc, known, d, key)
	// An enabled row has no host value. Its source says only whether the
	// file lists the stage, so enabled: false on an unlisted stage moves
	// nothing that is in force.
	if old.Value == now.Value && (old.Source == now.Source || field == "enabled") {
		return edit{}, invalid(key, fmt.Sprintf("the change moves neither the value %q nor its source %s in force", old.Value, old.Source))
	}
	return edit{old: old, raw: next}, nil
}

// named returns the setting key of pc over hc in force.
func named(hc *config.HostConfig, pc *config.ProjectConfig, known config.Known, d func(*config.ProjectConfig) config.Defaults, key string) config.Setting {
	var defaults config.Defaults
	if d != nil {
		defaults = d(pc)
	}
	for _, st := range config.Effective(hc, pc).Settings(known, defaults) {
		if st.Key.String() == key {
			return st
		}
	}
	return config.Setting{}
}

// invalid is the INVALID_SETTING refusal of key.
func invalid(key, reason string) *cli.Refusal {
	return &cli.Refusal{Code: cli.CodeInvalidSetting, Rule: "invalid_setting", Hint: key + ": " + reason}
}
