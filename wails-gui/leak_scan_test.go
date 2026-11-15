package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// leakScanEnv names a file. When set, every memory folder read made from a
// goroutine other than a test's own is written to it: the time, the folder,
// where the goroutine was created and its frames. A goroutine left running by
// an earlier test resolves HOME at call time, so it reads (and creates
// folders in) the current test's home. Match the times against
// `go test -json` to find the test that started it.
const leakScanEnv = "ISOFORM_LEAK_SCAN"

type leakScanFiles struct {
	inner memoryFileAccess
	mu    sync.Mutex
	out   *os.File
	seen  map[string]bool
}

func (l *leakScanFiles) note(dir string) {
	buf := make([]byte, 16<<10)
	stack := string(buf[:runtime.Stack(buf, false)])
	if strings.Contains(stack, "testing.tRunner") {
		return
	}
	created := ""
	if i := strings.Index(stack, "created by "); i >= 0 {
		parts := strings.SplitN(stack[i:], "\n", 3)
		created = parts[0]
		if len(parts) > 1 {
			created += " " + strings.TrimSpace(parts[1])
		}
	}
	var frames []string
	for _, line := range strings.Split(stack, "\n") {
		if strings.HasPrefix(line, "main.") {
			frames = append(frames, strings.SplitN(line, "(", 2)[0])
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	key := dir + "|" + created
	if l.seen[key] {
		return
	}
	l.seen[key] = true
	fmt.Fprintf(l.out, "%s\t%s\t%s\t%s\n", time.Now().Format(time.RFC3339Nano), dir, created, strings.Join(frames, " < "))
}

func (l *leakScanFiles) ReadDir(name string) ([]os.DirEntry, error) {
	l.note(name)
	return l.inner.ReadDir(name)
}

func (l *leakScanFiles) ReadFile(name string) ([]byte, error) { return l.inner.ReadFile(name) }

func startLeakScan() (stop func()) {
	path := os.Getenv(leakScanEnv)
	if path == "" {
		return func() {}
	}
	out, err := os.Create(path)
	if err != nil {
		return func() {}
	}
	scan := &leakScanFiles{inner: memoryFiles, out: out, seen: map[string]bool{}}
	memoryFiles = scan
	return func() {
		scan.mu.Lock()
		defer scan.mu.Unlock()
		_ = out.Close()
	}
}
