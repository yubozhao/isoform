package remoteaccess

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "fake-relay":
		runFakeRelay()
		os.Exit(0)
	case "fake-dumbpipe":
		runFakeDumbpipe()
		os.Exit(0)
	default:
		os.Exit(m.Run())
	}
}

func runFakeRelay() {
	stateDir := os.Getenv("ISOFORM_RELAY_STATE_DIR")
	if _, err := os.Stat(filepath.Join(stateDir, "crash-once")); err == nil {
		_ = os.Remove(filepath.Join(stateDir, "crash-once"))
		os.Exit(3)
	}
	variables := map[string]string{}
	for _, pair := range os.Environ() {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			variables[parts[0]] = parts[1]
		}
	}
	data, _ := json.Marshal(variables)
	_ = os.WriteFile(filepath.Join(stateDir, "env-dump.json"), data, 0600)
	listener, err := net.Listen("tcp", os.Getenv("ISOFORM_RELAY_ADDR"))
	if err != nil {
		os.Exit(4)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			w.WriteHeader(http.StatusOK)
		}
	})}
	_ = server.Serve(listener)
}

func runFakeDumbpipe() {
	home := os.Getenv("HOME")
	env := map[string]string{}
	for _, pair := range os.Environ() {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}
	encoded, _ := json.Marshal(map[string]any{"env": env, "args": os.Args})
	_ = os.WriteFile(filepath.Join(home, "dumbpipe-dump.json"), encoded, 0600)
	_ = os.WriteFile(filepath.Join(home, "dumbpipe-pid"), []byte(strconv.Itoa(os.Getpid())), 0600)
	if env["FAKE_CLOSE_STDERR_KEEP_STDOUT"] == "1" {
		_ = os.Stderr.Close()
		time.Sleep(100 * time.Millisecond)
		for {
			_, _ = fmt.Fprintln(os.Stdout, "stdout after stderr close")
			time.Sleep(10 * time.Millisecond)
		}
	}
	hash := sha256.Sum256([]byte(env["IROH_SECRET"]))
	suffix := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash[:]))
	_, _ = fmt.Fprintln(os.Stderr, "\x1b[36mnoise\x1b[0m")
	_, _ = fmt.Fprintf(os.Stderr, "dumbpipe connect-tcp endpoint%s\n", suffix)
	for {
		time.Sleep(time.Hour)
	}
}

type fakeInvocation struct {
	Env  map[string]string `json:"env"`
	Args []string          `json:"args"`
}

func helpers(t *testing.T, stateDir string) (string, string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	relay := filepath.Join(stateDir, "fake-relay")
	dumbpipe := filepath.Join(stateDir, "fake-dumbpipe")
	if err := os.Symlink(binary, relay); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, dumbpipe); err != nil {
		t.Fatal(err)
	}
	return relay, dumbpipe
}

func newTestSupervisor(t *testing.T, stateDir string, backoff func(int) time.Duration) *Supervisor {
	t.Helper()
	relay, dumbpipe := helpers(t, stateDir)
	t.Setenv("HOME", stateDir)
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("ISOFORM_RELAY_APNS_KEY", "must-not-be-inherited")
	t.Setenv("ISOFORM_RELAY_APNS_KEY_ID", "must-not-be-inherited")
	t.Setenv("YANSU_RELAY_ADDR", "127.0.0.1:9999")
	t.Setenv("YANSU_RELAY_STATE_DIR", "must-not-be-inherited")
	t.Setenv("YANSU_RELAY_TRUSTED_PROXIES", "must-not-be-inherited")
	t.Setenv("YANSU_RELAY_APNS_KEY", "must-not-be-inherited")
	t.Setenv("YANSU_RELAY_APNS_KEY_ID", "must-not-be-inherited")
	return New(Config{RelayBin: relay, DumbpipeBin: dumbpipe, StateDir: stateDir, Backoff: backoff})
}

func waitReady(t *testing.T, s *Supervisor) Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	snap, err := s.WaitReady(ctx)
	if err != nil {
		t.Fatalf("WaitReady: %v (snapshot %+v)", err, snap)
	}
	return snap
}

func TestOpenCappedLogRotatesOversizedFile(t *testing.T) {
	for _, test := range []struct {
		name string
		size int64
	}{
		{name: "over-cap", size: maxChildLogBytes + 1},
		{name: "at-cap", size: maxChildLogBytes},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "relay.log")
			original := bytes.Repeat([]byte("x"), int(test.size))
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+".1", []byte("old backup"), 0600); err != nil {
				t.Fatal(err)
			}

			logFile, _, err := openCappedLog(path)
			if err != nil {
				t.Fatal(err)
			}
			defer logFile.Close()

			rotated, err := os.ReadFile(path + ".1")
			if err != nil || !bytes.Equal(rotated, original) {
				t.Fatalf("rotated log does not contain the previous log: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Size() != 0 {
				t.Fatalf("new log size = %v, %v; want 0", info, err)
			}
		})
	}
}

type failingLogWriter struct{}

func (failingLogWriter) Write([]byte) (int, error) {
	return 0, errors.New("disk full")
}

func TestCappedWriterSwallowsLogErrors(t *testing.T) {
	writer := &cappedWriter{writer: failingLogWriter{}, remaining: 10}
	data := []byte("sidecar output")
	n, err := writer.Write(data)
	if n != len(data) || err != nil {
		t.Fatalf("Write() = %d, %v; want %d, nil", n, err, len(data))
	}
	if writer.remaining != 0 {
		t.Fatalf("remaining bytes = %d, want 0 after write error", writer.remaining)
	}
	n, err = writer.Write(data)
	if n != len(data) || err != nil {
		t.Fatalf("Write() after disk error = %d, %v; want %d, nil", n, err, len(data))
	}
}

func TestStartChildKeepsStdoutOpenAfterStderrEOF(t *testing.T) {
	dir := t.TempDir()
	s := newTestSupervisor(t, dir, nil)
	cmd := exec.Command(s.cfg.DumbpipeBin)
	cmd.Env = append(os.Environ(), "FAKE_CLOSE_STDERR_KEEP_STDOUT=1")
	outputDone, logFile, err := s.startChild("dumbpipe", cmd)
	if err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		<-outputDone
		_ = logFile.Close()
		close(waitDone)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
			t.Error("child did not stop after cleanup")
		}
	})

	select {
	case <-outputDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stderr scanner did not finish after the child closed stderr")
	}
	logPath := filepath.Join(dir, "dumbpipe.log")
	deadline := time.Now().Add(2 * time.Second)
	for {
		log, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(log), "stdout after stderr close") >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stdout was not logged after stderr closed: %q", log)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("child exited after closing stderr: %v", err)
	}
	select {
	case <-waitDone:
		t.Fatal("child stopped while continuing to write stdout")
	default:
	}
}

func TestCappedDumbpipeLogStillParsesTicketAndKeepsChildRunning(t *testing.T) {
	dir := t.TempDir()
	nearLimit := bytes.Repeat([]byte("x"), int(maxChildLogBytes-5))
	if err := os.WriteFile(filepath.Join(dir, "dumbpipe.log"), nearLimit, 0600); err != nil {
		t.Fatal(err)
	}
	s := newTestSupervisor(t, dir, nil)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)

	snapshot := waitReady(t, s)
	if snapshot.Ticket == "" {
		t.Fatal("ticket was not parsed after the log reached its cap")
	}
	info, err := os.Stat(filepath.Join(dir, "dumbpipe.log"))
	if err != nil || info.Size() != maxChildLogBytes {
		t.Fatalf("dumbpipe log size = %v, %v; want %d", info, err, maxChildLogBytes)
	}
	dumbpipeData, err := os.ReadFile(filepath.Join(dir, "dumbpipe-dump.json"))
	if err != nil {
		t.Fatal(err)
	}
	if pid := readDumbpipePID(t, dumbpipeData); syscall.Kill(pid, 0) != nil {
		t.Fatalf("dumbpipe exited after the log cap was reached: pid %d", pid)
	}
}

func TestReadyPersistenceEnvironmentResetAndStop(t *testing.T) {
	dir := t.TempDir()
	s := newTestSupervisor(t, dir, nil)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := waitReady(t, s)
	if first.State != StateReady || !strings.HasPrefix(first.RelayURL, "http://127.0.0.1:") || !ticketPattern.MatchString(first.Ticket) {
		t.Fatalf("ready snapshot = %+v", first)
	}
	keyPath := filepath.Join(dir, "node-key.json")
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("node key mode = %v, %v", info, err)
	}

	var dumbpipe fakeInvocation
	dumbpipeData, err := os.ReadFile(filepath.Join(dir, "dumbpipe-dump.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(dumbpipeData, &dumbpipe); err != nil {
		t.Fatal(err)
	}
	if dumbpipe.Env["IROH_SECRET"] == "" || len(dumbpipe.Env["IROH_SECRET"]) != 64 || dumbpipe.Env["NO_COLOR"] != "1" {
		t.Fatalf("dumbpipe env = %+v", dumbpipe.Env)
	}
	if _, ok := dumbpipe.Env["RUST_LOG"]; ok {
		t.Fatalf("RUST_LOG was inherited: %+v", dumbpipe.Env)
	}
	if strings.Contains(strings.Join(dumbpipe.Args, " "), dumbpipe.Env["IROH_SECRET"]) {
		t.Fatal("node secret appeared in dumbpipe argv")
	}
	if got := dumbpipe.Args[1:]; len(got) != 3 || got[0] != "listen-tcp" || got[1] != "--host" {
		t.Fatalf("dumbpipe args = %v", dumbpipe.Args)
	}

	var relayEnv map[string]string
	relayData, err := os.ReadFile(filepath.Join(dir, "relay", "env-dump.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(relayData, &relayEnv); err != nil {
		t.Fatal(err)
	}
	if relayEnv["ISOFORM_RELAY_TRUSTED_PROXIES"] != "none" || relayEnv["ISOFORM_RELAY_ADDR"] == "" {
		t.Fatalf("relay env = %+v", relayEnv)
	}
	for key := range relayEnv {
		if strings.HasPrefix(key, "ISOFORM_RELAY_APNS_") || strings.HasPrefix(key, "YANSU_RELAY_APNS_") {
			t.Fatalf("APNs environment inherited: %s", key)
		}
		if strings.HasPrefix(key, "YANSU_RELAY_") {
			t.Fatalf("legacy relay environment inherited: %s", key)
		}
	}
	log, err := os.ReadFile(filepath.Join(dir, "dumbpipe.log"))
	if err != nil || strings.Contains(string(log), dumbpipe.Env["IROH_SECRET"]) {
		t.Fatalf("secret leaked to child log: %v", err)
	}

	pid := readDumbpipePID(t, dumbpipeData)
	s.Stop()
	s.Stop()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("dumbpipe process %d still exists: %v", pid, err)
	}

	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	again := waitReady(t, s)
	if again.Ticket != first.Ticket {
		t.Fatalf("ticket changed across restart: %q != %q", again.Ticket, first.Ticket)
	}
	restartedKeyData, err := os.ReadFile(keyPath)
	if err != nil || string(restartedKeyData) != string(keyData) {
		t.Fatalf("node key was not persisted across restart: %v", err)
	}
	portData, err := os.ReadFile(filepath.Join(dir, "relay-port"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(again.RelayURL, ":"+strings.TrimSpace(string(portData))) {
		t.Fatalf("port not reused: %q vs %q", again.RelayURL, portData)
	}
	s.Stop()

	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	beforeReset, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	reset, resetErr := s.Reset(ctx)
	cancel()
	if resetErr != nil {
		t.Fatal(resetErr)
	}
	afterReset, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeReset) == string(afterReset) || reset.Ticket == again.Ticket {
		t.Fatal("Reset did not rotate the key and ticket")
	}
	s.Stop()
}

func readDumbpipePID(t *testing.T, data []byte) int {
	t.Helper()
	var invocation fakeInvocation
	if err := json.Unmarshal(data, &invocation); err != nil {
		t.Fatal(err)
	}
	pidText, err := os.ReadFile(filepath.Join(invocation.Env["HOME"], "dumbpipe-pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidText)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestRestartBackoffAndPersistentPort(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "relay"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "relay", "crash-once"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	s := newTestSupervisor(t, dir, func(int) time.Duration { return time.Millisecond })
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := waitReady(t, s)
	if snap.Restarts == 0 {
		t.Fatalf("relay restart not counted: %+v", snap)
	}
	portData, err := os.ReadFile(filepath.Join(dir, "relay-port"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(portData)))
	if err != nil || port < 1 || port >= 65536 || portFree(port) {
		t.Fatalf("relay port is invalid or not in use: %q, %v", portData, err)
	}
	s.Stop()
	if !portFree(port) {
		t.Fatalf("relay port %d is still occupied after Stop", port)
	}
	if err := os.Remove(filepath.Join(dir, "fake-relay")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "fake-dumbpipe")); err != nil {
		t.Fatal(err)
	}
	restarted := newTestSupervisor(t, dir, func(int) time.Duration { return time.Millisecond })
	if err := restarted.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitReady(t, restarted)
	reusedData, err := os.ReadFile(filepath.Join(dir, "relay-port"))
	if err != nil {
		t.Fatal(err)
	}
	reusedPort, err := strconv.Atoi(strings.TrimSpace(string(reusedData)))
	if err != nil || reusedPort != port {
		t.Fatalf("relay port was not reused: before=%d after=%q error=%v", port, reusedData, err)
	}
	restarted.Stop()
}

func TestMalformedNodeKeyFailsWithoutReplacingFile(t *testing.T) {
	dir := t.TempDir()
	s := newTestSupervisor(t, dir, nil)
	keyPath := filepath.Join(dir, "node-key.json")
	original := []byte(`{"version":1,"secret":"broken"}`)
	if err := os.WriteFile(keyPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("malformed node key accepted")
	}
	if snap := s.Snapshot(); snap.State != StateFailed || snap.LastError == "" {
		t.Fatalf("failed snapshot = %+v", snap)
	}
	after, err := os.ReadFile(keyPath)
	if err != nil || string(after) != string(original) {
		t.Fatalf("malformed key changed: %q, %v", after, err)
	}
}

func TestSupervisorRestartsAfterChildStartFailure(t *testing.T) {
	dir := t.TempDir()
	s := newTestSupervisor(t, dir, nil)
	relayBin := s.cfg.RelayBin
	if err := os.Remove(relayBin); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(relayBin, []byte("#!/missing-for-supervisor-restart\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start with a valid executable path: %v", err)
	}
	t.Cleanup(s.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	failedSnapshot, waitErr := s.WaitReady(ctx)
	cancel()
	if waitErr == nil || failedSnapshot.State != StateFailed {
		t.Fatalf("failed startup = %+v, %v; want StateFailed", failedSnapshot, waitErr)
	}

	s.mu.Lock()
	failedCtx := s.ctx
	started, stopped := s.started, s.stopped
	s.mu.Unlock()
	if !started || stopped {
		t.Fatalf("lifecycle after startup failure: started=%t stopped=%t", started, stopped)
	}
	select {
	case <-failedCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("failed supervisor context was not cancelled")
	}

	stopWithinTimeout := func() {
		t.Helper()
		done := make(chan struct{})
		go func() {
			s.Stop()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("Stop did not finish promptly")
		}
	}
	stopWithinTimeout()

	s.mu.Lock()
	started, stopped = s.started, s.stopped
	stoppedSnapshot := s.snapshot
	s.mu.Unlock()
	if !started || !stopped || stoppedSnapshot.State != StateOff {
		t.Fatalf("lifecycle after Stop: started=%t stopped=%t snapshot=%+v", started, stopped, stoppedSnapshot)
	}

	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(relayBin); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, relayBin); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start after Stop: %v", err)
	}

	s.mu.Lock()
	restartedCtx := s.ctx
	started, stopped = s.started, s.stopped
	restartedSnapshot := s.snapshot
	s.mu.Unlock()
	if restartedCtx == failedCtx || restartedCtx.Err() != nil {
		t.Fatal("restart did not create a fresh active context")
	}
	if !started || stopped || restartedSnapshot.State == StateFailed || restartedSnapshot.LastError != "" {
		t.Fatalf("lifecycle after restart: started=%t stopped=%t snapshot=%+v", started, stopped, restartedSnapshot)
	}
	if snapshot := waitReady(t, s); snapshot.State != StateReady {
		t.Fatalf("restart did not reach ready: %+v", snapshot)
	}
	stopWithinTimeout()
}

func TestSupervisorSerializesConcurrentStopAndStart(t *testing.T) {
	dir := t.TempDir()
	s := newTestSupervisor(t, dir, nil)
	t.Cleanup(s.Stop)
	relayBin := s.cfg.RelayBin
	if err := os.Remove(relayBin); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(relayBin, []byte("#!/missing-for-supervisor-restart\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start with a valid executable path: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	failedSnapshot, waitErr := s.WaitReady(ctx)
	cancel()
	if waitErr == nil || failedSnapshot.State != StateFailed {
		t.Fatalf("failed startup = %+v, %v; want StateFailed", failedSnapshot, waitErr)
	}

	workersDone := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(workersDone)
	}()
	select {
	case <-workersDone:
	case <-time.After(3 * time.Second):
		t.Fatal("failed supervisor workers did not exit")
	}

	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(relayBin); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, relayBin); err != nil {
		t.Fatal(err)
	}

	workerRelease := make(chan struct{})
	s.wg.Add(1)
	go func() {
		<-workerRelease
		s.wg.Done()
	}()
	var releaseOnce sync.Once
	releaseWorker := func() {
		releaseOnce.Do(func() { close(workerRelease) })
	}
	restartCtx, cancelRestart := context.WithCancel(context.Background())
	t.Cleanup(func() {
		releaseWorker()
		cancelRestart()
		s.Stop()
	})

	operationsDone := make(chan error, 4)
	go func() {
		s.Stop()
		operationsDone <- nil
	}()
	stopped := false
	stopDeadline := time.NewTimer(3 * time.Second)
	defer stopDeadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !stopped {
		s.mu.Lock()
		stopped = s.stopped
		s.mu.Unlock()
		if stopped {
			break
		}
		select {
		case <-stopDeadline.C:
			t.Fatal("first Stop did not enter its wait")
		case <-ticker.C:
		}
	}

	attempted := make(chan struct{}, 3)
	for range 2 {
		go func() {
			attempted <- struct{}{}
			s.Stop()
			operationsDone <- nil
		}()
	}
	restartStopReturned := make(chan struct{})
	go func() {
		attempted <- struct{}{}
		s.Stop()
		close(restartStopReturned)
		operationsDone <- s.Start(restartCtx)
	}()
	attemptsDeadline := time.NewTimer(3 * time.Second)
	defer attemptsDeadline.Stop()
	for range 3 {
		select {
		case <-attempted:
		case <-attemptsDeadline.C:
			t.Fatal("concurrent lifecycle operations did not start")
		}
	}
	interleaved := false
	select {
	case <-restartStopReturned:
		interleaved = true
	case <-time.After(100 * time.Millisecond):
	}
	releaseWorker()

	operationsDeadline := time.NewTimer(5 * time.Second)
	defer operationsDeadline.Stop()
	for range 4 {
		select {
		case err := <-operationsDone:
			if err != nil {
				t.Fatalf("concurrent lifecycle operation: %v", err)
			}
		case <-operationsDeadline.C:
			t.Fatal("concurrent lifecycle operations did not finish")
		}
	}
	if interleaved {
		t.Fatal("Stop-Start sequence ran before the in-flight Stop completed")
	}
	if err := s.Start(restartCtx); err != nil {
		t.Fatalf("Start after concurrent lifecycle operations: %v", err)
	}
	if snapshot := waitReady(t, s); snapshot.State != StateReady {
		t.Fatalf("supervisor after concurrent lifecycle operations = %+v", snapshot)
	}

	finalStop := make(chan struct{})
	go func() {
		s.Stop()
		close(finalStop)
	}()
	select {
	case <-finalStop:
	case <-time.After(3 * time.Second):
		t.Fatal("final Stop did not finish promptly")
	}
}

func TestLocateOverrideMustBeExecutable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMOTE_BINARY", path)
	if _, err := LocateBundledBinary("REMOTE_BINARY", "bundle", "app", "dev", "path"); err == nil {
		t.Fatal("non-executable override accepted")
	}
}

// The ticket is parsed for the snapshot but never logged: not through Logf
// (the app log) and not in dumbpipe's own log file.
func TestDumbpipeTicketIsParsedButNeverLogged(t *testing.T) {
	dir := t.TempDir()
	s := newTestSupervisor(t, dir, nil)
	var mu sync.Mutex
	var logged []string
	s.cfg.Logf = func(format string, args ...any) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	snapshot := waitReady(t, s)
	if !ticketPattern.MatchString(snapshot.Ticket) {
		t.Fatalf("ticket was not parsed: %q", snapshot.Ticket)
	}
	mu.Lock()
	lines := append([]string(nil), logged...)
	mu.Unlock()
	sawRedacted := false
	for _, line := range lines {
		if strings.Contains(line, snapshot.Ticket) {
			t.Fatalf("the app log received the ticket: %q", line)
		}
		sawRedacted = sawRedacted || strings.Contains(line, redactedTicket)
	}
	if !sawRedacted {
		t.Fatalf("the ticket line was not logged in redacted form: %q", lines)
	}
	data, err := os.ReadFile(filepath.Join(dir, "dumbpipe.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), snapshot.Ticket) {
		t.Fatal("dumbpipe.log holds the ticket")
	}
}

func TestTicketRedactingWriterReplacesTickets(t *testing.T) {
	var out bytes.Buffer
	input := "connect with endpointabcdefghijklmnopqrstuvwxyz234567 now\n"
	n, err := ticketRedactingWriter{writer: &out}.Write([]byte(input))
	if err != nil || n != len(input) {
		t.Fatalf("write = %d, %v", n, err)
	}
	if out.String() != "connect with "+redactedTicket+" now\n" {
		t.Fatalf("redacted output = %q", out.String())
	}
}
