package main

import (
	"context"
	"debug/macho"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"time"
)

// The only projection still over 100 ms after query fixes is native permission
// status. This cache contains code-owned permission rows and grant booleans,
// never captured content, people, beliefs, queries, tokens or source material.
// It is display state only: capture and permission requests use live OS checks.
type pagePermissionSnapshot struct {
	Version int                 `json:"version"`
	Build   string              `json:"build"`
	Schema  string              `json:"schema"`
	Binding string              `json:"binding"`
	Args    []string            `json:"args"`
	At      time.Time           `json:"at"`
	View    PlatformPermissions `json:"view"`
}

type pagePermissionCache struct {
	mu         sync.Mutex
	path       string
	value      *pagePermissionSnapshot
	loaded     bool
	generation uint64
	running    chan struct{}
	stopped    bool
}

func pagePermissionSnapshotPath() (string, error) {
	root, err := flavorDataDir()
	return filepath.Join(root, "page-snapshots", agencyPayloadHash([]byte("GetPlatformPermissions:[]"))+".json"), err
}

func (a *App) pageSnapshotAllowed() bool {
	return a != nil && a.store != nil && !a.IsClosedEyesModeEnabled() && (a.ctx == nil || a.ctx.Err() == nil)
}

// permissionViewChanged compares a stored view with a fresh probe. Both sides
// are normalised the same way: a nil and an empty Items list are the same
// answer. Comparing a stored clone against a raw probe read as a change on
// every refresh and emitted permissions:changed, which re-read every settings
// page and refreshed again (the storm of 2026-11-04).
func permissionViewChanged(prior, next PlatformPermissions) bool {
	return !reflect.DeepEqual(clonePlatformPermissions(prior), clonePlatformPermissions(next))
}

func clonePlatformPermissions(view PlatformPermissions) PlatformPermissions {
	view.Items = append([]PermissionItem(nil), view.Items...)
	return view
}

const pagePermissionSnapshotSchema = "platform-permissions:2"
const pageFirstReadWaitBudget = 250 * time.Millisecond

// Dirty developer rebuilds can share a version and commit. Include the binary
// stamp and build-ID-bearing headers/text prefix, read once with bounded IO.
var pagePermissionBuildIdentity = sync.OnceValue(func() string {
	commit, dirty, built := readBuildProvenance()
	stamp := fmt.Sprint(time.Now().UnixNano()) // Fail closed across restarts.
	if path, err := os.Executable(); err == nil {
		if info, err := os.Stat(path); err == nil {
			if file, err := os.Open(path); err == nil {
				defer file.Close()
				header := make([]byte, 32<<10)
				n, _ := io.ReadFull(file, header)
				identity := header[:n]
				// Go's Mach-O build ID can follow arbitrarily large headers.
				if binary, err := macho.NewFile(file); err == nil {
					if text := binary.Section("__text"); text != nil {
						prefix := make([]byte, min(text.Size, 32<<10))
						n, _ := file.ReadAt(prefix, int64(text.Offset))
						identity = append(identity, prefix[:n]...)
					}
				}
				if len(identity) > 0 {
					stamp = fmt.Sprintf("%d:%d:%s", info.Size(), info.ModTime().UnixNano(), agencyPayloadHash(identity))
				}
			}
		}
	}
	return agencyPayloadHash([]byte(fmt.Sprintf("%s:%s:%t:%s:%s:%s:%s", Version, commit, dirty, built, runtime.GOOS, runtime.GOARCH, stamp)))
})

func (s *pagePermissionSnapshot) valid(now time.Time) bool {
	return s != nil && s.Version == 2 && s.Build == pagePermissionBuildIdentity() &&
		s.Schema == pagePermissionSnapshotSchema && s.Binding == "GetPlatformPermissions" && len(s.Args) == 0 &&
		s.View.Platform == runtime.GOOS && len(s.View.Items) > 0 && !s.At.After(now) && now.Sub(s.At) < 24*time.Hour
}

// Revalidate memory too: a running app can outlive the disk admission's TTL.
func (c *pagePermissionCache) discardInvalid(now time.Time) {
	if c.value != nil && !c.value.valid(now) {
		c.value = nil
		if c.path != "" {
			_ = os.Remove(c.path)
		}
	}
}

// Native probes are injectable only at this internal seam for deterministic
// unit lifecycle tests. Production and benchmarks always pass the real probe.
func (a *App) pageNativePermissions(probe func() PlatformPermissions) PlatformPermissions {
	if !a.pageSnapshotAllowed() {
		return probe()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	view, err := a.pageNativePermissionsContext(ctx, probe)
	if err != nil {
		// Foreground reads retain the existing live fallback instead of
		// displaying an empty permissions list. The warmer calls the context
		// variant directly and never enters this uninterruptible OS fallback.
		return probe()
	}
	return view
}

func (a *App) pageNativePermissionsContext(ctx context.Context, probe func() PlatformPermissions) (PlatformPermissions, error) {
	if err := ctx.Err(); err != nil {
		return PlatformPermissions{}, err
	}
	if !a.pageSnapshotAllowed() {
		return PlatformPermissions{}, context.Canceled
	}
	c := &a.pagePermissions
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return PlatformPermissions{}, context.Canceled
	}
	if c.path == "" {
		c.path, _ = pagePermissionSnapshotPath()
	}
	if !c.loaded {
		c.loaded = true
		if path := c.path; path != "" {
			valid := false
			if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Size() <= 64<<10 && info.Mode().Perm() == 0600 {
				var saved pagePermissionSnapshot
				data, err := os.ReadFile(path)
				if err == nil && json.Unmarshal(data, &saved) == nil && saved.valid(time.Now()) {
					c.value = &saved
					valid = true
				}
			}
			if !valid {
				_ = os.Remove(path)
			}
		}
	}
	c.discardInvalid(time.Now())
	var prior *pagePermissionSnapshot
	if c.value != nil {
		copy := *c.value
		copy.View = clonePlatformPermissions(copy.View)
		prior = &copy
	}
	// Every served snapshot gets a fresh native check; coalesce overlapping
	// callers. A changed result uses the page's existing update event.
	done := c.running
	if done == nil {
		done = make(chan struct{})
		c.running = done
		generation := c.generation
		go a.refreshPagePermissions(probe, generation, done)
	}
	allowed := a.pageSnapshotAllowed()
	c.mu.Unlock()
	if prior != nil && allowed {
		return prior.View, nil
	}
	select {
	case <-ctx.Done():
		return PlatformPermissions{}, ctx.Err()
	case <-done:
	}
	c.mu.Lock()
	c.discardInvalid(time.Now())
	if c.value != nil && a.pageSnapshotAllowed() && !c.stopped {
		view := clonePlatformPermissions(c.value.View)
		c.mu.Unlock()
		return view, nil
	}
	c.mu.Unlock()
	// A privacy transition raced the cold fill. No cached value crosses it.
	return PlatformPermissions{}, context.Canceled
}

func (a *App) refreshPagePermissions(probe func() PlatformPermissions, generation uint64, done chan struct{}) {
	view := probe()
	c := &a.pagePermissions
	c.mu.Lock()
	defer func() {
		if c.running == done {
			c.running = nil
		}
		close(done)
		c.mu.Unlock()
	}()
	if c.stopped || c.generation != generation || !a.pageSnapshotAllowed() {
		return
	}
	changed := c.value != nil && permissionViewChanged(c.value.View, view)
	saved := &pagePermissionSnapshot{Version: 2, Build: pagePermissionBuildIdentity(), Schema: pagePermissionSnapshotSchema, Binding: "GetPlatformPermissions", Args: []string{}, At: time.Now(), View: clonePlatformPermissions(view)}
	c.value = saved
	if path := c.path; path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err == nil {
			if data, err := json.Marshal(saved); err == nil && len(data) <= 64<<10 {
				if file, err := os.CreateTemp(filepath.Dir(path), ".permissions-*"); err == nil {
					name := file.Name()
					_, writeErr := file.Write(data)
					closeErr := file.Close()
					if writeErr == nil && closeErr == nil {
						_ = os.Rename(name, path)
					}
					_ = os.Remove(name)
				}
			}
		}
	}
	if changed {
		// emitBrowserEvent does not clear snapshots for this display event.
		// Emit while holding the generation lock so erasure cannot be followed
		// by a late publication. Event handlers enqueue work; they do not read.
		a.emitBrowserEvent("permissions:changed")
	}
}

// Clear both disk and memory, and fence any native check already in flight.
func (a *App) clearPageSnapshots() {
	if a == nil {
		return
	}
	if bridge := a.mobileBridgeForUse(); bridge != nil {
		bridge.invalidateToday()
	}
	// Saved page snapshots go in the same step (derived_views_disk.go).
	derivedDiskDropAll()
	c := &a.pagePermissions
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.value = nil
	c.loaded = true
	if c.path == "" {
		c.path, _ = pagePermissionSnapshotPath()
	}
	if path := c.path; path != "" {
		_ = os.RemoveAll(filepath.Dir(path))
	}
}

// Save critical replies before optional joins or service teardown can spend
// the exit watchdog's budget.
func (a *App) flushAndStopPageFirstReads(optional ...<-chan struct{}) {
	// Cancellation also frees a scarce SQLite connection before the write.
	a.pageWarm.mu.Lock()
	a.pageWarm.stopped = true
	if a.pageWarm.cancel != nil {
		a.pageWarm.cancel()
	}
	a.pageWarm.mu.Unlock()
	a.flushAgentSegmentWrites(false)
	a.stopPageFirstReadsWithOptional(optional...)
}

func (a *App) stopPageFirstReads() {
	a.stopPageFirstReadsWithOptional()
}

func (a *App) stopPageFirstReadsWithOptional(optional ...<-chan struct{}) {
	a.pageWarm.mu.Lock()
	a.pageWarm.stopped = true
	if a.pageWarm.cancel != nil {
		a.pageWarm.cancel()
	}
	done := a.pageWarm.done
	a.pageWarm.mu.Unlock()
	c := &a.pagePermissions
	c.mu.Lock()
	c.stopped = true
	c.generation++
	refresh := c.running
	c.value = nil
	c.mu.Unlock()
	// One shared budget bounds the entire join. Native OS probes cannot be
	// interrupted by Go; stopped/generation fence their eventual publication.
	ctx, cancel := context.WithTimeout(context.Background(), pageFirstReadWaitBudget)
	defer cancel()
	for _, pending := range append([]<-chan struct{}{done, refresh}, optional...) {
		if pending != nil {
			select {
			case <-pending:
			case <-ctx.Done():
				return
			}
		}
	}
}
