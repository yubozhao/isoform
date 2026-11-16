package main

import (
	"regexp"
	"strings"
	"testing"
)

// A step line names each step's time and is never counted as a binding read
// by the page budgets, whose read pattern is "binding=<name> ms=<n>".
func TestPerfStagesLineIsNotABindingRead(t *testing.T) {
	stages := newPerfStages("GetPersonDetail")
	stages.mark("base")
	stages.mark("loops")
	line := stages.line()
	if !strings.Contains(line, "base_ms=") || !strings.Contains(line, "loops_ms=") || !strings.Contains(line, "total_ms=") {
		t.Fatalf("steps missing: %s", line)
	}
	if regexp.MustCompile(`\[perf\] binding=(\w+) ms=(\d+)`).MatchString(line) {
		t.Fatalf("budget pattern counts the step line as a read: %s", line)
	}
}
