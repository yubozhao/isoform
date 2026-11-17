package main

import (
	"context"
	"log"
	"strings"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

// Prep follows you once (notify v1.0.0 rule 5, B8): prep that rang on the
// computer, still unopened when the person leaves it before the meeting
// starts, goes to the phone once, without a sound. Never the other way, and
// never twice: a prep that reached the phone is never sent again.

// prepFollowWindow is how far back a prep that rang on the computer is
// still worth following: prep rings 15 minutes before its meeting.
const prepFollowWindow = 2 * time.Hour

// followPrepToPhone runs after each delivery pass.
func (s *engagementService) followPrepToPhone(ctx context.Context, now time.Time) {
	if s.presence == nil || s.deliver.Push == nil {
		return
	}
	p := s.presence(now)
	if engagement.AtComputer(p) || !engagement.PhoneReachable(p) {
		return
	}
	rows, err := s.store.SentEngagementMomentsSince(ctx, now.Add(-prepFollowWindow), 0)
	if err != nil {
		return
	}
	spec, _ := engagement.Spec(engagement.KindMeetingPrep)
	for _, row := range rows {
		channels := strings.Split(row.Channel, "+")
		if row.Kind != string(engagement.KindMeetingPrep) || !engagementChannelIn(channels, engagement.ChannelDesktop) ||
			engagementChannelIn(channels, engagement.ChannelPush) {
			continue
		}
		// The meeting started: prep is no use any more.
		if row.ExpiresAt > 0 && row.ExpiresAt <= now.UnixMilli() {
			continue
		}
		if s.momentOpened(ctx, row.ID) {
			continue
		}
		// A forgotten source, a channel the person chose, or a source that may
		// no longer be read: it stays where it rang.
		m, allowed := sanitizeEngagementMoment(momentFromRow(row))
		if row.Params == "{}" || len(m.Channels) > 0 {
			continue
		}
		if s.sourceAllowsRead != nil {
			allowed = allowed && s.sourceAllowsRead(ctx, m)
		}
		if !allowed {
			continue
		}
		verdict := engagement.Verdict{Mode: engagement.ModeInterrupt, Interruption: engagement.InterruptionPassive, Reason: "prep follows"}
		if err := s.deliver.Push(ctx, row, m, verdict, s.render(spec, m)); err != nil {
			log.Printf("[engagement] prep did not follow to the phone: %v", err)
			continue
		}
		log.Printf("[engagement] route kind=%s channel=push reason=prep_follows", row.Kind)
		if err := s.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateSent, row.Reason,
			storage.EngagementMomentRow{Channel: row.Channel + "+" + string(engagement.ChannelPush), UpdatedAt: now.UnixMilli()}); err != nil {
			log.Printf("[engagement] recording prep that followed: %v", err)
		}
	}
}

// momentOpened reports whether any device opened or answered the moment.
func (s *engagementService) momentOpened(ctx context.Context, id string) bool {
	receipts, err := s.store.ListEngagementReceipts(ctx, id)
	if err != nil {
		return true // unknown: never risk a second ring
	}
	for _, receipt := range receipts {
		if receipt.State == storage.EngagementReceiptOpened || receipt.State == storage.EngagementReceiptActed {
			return true
		}
	}
	return s.noticeRead != nil && s.noticeRead(id)
}

func engagementChannelIn(channels []string, want engagement.Channel) bool {
	for _, channel := range channels {
		if channel == string(want) {
			return true
		}
	}
	return false
}
