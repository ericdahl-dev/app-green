package link_test

import (
	"reflect"
	"testing"

	"github.com/ericdahl-dev/app-green/internal/link"
)

func TestKeysIn(t *testing.T) {
	projects := []string{"ABC"}
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"ABC-12: Fix the thing", "abc-12-fix-thing"}, []string{"ABC-12"}},
		{[]string{"[3/5] ABC-7: Step three", "abc-7-step"}, []string{"ABC-7"}},
		{[]string{"ABC-1, ABC-2: both", ""}, []string{"ABC-1", "ABC-2"}},
		{[]string{"XYZ-9: other project", "xyz-9"}, nil},
		{[]string{"Bump rails", "dependabot/bundler/rails-8"}, nil},
		{[]string{"UTF-8 handling", ""}, nil}, // not a configured project
		{[]string{"", "feature/abc-12_fix"}, []string{"ABC-12"}},
		{[]string{"", "feature_abc-12-x"}, []string{"ABC-12"}},
		{[]string{"", "abc-12fix"}, []string{"ABC-12"}},
		{[]string{"", "xabc-12"}, nil},
	}
	for _, c := range cases {
		if got := link.KeysIn(projects, c.in...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("KeysIn(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestKeysInProjectCaseInsensitive(t *testing.T) {
	got := link.KeysIn([]string{"abc"}, "abc-12: lower-case config")
	if !reflect.DeepEqual(got, []string{"ABC-12"}) {
		t.Errorf("KeysIn with project \"abc\" = %v, want [ABC-12]", got)
	}
}
