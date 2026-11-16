package main

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// perfStages times the steps of one slow binding read and logs them as one
// line, "[perf] binding=<name> stage=steps <step>_ms=<n> ...", only when the
// whole read took at least the threshold. Timings only, never content. The
// line has no "ms=" right after the binding name, so the page budgets
// (scripts/daily-trial/today-budgets.mjs) do not count it as a read.
type perfStages struct {
	binding string
	start   time.Time
	last    time.Time
	steps   []string
}

func newPerfStages(binding string) *perfStages {
	now := time.Now()
	return &perfStages{binding: binding, start: now, last: now}
}

// mark ends the step that began at the previous mark (or the start).
func (p *perfStages) mark(step string) {
	now := time.Now()
	p.steps = append(p.steps, fmt.Sprintf("%s_ms=%d", step, now.Sub(p.last).Milliseconds()))
	p.last = now
}

func (p *perfStages) line() string {
	return fmt.Sprintf("[perf] binding=%s stage=steps total_ms=%d %s", p.binding, time.Since(p.start).Milliseconds(), strings.Join(p.steps, " "))
}

func (p *perfStages) logIfOver(threshold time.Duration) {
	if time.Since(p.start) >= threshold {
		log.Print(p.line())
	}
}
