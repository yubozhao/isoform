package remoteaccess

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const listenerIDEnv = "ISOFORM_LISTENER_ID="

// A digest preserves argument boundaries without storing installation paths.
// The launch nonce distinguishes even a reused PID running the same command.
type listenerRecord struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
	Command   string    `json:"command"`
	ID        string    `json:"id"`
}

func newListenerRecord(cmd *exec.Cmd) listenerRecord {
	record := listenerRecord{ID: rand.Text(), StartedAt: time.Now().UTC(), Command: commandDigest(cmd.Args)}
	cmd.Env = append(cmd.Environ(), listenerIDEnv+record.ID)
	return record
}

func commandDigest(args []string) string {
	digest := sha256.Sum256([]byte(strings.Join(args, "\x00")))
	return hex.EncodeToString(digest[:])
}

func (r listenerRecord) matches(exe string, args, env []string) bool {
	if len(args) != 4 || args[0] != exe || !filepath.IsAbs(exe) || commandDigest(args) != r.Command {
		return false
	}
	const suffix = "/Isoform.app/Contents/"
	if !strings.HasSuffix(exe, suffix+"Resources/dumbpipe-bundle/dumbpipe") && !strings.HasSuffix(exe, suffix+"MacOS/dumbpipe-bundle/dumbpipe") {
		return false
	}
	port, ok := strings.CutPrefix(args[3], "127.0.0.1:")
	n, err := strconv.Atoi(port)
	if args[1] != "listen-tcp" || args[2] != "--host" || !ok || err != nil || n < 1 || n > 65535 || port != strconv.Itoa(n) {
		return false
	}
	for _, entry := range env {
		if entry == listenerIDEnv+r.ID {
			return true
		}
	}
	return false
}

func (r listenerRecord) save(path string) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return atomicWrite(path, data, 0600)
}

func (s *Supervisor) listenerPath() string {
	return filepath.Join(s.cfg.StateDir, "dumbpipe-process.json")
}

func (s *Supervisor) reapListener() {
	data, err := os.ReadFile(s.listenerPath())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var record listenerRecord
	if err != nil {
		s.logf("could not read the previous dumbpipe process record")
	} else if json.Unmarshal(data, &record) != nil || !record.valid() {
		s.logf("invalid dumbpipe process record")
	} else if err := s.stopListener(record); err != nil {
		s.logf("could not stop the previous dumbpipe listener")
		// Keep its record until the replacement starts and writes its own.
		return
	}
	s.clearListenerRecord()
}

func (r listenerRecord) valid() bool {
	digest, err := hex.DecodeString(r.Command)
	return r.PID > 1 && r.PID != os.Getpid() && r.ID != "" && !r.StartedAt.IsZero() && err == nil && len(digest) == sha256.Size
}

func (s *Supervisor) clearListenerRecord() {
	if err := os.Remove(s.listenerPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logf("could not remove the dumbpipe process record")
	}
}
