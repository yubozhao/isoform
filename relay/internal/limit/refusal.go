package limit

import "fmt"

// Refusal carries the counters observed by the check while preserving its wire error.
type Refusal struct {
	Name       string
	Count, Cap int64
	Err        error
}

// Full describes a fixed-size bucket whose rejected events are never counted.
func Full(name string, cap int, err error) *Refusal {
	return &Refusal{Name: name, Count: int64(cap), Cap: int64(cap), Err: err}
}

func (e *Refusal) Error() string { return e.Err.Error() }
func (e *Refusal) Unwrap() error { return e.Err }

// Line formats a rejection using only a fixed name, route template, and counters.
func Line(route, name string, count, cap int64) string {
	return fmt.Sprintf("status=429 limit=%s count=%d cap=%d route=%s", name, count, cap, route)
}
