package resolver

import "time"

// SetInterval replaces the poll interval for a test.
func SetInterval(r *Resolver, d time.Duration) { r.interval = d }
