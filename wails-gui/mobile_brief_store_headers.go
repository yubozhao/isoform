package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// mobileBriefHeader is what a reader needs to pick a stored brief without
// decoding every file: Today's read and the current-brief choice look at each
// stored brief's time, status and slot, then decode only the one they show.
// Decoding all of them on every read cost 100 ms to 1.4 s on 2026-11-15 (42
// briefs, 15 MB). Headers hold no brief text.
type mobileBriefHeader struct {
	BriefID         string
	AsOf            string
	NarrativeStatus string
	Slot            string
	asOf            time.Time
	written         bool
	empty           bool
	aheadAt         time.Time
	ahead           bool
}

func (h mobileBriefHeader) held(now time.Time) bool { return h.ahead && now.Before(h.aheadAt) }

func mobileBriefHeaderOf(brief MobileBriefV1) mobileBriefHeader {
	aheadAt, ahead := mobileBriefAheadSlotTime(brief)
	return mobileBriefHeader{
		BriefID: brief.BriefID, AsOf: brief.AsOf, NarrativeStatus: brief.NarrativeStatus, Slot: brief.Slot,
		asOf: mobileBriefAsOfTime(brief), written: mobileBriefHasWrittenText(brief), empty: mobileBriefIsEmpty(brief),
		aheadAt: aheadAt, ahead: ahead,
	}
}

// mobileBriefFileStamp names one version of a brief file. Every write is an
// atomic rename, so a new version has a new inode and modification time.
type mobileBriefFileStamp struct {
	size  int64
	mtime int64
	ino   uint64
}

func mobileBriefFileStampOf(info os.FileInfo) mobileBriefFileStamp {
	stamp := mobileBriefFileStamp{size: info.Size(), mtime: info.ModTime().UnixNano()}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		stamp.ino = uint64(sys.Ino)
	}
	return stamp
}

type mobileBriefHeaderEntry struct {
	stamp  mobileBriefFileStamp
	header mobileBriefHeader
	ok     bool
}

// Headers are kept per directory: separate store instances can share one.
var mobileBriefHeaderMemo = struct {
	sync.Mutex
	dirs map[string]map[string]mobileBriefHeaderEntry
	// decodes counts brief files decoded for headers, per directory (tests).
	decodes map[string]int
}{dirs: map[string]map[string]mobileBriefHeaderEntry{}, decodes: map[string]int{}}

// headers lists the stored briefs briefs() would return, in the same order,
// as headers. A file is decoded again only when it changed on disk.
func (s *mobileBriefStore) headers() []mobileBriefHeader {
	if s.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	mobileBriefHeaderMemo.Lock()
	memo := mobileBriefHeaderMemo.dirs[s.dir]
	mobileBriefHeaderMemo.Unlock()
	next := make(map[string]mobileBriefHeaderEntry, len(entries))
	decoded := 0
	now := mobileBriefClock()
	var out []mobileBriefHeader
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		// latest.json and other records are not briefs; load rejects them too.
		if id == entry.Name() || !mobileBriefIDPattern.MatchString(id) {
			continue
		}
		info, err := os.Stat(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}
		stamp := mobileBriefFileStampOf(info)
		cached, hit := memo[id]
		if !hit || cached.stamp != stamp {
			decoded++
			brief, ok := s.load(id)
			cached = mobileBriefHeaderEntry{stamp: stamp, ok: ok}
			if ok {
				cached.header = mobileBriefHeaderOf(brief)
			}
		}
		next[id] = cached
		if cached.ok && !cached.header.held(now) {
			out = append(out, cached.header)
		}
	}
	mobileBriefHeaderMemo.Lock()
	mobileBriefHeaderMemo.dirs[s.dir] = next
	mobileBriefHeaderMemo.decodes[s.dir] += decoded
	mobileBriefHeaderMemo.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].asOf.Equal(out[j].asOf) {
			return out[i].asOf.After(out[j].asOf)
		}
		return out[i].BriefID > out[j].BriefID
	})
	return out
}

func mobileBriefHeaderDecodes(dir string) int {
	mobileBriefHeaderMemo.Lock()
	defer mobileBriefHeaderMemo.Unlock()
	return mobileBriefHeaderMemo.decodes[dir]
}
