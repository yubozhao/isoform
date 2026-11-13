package agent

import (
	"runtime"
	"strings"
)

// callerLabelFrames is how many frames outside this package a model-call
// label names, innermost first, so a shared helper still shows who called it.
const callerLabelFrames = 3

// CallerLabel names the code that asked for a model call: TaskInput.Caller
// when set, else the nearest functions outside this package. It holds code
// identifiers only, never prompt text or user content, so it is safe to log.
func CallerLabel(input *TaskInput) string {
	if input != nil && strings.TrimSpace(input.Caller) != "" {
		return strings.TrimSpace(input.Caller)
	}
	pcs := make([]uintptr, 32)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	var names []string
	for len(names) < callerLabelFrames {
		frame, more := frames.Next()
		if name := callerLabelFunc(frame.Function); name != "" {
			names = append(names, name)
		}
		if !more {
			break
		}
	}
	if len(names) == 0 {
		return "unknown"
	}
	return strings.Join(names, "<")
}

// callerLabelFunc shortens "wails-gui.(*App).foo.func1" to "(*App).foo" and
// drops frames inside this package and the runtime.
func callerLabelFunc(full string) string {
	if full == "" {
		return ""
	}
	pkgEnd := strings.LastIndex(full, "/")
	rest := full[pkgEnd+1:]
	dot := strings.Index(rest, ".")
	if dot < 0 {
		return ""
	}
	pkg, name := rest[:dot], rest[dot+1:]
	if (pkg == "agent" && strings.HasSuffix(full[:pkgEnd+1+dot], "yansu-agent/agent")) || pkg == "runtime" || pkg == "testing" {
		return ""
	}
	for {
		i := strings.LastIndex(name, ".func")
		if i < 0 {
			break
		}
		name = name[:i]
	}
	return name
}
