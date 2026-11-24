package main

import (
	"context"
	"errors"
	"time"

	"wails-gui/storage"
)

// MailOverview is what Settings > Observation shows about Mail.
type MailOverview struct {
	Supported bool `json:"supported"`
	Granted   bool `json:"granted"`
	Enabled   bool `json:"enabled"`
	// MailOpen is whether Mail is running now; Isoform reads only then.
	MailOpen   bool   `json:"mailOpen"`
	LastReadAt string `json:"lastReadAt"`
	LastError  string `json:"lastError"`
	// Stalled is reading stopped after Mail was slow to answer too often;
	// only Refresh reads again.
	Stalled bool `json:"stalled"`
	// Received counts the inbox messages of the last week, Waiting the
	// threads waiting on the user's reply.
	Received int `json:"received"`
	Waiting  int `json:"waiting"`
}

// GetMailOverview reports access, whether Mail is open, the last read and
// what was kept. It never asks macOS for anything and never opens Mail.
func (a *App) GetMailOverview() (MailOverview, error) {
	view := MailOverview{
		Supported: systemMailSupported(),
		Granted:   mailAutomationGranted(),
		MailOpen:  mailRunning(),
	}
	if a == nil || a.store == nil {
		return view, errors.New("the store is not ready")
	}
	ctx := context.Background()
	// A switch that can't be read fails the read; the page shows it with Try
	// again, never as on.
	enabled, err := a.permissionSwitchRead(context.Background(), mailPermissionKey)
	if err != nil {
		return view, err
	}
	view.Enabled = enabled
	view.Stalled = a.mailSync.stalled()
	if last, lastErr, _ := a.mailSync.status(); !last.IsZero() {
		view.LastReadAt = last.UTC().Format(time.RFC3339)
		view.LastError = lastErr
	} else {
		view.LastError = lastErr
		if stored, err := a.store.GetUIState(ctx, mailLastReadKey); err == nil {
			view.LastReadAt = stored
		}
	}
	if !view.Enabled {
		return view, nil
	}
	now := time.Now()
	rows, err := a.store.ListMailMessages(ctx, now.Add(-mailWindowDays*24*time.Hour).UnixMilli())
	if err != nil {
		return view, err
	}
	for _, row := range rows {
		if row.Mailbox == storage.MailboxInbox {
			view.Received++
		}
	}
	view.Waiting = len(mailWaitingOnReply(rows, now))
	return view, nil
}

// SyncMailNow reads Mail right away. It is the Refresh button; it does
// nothing while Mail is closed.
func (a *App) SyncMailNow() error {
	if a == nil || a.mailSync == nil {
		return errors.New("Mail is not available on this computer")
	}
	if !a.isPermissionEnabled(mailPermissionKey) {
		return errors.New("mail reading is switched off")
	}
	if a.IsClosedEyesModeEnabled() {
		return errors.New("observing is paused")
	}
	if !a.mailSync.readingAllowed() {
		return errors.New("Mail is one of the apps you protect")
	}
	if !mailRunning() {
		return errors.New("Mail is not open")
	}
	_, err := a.mailSync.Sync(context.Background(), "refresh")
	return err
}
