// Package link turns adapter snapshots into chains. Everything here is pure:
// no I/O, no clocks. Compare results come in through model.CompareFunc.
package link

import (
	"regexp"
	"slices"
	"strings"
)

// keyRe finds PROJ-123 at the start of a text or after a non-alphanumeric
// character ("feature/abc-12_fix", "feature_abc-12-x"). RE2 has no lookaround,
// so the leading boundary is matched as a group; there is no trailing boundary,
// so "abc-12fix" yields ABC-12.
var keyRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])([a-z][a-z0-9]+)-(\d+)`)

// KeysIn returns the distinct ticket keys in texts whose project is one of
// projects (compared case-insensitively), uppercased, in first-seen order.
func KeysIn(projects []string, texts ...string) []string {
	var keys []string
	for _, t := range texts {
		for _, m := range keyRe.FindAllStringSubmatch(t, -1) {
			proj := strings.ToUpper(m[1])
			if !slices.ContainsFunc(projects, func(p string) bool { return strings.EqualFold(p, proj) }) {
				continue
			}
			k := proj + "-" + m[2]
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	return keys
}
