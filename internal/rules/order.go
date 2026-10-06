package rules

import (
	"slices"
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
		if c.Stage == model.StageInProd && c.Level() == model.None && th.FadeAfter > 0 &&
			now.Sub(prodSince(c)) > th.FadeAfter {
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

// prodSince is the latest time a prod slot was deployed.
func prodSince(c model.Chain) time.Time {
	var t time.Time
	for _, s := range c.Slots {
		if s.Env.Prod && s.State == model.SlotDeployed && s.At.After(t) {
			t = s.At
		}
	}
	return t
}

func compareKeys(a, b string) int {
	if len(a) != len(b) {
		return len(a) - len(b) // ABC-9 before ABC-10
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
