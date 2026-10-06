// Package link turns adapter snapshots into chains. Everything here is pure:
// no I/O, no clocks. Compare results come in through model.CompareFunc.
package link

import (
	"regexp"
	"slices"
	"strings"
)

var keyRe = regexp.MustCompile(`(?i)\b([a-z][a-z0-9]+)-(\d+)\b`)

// KeysIn returns the distinct ticket keys in texts whose project is one of
// projects, uppercased, in first-seen order.
func KeysIn(projects []string, texts ...string) []string {
	var keys []string
	for _, t := range texts {
		for _, m := range keyRe.FindAllStringSubmatch(t, -1) {
			proj := strings.ToUpper(m[1])
			if !slices.Contains(projects, proj) {
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
