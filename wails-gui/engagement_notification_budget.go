package main

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

// refreshNotificationBudget reconstructs today's distinct automatically sent
// notices from the same receipts used by the Settings weekly count. Pass is
// serialized, so successful sends in this pass can reserve their own slot.
func (s *engagementService) refreshNotificationBudget(ctx context.Context, now time.Time) error {
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	notices, err := s.store.EngagementNotificationsBetween(ctx, midnight, now)
	if err != nil {
		return err
	}
	s.notificationIDs = make(map[string]bool, len(notices))
	for _, notice := range notices {
		if engagementAutomaticNotification(engagement.Kind(notice.Kind)) {
			s.notificationIDs[notice.MomentID] = true
		}
	}
	return nil
}

func engagementAutomaticNotification(kind engagement.Kind) bool {
	spec, ok := engagement.Spec(kind)
	return !ok || !spec.Requested
}

func engagementHasNotificationChannel(channels string) bool {
	for _, channel := range strings.Split(channels, "+") {
		switch engagement.Channel(channel) {
		case engagement.ChannelDesktop, engagement.ChannelPush, engagement.ChannelText, engagement.ChannelEmail:
			return true
		}
	}
	return false
}

func (s *engagementService) notificationAllowed(row storage.EngagementMomentRow, m engagement.Moment, prefs engagement.Prefs) bool {
	return !engagementAutomaticNotification(m.Kind) || prefs.DailyInterruptBudget <= 0 ||
		s.notificationIDs[row.ID] || engagementHasNotificationChannel(row.Channel) || len(s.notificationIDs) < prefs.DailyInterruptBudget
}

func (s *engagementService) notificationSent(row storage.EngagementMomentRow, m engagement.Moment) {
	if engagementAutomaticNotification(m.Kind) && !engagementHasNotificationChannel(row.Channel) {
		if s.notificationIDs == nil {
			s.notificationIDs = map[string]bool{}
		}
		s.notificationIDs[row.ID] = true
	}
}

func engagementNextMidnight(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
}

// deliverAutomaticDesktop reserves a receipt before dispatch, serialized with
// the regular engagement pass. The caller supplies the native delivery.
func (s *engagementService) deliverAutomaticDesktop(send func()) bool {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	ctx, now := context.Background(), s.now()
	if !s.ensureLoaded(ctx) {
		return false
	}
	if !s.persistUnrecorded(ctx, now) {
		return false
	}
	if err := s.refreshNotificationBudget(ctx, now); err != nil {
		return false
	}
	moment := engagement.Moment{Kind: engagement.Kind("system_notice")}
	if !s.notificationAllowed(storage.EngagementMomentRow{}, moment, s.Prefs()) {
		return false
	}
	row, err := s.store.CreateEngagementMoment(ctx, storage.EngagementMomentRow{
		Kind: string(moment.Kind), DedupeKey: "system_notice:" + uuid.NewString(),
		CreatedAt: now.UnixMilli(), SentAt: now.UnixMilli(), State: storage.EngagementStateSent,
		Channel: string(engagement.ChannelDesktop),
	})
	if err != nil {
		return false
	}
	s.notificationIDs[row.ID] = true
	send()
	return true
}

// notifyAutomaticDesktop shares the same budget as engagement delivery. Its
// durable receipt contains no notification text or destination.
func (a *App) notifyAutomaticDesktop(title, body, clickData string) string {
	svc := a.engagementService()
	if svc == nil || notificationExplicitlyDenied() {
		return "suppressed"
	}
	title, body, allowed := a.notificationForDelivery(title, body, clickData)
	if !allowed {
		return "suppressed"
	}
	if !svc.deliverAutomaticDesktop(func() { showDesktopToast(title, body, a.notifyIconPath(), clickData) }) {
		return "suppressed"
	}
	a.speakNotification(title)
	return "desktop"
}
