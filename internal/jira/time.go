// Copied from jira-green internal/jira/time.go (2026-10-06).

package jira

import (
	"bytes"
	"fmt"
	"strconv"
	"time"
)

// Time parses Jira's timestamp format, which is not RFC 3339
// ("2026-09-30T10:00:00.000-0400").
type Time struct{ time.Time }

// layouts are tried in order: Jira's usual format, Jira without
// milliseconds, then RFC 3339.
var layouts = []string{"2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05-0700", time.RFC3339}

// UnmarshalJSON accepts null (the zero time) or a JSON string in Jira's
// layout (with or without milliseconds) or RFC 3339. Anything else is an error.
func (t *Time) UnmarshalJSON(b []byte) error {
	t.Time = time.Time{}
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	if len(b) == 0 || b[0] != '"' {
		return fmt.Errorf("jira: time %s is not a JSON string", b)
	}
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return fmt.Errorf("jira: time %s is not a JSON string", b)
	}
	for _, l := range layouts {
		if p, err := time.Parse(l, s); err == nil {
			t.Time = p
			return nil
		}
	}
	return fmt.Errorf("jira: unparseable time %q", s)
}
