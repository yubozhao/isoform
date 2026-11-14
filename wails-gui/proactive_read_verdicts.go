package main

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"wails-gui/internal/engagement"
)

// A generated card's voice gate (proactiveVoiceReceiptAllowsReadLive) reloads
// the card's sources and checks them again: permissions, exclusions, source rows,
// origins and revisions. On a page of 48 cards that took 1 to 2.5 s, and the page
// projects its cards on every refresh and every background card check. The
// verdict is kept as a snapshot while nothing it depends on can have changed,
// and worked out again otherwise.
//
// Each source the gate reads has a change signal in the verdict's stamp:
//   - people, app handles, mail, calendar events (an origin replaced included),
//     reminders and the source permission switches: storage's voice source
//     version, bumped by triggers on every write to them, from any writer;
//   - capture state, for a capture card: the live capture snapshot itself;
//   - the user's chat words, for the kinds that quote them: the main chat's
//     stream item version;
//   - any withdrawal in Isoform, Closed Eyes, sensitive apps and observation
//     pauses: the privacy stamp and the brief privacy print;
//   - memory, topics, briefs, handoffs and the other brief inputs: the
//     read-cache epoch, the memory revision, the forget markers and the events
//     in proactiveReadEvents.
//
// proactiveReadVerdictMaxAge bounds anything else (a purpose snapshot, a
// memory note edited outside Isoform before its watcher fires). A verdict
// reached while its context ended, or while its stamp moved, is not kept, and a
// stamp that cannot be read means no cache at all.
const proactiveReadVerdictMaxAge = 2 * time.Minute

type proactiveReadVerdictKey struct {
	id, kind, presentation string
	status                 bool
}

type proactiveReadVerdict struct {
	allowed bool
	stamp   string
	at      time.Time
}

type proactiveReadVerdicts struct {
	mu       sync.Mutex
	verdicts map[proactiveReadVerdictKey]proactiveReadVerdict
}

// proactiveReadVerdictsByApp keeps the verdicts per app, so test apps never
// share them.
var proactiveReadVerdictsByApp sync.Map

func (a *App) proactiveReadVerdictCache() *proactiveReadVerdicts {
	cache, _ := proactiveReadVerdictsByApp.LoadOrStore(a, &proactiveReadVerdicts{})
	return cache.(*proactiveReadVerdicts)
}

// proactiveReadEvents drop every verdict: app events for inputs the other
// signals do not name.
var proactiveReadEvents = map[string]bool{
	"permissions:changed": true, "brief:stored": true, "handoff:created": true, "handoff:updated": true,
	"background-listening:changed": true,
}

var proactiveReadEventGeneration atomic.Uint64

// noteProactiveReadEvent is called for every app event (emitBrowserEvent).
func noteProactiveReadEvent(eventName string) {
	if proactiveReadEvents[eventName] {
		proactiveReadEventGeneration.Add(1)
	}
}

// proactiveVoiceQuotesChat are the kinds whose source holds the user's own
// chat words (proactiveVoiceChatWords).
var proactiveVoiceQuotesChat = map[string]bool{
	string(engagement.KindWaitingOnReply): true, string(engagement.KindMeetingPrep): true, string(engagement.KindCommitmentSlipping): true,
}

// proactiveReadVerdictStamp names what a verdict for kind depends on. ok is
// false when a part of it cannot be read; the gate then runs live.
func (a *App) proactiveReadVerdictStamp(ctx context.Context, kind string) (string, bool) {
	sources, err := a.store.VoiceSourceVersion(ctx)
	if err != nil {
		return "", false
	}
	stamp := a.savedCopyStamp(ctx) + "|" + a.briefPrivacyPrint() +
		"|" + strconv.FormatUint(mobileBriefReadEpoch.Load(), 36) +
		"|" + strconv.FormatUint(a.memoryExternalReads.revision.Load(), 36) +
		"|" + strconv.FormatUint(memoryTombstoneGeneration.Load(), 36) +
		"|" + strconv.FormatUint(proactiveReadEventGeneration.Load(), 36) +
		"|" + strconv.FormatInt(sources, 36)
	if kind == string(engagement.KindCapturePaused) {
		raw, err := json.Marshal(proactiveVoiceCaptureSnapshots(a))
		if err != nil {
			return "", false
		}
		stamp += "|" + string(raw)
	}
	if proactiveVoiceQuotesChat[kind] {
		main, err := a.store.LoadMainSession(ctx)
		if err != nil || main == nil {
			// No main chat yet: no chat words; one made later changes this part.
			stamp += "|no-main"
		} else {
			version, err := a.store.StreamItemVersion(ctx, main.LocalID)
			if err != nil {
				return "", false
			}
			stamp += "|" + main.LocalID + ":" + strconv.FormatInt(version, 36) + ":" + strconv.FormatBool(main.Hidden)
		}
	}
	return stamp, true
}

func (a *App) proactiveReadVerdict(ctx context.Context, key proactiveReadVerdictKey, live func() bool) bool {
	stamp, ok := a.proactiveReadVerdictStamp(ctx, key.kind)
	if !ok {
		return live()
	}
	cache := a.proactiveReadVerdictCache()
	now := time.Now()
	cache.mu.Lock()
	if v, found := cache.verdicts[key]; found && v.stamp == stamp && now.Sub(v.at) < proactiveReadVerdictMaxAge {
		cache.mu.Unlock()
		return v.allowed
	}
	cache.mu.Unlock()
	allowed := live()
	if ctx.Err() != nil {
		return allowed
	}
	if after, ok := a.proactiveReadVerdictStamp(ctx, key.kind); !ok || after != stamp {
		// Something it depends on changed while it ran: not kept.
		return allowed
	}
	cache.mu.Lock()
	if cache.verdicts == nil {
		cache.verdicts = map[proactiveReadVerdictKey]proactiveReadVerdict{}
	}
	for k, v := range cache.verdicts {
		if now.Sub(v.at) >= proactiveReadVerdictMaxAge {
			delete(cache.verdicts, k)
		}
	}
	cache.verdicts[key] = proactiveReadVerdict{allowed: allowed, stamp: stamp, at: now}
	cache.mu.Unlock()
	return allowed
}
