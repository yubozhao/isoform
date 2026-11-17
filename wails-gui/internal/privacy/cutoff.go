package privacy

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CutoffFile is the saved forget-before time.
const CutoffFile = "forget-before"

// Cutoff is the forget-before time Forget everything stamps when it starts:
// nothing captured before it may be saved again by a derived build. A build
// takes a Start token when it begins, and Allows(token) refuses the write of
// any build that began before the cutoff, so a build already running when the
// job started can't write old data back after its store was forgotten.
type Cutoff struct {
	mu   sync.Mutex
	at   time.Time
	path string
}

// NewCutoff returns a cutoff saved as CutoffFile in dir ("" keeps it in memory).
func NewCutoff(dir string) *Cutoff {
	c := &Cutoff{}
	if dir != "" {
		c.path = filepath.Join(dir, CutoffFile)
		if raw, err := os.ReadFile(c.path); err == nil {
			if ms, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil {
				c.at = time.UnixMilli(ms)
			}
		}
	}
	return c
}

// Set moves the cutoff to at; it never moves back.
func (c *Cutoff) Set(at time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !at.After(c.at) {
		return nil
	}
	c.at = at
	if c.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(c.path+".tmp", []byte(strconv.FormatInt(at.UnixMilli(), 10)), 0o600); err != nil {
		return err
	}
	return os.Rename(c.path+".tmp", c.path)
}

// At is the cutoff, zero when none was ever set.
func (c *Cutoff) At() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

// Start is the token a build takes when it begins.
func (c *Cutoff) Start() time.Time { return time.Now() }

// Allows reports whether a build that began at start may save its result.
func (c *Cutoff) Allows(start time.Time) bool {
	at := c.At()
	return at.IsZero() || !start.Before(at)
}

// CapturedAllowed reports whether an input captured at captured may be used.
func (c *Cutoff) CapturedAllowed(captured time.Time) bool { return c.Allows(captured) }
