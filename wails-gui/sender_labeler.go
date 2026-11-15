package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"wails-gui/internal/identity"
	"wails-gui/storage"
)

// senderPreviewMaxRunes caps the preview body stored per chat message.
// 100 runes is plenty for "did the last message look angry/important?"
// previews on the Who you know page while keeping the rolling-window table
// well-bounded even after months of activity.
const senderPreviewMaxRunes = 100

// senderLabeler resolves chat senders to user-facing prompt labels and
// accumulates a per-session ChatSenderStats tally. Construction is cheap;
// labels are cached per (app, raw sender) tuple for the lifetime of the
// labeler so a single conversation with many messages from the same sender
// only hits the resolver once.
//
// The labeler is safe to use when the underlying resolver is nil — every
// sender then degrades to the "unknown" label and counts toward
// stats.UnknownMsgCount. This keeps tests that construct an ActivityTriager
// without a store working.
type senderLabeler struct {
	resolver *identity.Resolver
	store    *storage.Store
	cache    map[string]senderLabelCacheEntry
	stats    ChatSenderStats
	// tsCounter advances by 1ms per Label call so the (principal_id,
	// seen_at) primary key on principal_recent_messages can store one
	// preview per message even when many messages render inside the
	// same wall-clock millisecond. Reset to time.Now() on first use.
	tsCounter int64
}

type senderLabelCacheEntry struct {
	principalID string
	label       string
	kind        identity.SubjectKind
}

func newSenderLabeler(store *storage.Store) *senderLabeler {
	var r *identity.Resolver
	if store != nil {
		r = identity.NewResolver(store, true) // auto-bucket so the onboarding UI can later promote anonymous senders
	}
	return &senderLabeler{
		resolver: r,
		store:    store,
		cache:    map[string]senderLabelCacheEntry{},
	}
}

// Label returns the prompt-facing prefix string for a chat sender and
// increments the stats accordingly. rawSender is the verbatim Sender from
// the parser; an empty rawSender returns "—" without touching the resolver
// (and counts toward UnknownMsgCount).
//
// preview is the raw message body associated with this sighting — when
// non-empty (and the sender resolves to a known principal) it is truncated
// to senderPreviewMaxRunes and persisted via store.BumpPrincipalSighting so
// the Who you know page can render "what did X recently say" without re-scanning
// the parsed-content store. Storage writes are best-effort: a transient DB
// error is logged but never fails the render path.
// snapshotTs is the millisecond timestamp of the snapshot that produced
// this message — used so the People → Activity drill-through can land on
// the actual frame where the person appeared. Pass 0 from contexts that
// don't have a meaningful snapshot timestamp (debug calls, tests); the
// labeler will fall back to its monotonic wall clock for sighting
// rotation only — session_sightings won't get a sensible first/last
// when the caller passes 0.
func (l *senderLabeler) Label(ctx context.Context, app, rawSender, preview, sessionID string, snapshotTs int64) string {
	if l == nil {
		return "—"
	}
	rawSender = strings.TrimSpace(rawSender)
	if rawSender == "" {
		l.stats.UnknownMsgCount++
		return "—"
	}

	// Normalize the sender BEFORE cache key + resolution so chat parsers
	// that leak timestamps / role badges / message body fragments into
	// the sender field (Discord cluster headers are the recurring
	// offender) don't fan out into one Anonymous principal per
	// (name, timestamp) tuple. The label rendered back into the prompt
	// uses the normalized form too — keeping it noisy would defeat the
	// purpose of the normalizer.
	normalizedSender := identity.NormalizeSenderName(rawSender)
	if normalizedSender == "" {
		normalizedSender = rawSender
	}
	key := strings.ToLower(app) + "|" + strings.ToLower(normalizedSender)

	// A labeler can outlive a merge. Re-resolve hidden cached owners through
	// the transferred handle rather than attributing new work to a source.
	if cached, ok := l.cache[key]; ok && cached.principalID != "" && l.store != nil {
		if _, err := l.store.GetPrincipal(ctx, cached.principalID); err != nil {
			delete(l.cache, key)
		}
	}

	var entry senderLabelCacheEntry
	if cached, ok := l.cache[key]; ok {
		entry = cached
	} else {
		entry.kind = identity.SubjectUnknown
		entry.label = fmt.Sprintf("[Unknown: %s]", normalizedSender)
		if l.resolver != nil {
			resolve := l.resolver.ResolveSender
			if identity.SenderIsAutomated(rawSender) {
				// An app or bot is attributed only when the person already
				// named it; it never becomes a new person.
				resolve = l.resolver.ResolveExistingSender
			}
			ref, err := resolve(ctx, app, "", normalizedSender)
			if err == nil {
				entry.kind = ref.Kind
				entry.label = formatSenderLabel(ref, normalizedSender)
				entry.principalID = ref.PrincipalID
			}
		}
		l.cache[key] = entry
	}

	l.bumpStats(entry.kind)
	l.recordSighting(ctx, entry, app, preview, sessionID, snapshotTs)
	return entry.label
}

// recordSighting writes the per-message sighting to storage. No-op when the
// sender did not resolve to a real principal (Unknown / nil store), so the
// Who you know page never accumulates rows for unattributed noise. Errors are
// downgraded to a log line — the render path must not fail because a stats
// counter could not bump.
//
// snapshotTs is the millisecond timestamp of the snapshot that produced
// this message. It's used for first_seen/last_seen on principal_sightings
// AND principal_session_sightings so the People → Activity drill-through
// can land on the actual frame. When 0 (debug / test callers), we fall
// back to the labeler's monotonic wall clock; the resulting timestamp
// reflects when the render ran rather than when the message was visible
// on screen, which is the legacy behavior and good enough for
// non-drill-through consumers.
func (l *senderLabeler) recordSighting(ctx context.Context, entry senderLabelCacheEntry, app, preview, sessionID string, snapshotTs int64) {
	if l.store == nil || entry.principalID == "" || entry.kind == identity.SubjectUnknown {
		return
	}
	ts := snapshotTs
	if ts <= 0 {
		ts = l.nextTimestamp()
	}
	short := truncateRunesForPreview(preview, senderPreviewMaxRunes)
	afterSighting := engagementPersonSightingHook(ctx, entry.principalID, app, ts)
	if err := l.store.BumpPrincipalSighting(ctx, entry.principalID, app, sessionID, ts, short); err != nil {
		log.Printf("[sender-labeler] BumpPrincipalSighting failed for %s/%s: %v", entry.principalID, app, err)
		return
	}
	if afterSighting != nil {
		afterSighting()
	}
}

// nextTimestamp returns a monotonically-increasing millisecond timestamp so
// rapid-fire Label calls inside a single render get distinct seen_at values
// (the principal_recent_messages primary key would otherwise drop all but
// the first preview per ms). Resets to wall-clock once the labeler stays
// idle long enough that wall-clock has caught up.
func (l *senderLabeler) nextTimestamp() int64 {
	now := time.Now().UnixMilli()
	if now > l.tsCounter {
		l.tsCounter = now
	} else {
		l.tsCounter++
	}
	return l.tsCounter
}

func truncateRunesForPreview(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "…"
}

func (l *senderLabeler) bumpStats(kind identity.SubjectKind) {
	switch kind {
	case identity.SubjectSelf:
		l.stats.SelfMsgCount++
	case identity.SubjectContact:
		l.stats.ContactMsgCount++
	case identity.SubjectAnonymous:
		l.stats.AnonymousMsgCount++
	default:
		l.stats.UnknownMsgCount++
	}
}

// Stats returns the accumulated counts. Safe to call on a nil receiver.
func (l *senderLabeler) Stats() ChatSenderStats {
	if l == nil {
		return ChatSenderStats{}
	}
	return l.stats
}

func formatSenderLabel(ref identity.SubjectRef, rawSender string) string {
	switch ref.Kind {
	case identity.SubjectSelf:
		return "[YOU]"
	case identity.SubjectContact:
		name := strings.TrimSpace(ref.DisplayName)
		if name == "" {
			name = rawSender
		}
		if ref.Relationship != "" {
			return fmt.Sprintf("[Contact: %s, %s]", name, ref.Relationship)
		}
		return fmt.Sprintf("[Contact: %s]", name)
	case identity.SubjectAnonymous:
		display := strings.TrimSpace(ref.DisplayName)
		if display == "" {
			display = rawSender
		}
		return fmt.Sprintf("[Anonymous: %s]", display)
	default:
		return fmt.Sprintf("[Unknown: %s]", rawSender)
	}
}
