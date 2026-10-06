package rules

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// Evaluate sets Stage and Flags on every chain, drops chains that have been
// in prod longer than FadeAfter, and sorts: red, yellow, none; then furthest
// stage first; then ticket key.
func Evaluate(chains []model.Chain, now time.Time, th Thresholds) []model.Chain {
	out := make([]model.Chain, 0, len(chains))
	for _, c := range chains {
		c.Stage = Stage(c)
		c.Flags = Flags(c, now, th)
		if since, ok := prodSince(c); ok && c.Stage == model.StageInProd && c.Level() == model.None &&
			th.FadeAfter > 0 && now.Sub(since) > th.FadeAfter {
			continue
		}
		out = append(out, c)
	}
	slices.SortStableFunc(out, func(a, b model.Chain) int {
		if a.Level() != b.Level() {
			return int(b.Level() - a.Level())
		}
		if a.Stage != b.Stage {
			return int(b.Stage - a.Stage)
		}
		return compareKeys(a.Ticket.Key, b.Ticket.Key)
	})
	return out
}

// prodSince is the latest time an applicable prod slot was deployed. ok is
// false when one has no time, so a row never fades on a missing timestamp.
func prodSince(c model.Chain) (since time.Time, ok bool) {
	for _, s := range c.Slots {
		if !s.Applies || !s.Env.Prod || s.State != model.SlotDeployed {
			continue
		}
		if s.At.IsZero() {
			return time.Time{}, false
		}
		if s.At.After(since) {
			since = s.At
		}
	}
	return since, !since.IsZero()
}

// compareKeys orders ticket keys by project, then by number: ABC-9, ABC-10,
// XY-2. A key without a numeric suffix compares as plain text.
func compareKeys(a, b string) int {
	ap, an, aok := splitKey(a)
	bp, bn, bok := splitKey(b)
	if !aok || !bok {
		return strings.Compare(a, b)
	}
	if c := strings.Compare(ap, bp); c != 0 {
		return c
	}
	return cmp.Compare(an, bn)
}

func splitKey(k string) (project string, n int, ok bool) {
	i := strings.LastIndexByte(k, '-')
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(k[i+1:])
	return k[:i], n, err == nil
}
