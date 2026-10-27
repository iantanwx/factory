package admission

import (
	"slices"
	"strings"
)

// AlertNamesRun reports whether a token of the alert's dedupe key or
// subject is the run id, or ends in "-" + the run id, as a checkout
// directory does. A token is a run of [A-Za-z0-9._-] with a final "."
// trimmed.
func AlertNamesRun(dedupeKey, subject, run string) bool {
	return slices.ContainsFunc(alertTokens(dedupeKey, subject), func(t string) bool {
		return t == run || strings.HasSuffix(t, "-"+run)
	})
}

// AlertNamesItem reports whether a token of the alert's dedupe key or
// subject is the item id. The caller also matches the alert's project.
func AlertNamesItem(dedupeKey, subject, item string) bool {
	return slices.Contains(alertTokens(dedupeKey, subject), item)
}

func alertTokens(fields ...string) []string {
	var out []string
	for _, f := range fields {
		for _, t := range strings.FieldsFunc(f, func(r rune) bool { return !isTokenRune(r) }) {
			out = append(out, strings.TrimSuffix(t, "."))
		}
	}
	return out
}

func isTokenRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
}
