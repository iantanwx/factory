package beadsx

import (
	"fmt"
	"regexp"
	"strings"
)

// The name rule bd 1.3.1 applies to custom statuses. The adapter applies the
// same rule to custom types and to metadata keys, so the fake and bd agree.
var (
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	keyPattern  = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

func validateID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%w: issue id %q", ErrInvalid, id)
	}
	return nil
}

// ValidateStatuses checks custom statuses the way bd 1.3.1 does: a lowercase
// name that starts with a letter, no collision with a built-in status, no
// duplicate, and one of the four categories.
func ValidateStatuses(defs []StatusDef) error {
	seen := map[string]bool{}
	for _, d := range defs {
		if !namePattern.MatchString(d.Name) {
			return fmt.Errorf("%w: status name %q must match %s", ErrInvalid, d.Name, namePattern)
		}
		if IsBuiltinStatus(d.Name) {
			return fmt.Errorf("%w: custom status %q collides with a built-in status", ErrInvalid, d.Name)
		}
		if seen[d.Name] {
			return fmt.Errorf("%w: status %q defined twice", ErrInvalid, d.Name)
		}
		seen[d.Name] = true
		switch d.Category {
		case CategoryActive, CategoryWIP, CategoryDone, CategoryFrozen:
		default:
			return fmt.Errorf("%w: status %q has category %q", ErrInvalid, d.Name, d.Category)
		}
	}
	return nil
}

func statusConfigValue(defs []StatusDef) string {
	parts := make([]string, len(defs))
	for i, d := range defs {
		parts[i] = d.Name + ":" + string(d.Category)
	}
	return strings.Join(parts, ",")
}

// ValidateTypes checks custom issue types.
func ValidateTypes(types []string) error {
	seen := map[string]bool{}
	for _, t := range types {
		if !namePattern.MatchString(t) {
			return fmt.Errorf("%w: type name %q must match %s", ErrInvalid, t, namePattern)
		}
		if builtinTypes[t] {
			return fmt.Errorf("%w: custom type %q collides with a built-in type", ErrInvalid, t)
		}
		if seen[t] {
			return fmt.Errorf("%w: type %q defined twice", ErrInvalid, t)
		}
		seen[t] = true
	}
	return nil
}

func validateMetadataKeys(set map[string]string, unset []string) error {
	for k := range set {
		if !keyPattern.MatchString(k) {
			return fmt.Errorf("%w: metadata key %q", ErrInvalid, k)
		}
	}
	for _, k := range unset {
		if !keyPattern.MatchString(k) {
			return fmt.Errorf("%w: metadata key %q", ErrInvalid, k)
		}
		if _, dup := set[k]; dup {
			return fmt.Errorf("%w: metadata key %q is both set and unset", ErrInvalid, k)
		}
	}
	return nil
}

// Validate checks a CreateSpec before any write.
func (s CreateSpec) Validate() error {
	if strings.TrimSpace(s.Title) == "" {
		return fmt.Errorf("%w: a title is required", ErrInvalid)
	}
	if s.Priority < 0 || s.Priority > 4 {
		return fmt.Errorf("%w: priority %d is not 0 to 4", ErrInvalid, s.Priority)
	}
	return validateMetadataKeys(s.Metadata, nil)
}

// Validate checks an UpdateSpec before any write. An expected status is
// required, because every server write is conditional (KTD6 step 2).
func (s UpdateSpec) Validate() error {
	if s.IfStatus == "" {
		return fmt.Errorf("%w: an expected status is required", ErrInvalid)
	}
	if s.Status == "" && s.Assignee == nil && len(s.SetMetadata)+len(s.UnsetMetadata) == 0 {
		return fmt.Errorf("%w: an update must change a field", ErrInvalid)
	}
	return validateMetadataKeys(s.SetMetadata, s.UnsetMetadata)
}

// Match reports whether an issue matches the filter.
func (f Filter) Match(is Issue) bool {
	if len(f.Statuses) > 0 {
		ok := false
		for _, s := range f.Statuses {
			ok = ok || s == is.Status
		}
		if !ok {
			return false
		}
	}
	if f.Type != "" && f.Type != is.Type {
		return false
	}
	for k, v := range f.Metadata {
		if got, ok := is.Metadata[k]; !ok || got != v {
			return false
		}
	}
	return true
}
