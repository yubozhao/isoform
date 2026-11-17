package main

import (
	"context"
	"log"
	"sort"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

const engagementDigestLimit = 5

type engagementDigestItem struct {
	Row     storage.EngagementMomentRow
	Moment  engagement.Moment
	Verdict engagement.Verdict
	Text    engagementText
}

// handleDigest folds the best due passive moments into one chat delivery.
// Each original moment still consumes its existing per-kind allowance and
// keeps its own dedupe key, receipts and retry state.
func (s *engagementService) handleDigest(ctx context.Context, rows []storage.EngagementMomentRow, now time.Time) {
	if len(rows) == 0 || s.deliver.Digest == nil {
		return
	}
	for i := range rows {
		spec, _ := engagement.Spec(engagement.Kind(rows[i].Kind))
		fatigue := s.fatigue(ctx, spec.Kind)
		s.mu.Lock()
		rows[i].Score = engagement.Score(momentFromRow(rows[i]), spec, s.prefs, fatigue, now)
		s.mu.Unlock()
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	s.mu.Lock()
	reserved := *s.state
	reserved.SentByKindToday = make(map[engagement.Kind]int, len(s.state.SentByKindToday))
	for kind, count := range s.state.SentByKindToday {
		reserved.SentByKindToday[kind] = count
	}
	reserved.LastSentByKey = copyTimeStringMap(s.state.LastSentByKey)
	s.mu.Unlock()
	var items []engagementDigestItem
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		m, allowed := sanitizeEngagementMoment(momentFromRow(row))
		if s.sourceAllowsRead != nil {
			allowed = allowed && s.sourceAllowsRead(ctx, m)
		}
		if !allowed {
			_ = s.store.SetEngagementMomentParams(ctx, row.ID, "{}")
			_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateSuppressed, "sensitive source", storage.EngagementMomentRow{})
			continue
		}
		if params := momentParamsJSON(m, engagementReportedInChat(row)); params != row.Params {
			if err := s.store.SetEngagementMomentParams(ctx, row.ID, params); err != nil {
				continue
			}
			row.Params = params
		}
		s.mu.Lock()
		_, unrecorded := s.unrecorded[row.ID]
		// Pacing reads these maps after the lock is released while Settings and
		// the phone write them: pace on a private copy.
		prefs := s.prefsCopyLocked()
		if s.paused[m.Kind] {
			prefs.Enabled[m.Kind] = false
		}
		s.mu.Unlock()
		if len(m.Channels) > 0 || unrecorded {
			s.handle(ctx, row, now)
			continue
		}
		spec, _ := engagement.Spec(m.Kind)
		verdict := engagement.Pace(m, spec, prefs, &reserved, now)
		switch verdict.Mode {
		case engagement.ModeSuppress:
			s.mu.Lock()
			reservedCap := verdict.Reason == "daily cap" && s.state.SentByKindToday[m.Kind] < spec.DailyCap
			s.mu.Unlock()
			if reservedCap {
				// A candidate in this bundle only reserves the allowance.
				// Do not discard others before that delivery has succeeded.
				_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, "digest allowance", storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: row.Score, HoldUntil: now.Add(engagementRetryDelay).UnixMilli()})
				continue
			}
			_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateSuppressed, verdict.Reason, storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: row.Score})
			continue
		case engagement.ModeDigest:
			// A late catch-up still respects today's quiet window. Pace
			// already found the next configured digest outside that window.
			if prefs.Quiet.Contains(now) {
				_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, verdict.Reason, storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: row.Score, HoldUntil: verdict.HoldUntil.UnixMilli()})
				continue
			}
			verdict.Mode = engagement.ModeInterrupt
		case engagement.ModeHold:
			_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, verdict.Reason, storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: row.Score, HoldUntil: verdict.HoldUntil.UnixMilli()})
			continue
		default:
			s.handle(ctx, row, now)
			continue
		}
		if len(items) == engagementDigestLimit {
			next := prefs.NextDigest(now)
			if !next.After(now) {
				next = now.Add(engagementTickInterval)
			}
			if prefs.Quiet.Contains(next) {
				next = prefs.Quiet.EndAfter(next)
			}
			_ = s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateScheduled, "digest remainder", storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: row.Score, HoldUntil: next.UnixMilli()})
			continue
		}
		items = append(items, engagementDigestItem{Row: row, Moment: m, Verdict: verdict, Text: s.render(spec, m)})
		reserved.Record(m, verdict, now)
	}
	if len(items) == 0 {
		return
	}
	channel, err := s.deliver.Digest(ctx, items)
	if err != nil || channel == engagement.ChannelNone || channel == "" {
		for _, item := range items {
			_ = s.store.SetEngagementMomentState(ctx, item.Row.ID, storage.EngagementStateScheduled, "retry", storage.EngagementMomentRow{UpdatedAt: now.UnixMilli(), Score: item.Row.Score, HoldUntil: now.Add(engagementRetryDelay).UnixMilli()})
		}
		return
	}
	for _, item := range items {
		if err := s.store.SetEngagementMomentState(ctx, item.Row.ID, storage.EngagementStateSent, "digest", storage.EngagementMomentRow{
			Score: item.Row.Score, Channel: string(channel), Level: string(item.Verdict.Interruption), SentAt: now.UnixMilli(),
		}); err != nil {
			s.noteUnrecorded(item.Row.ID, engagementUnrecorded{
				channels: string(channel), paced: true, sentAt: now.UnixMilli(),
				level: string(item.Verdict.Interruption), score: item.Row.Score,
			})
		}
		s.mu.Lock()
		s.state.Record(item.Moment, item.Verdict, now)
		s.mu.Unlock()
		if err := s.autoPause(ctx, item.Moment.Kind); err != nil {
			log.Printf("[engagement] pausing digest kind: %v", err)
		}
	}
}

func copyTimeStringMap(in map[string]time.Time) map[string]time.Time {
	out := make(map[string]time.Time, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
