package main

import (
	"context"
	"errors"
	"sort"
	"time"

	"wails-gui/storage"
)

// ReminderListView is one Reminders list in Settings > Observation: its name
// and how many of its reminders are open and overdue.
type ReminderListView struct {
	Title   string `json:"title"`
	Open    int    `json:"open"`
	Overdue int    `json:"overdue"`
}

// RemindersOverview is what Settings > Observation shows about reminders.
type RemindersOverview struct {
	Supported     bool               `json:"supported"`
	Authorization string             `json:"authorization"` // granted | denied | restricted | write_only | not_determined | not_applicable
	Granted       bool               `json:"granted"`
	Enabled       bool               `json:"enabled"`
	LastSyncAt    string             `json:"lastSyncAt"`
	LastError     string             `json:"lastError"`
	OpenCount     int                `json:"openCount"`
	OverdueCount  int                `json:"overdueCount"`
	Lists         []ReminderListView `json:"lists"`
}

// GetRemindersOverview reports access, the last read and the open reminders
// per list.
func (a *App) GetRemindersOverview() (RemindersOverview, error) {
	ctx := context.Background()
	view := RemindersOverview{
		Supported:     systemRemindersSupported(),
		Authorization: remindersAuthorization(),
		Granted:       remindersAccessGranted(),
		Lists:         []ReminderListView{},
	}
	if a == nil || a.store == nil {
		return view, errors.New("the store is not ready")
	}
	// A switch that can't be read fails the read; the page shows it with Try
	// again, never as on.
	enabled, err := a.permissionSwitchRead(context.Background(), remindersPermissionKey)
	if err != nil {
		return view, err
	}
	view.Enabled = enabled
	if last, lastErr := a.remindersSync.status(); !last.IsZero() {
		view.LastSyncAt = last.UTC().Format(time.RFC3339)
		view.LastError = lastErr
	} else if lastErr != "" {
		view.LastError = lastErr
	}
	if view.LastSyncAt == "" {
		if stored, err := a.store.GetUIState(ctx, remindersLastSyncKey); err == nil {
			view.LastSyncAt = stored
		}
	}
	rows, err := a.store.ListReminders(ctx, false)
	if err != nil {
		return view, err
	}
	view.OpenCount, view.OverdueCount, view.Lists = summarizeReminders(rows, time.Now(), time.Local)
	return view, nil
}

// summarizeReminders counts the open reminders, the overdue ones, and both
// per list, largest list first.
func summarizeReminders(rows []storage.ReminderRow, now time.Time, loc *time.Location) (int, int, []ReminderListView) {
	byList := map[string]*ReminderListView{}
	open, overdue := 0, 0
	for _, row := range rows {
		if row.CompletedAt > 0 || row.DeletedAt > 0 {
			continue
		}
		open++
		list := byList[row.List]
		if list == nil {
			list = &ReminderListView{Title: row.List}
			byList[row.List] = list
		}
		list.Open++
		if reminderFromRow(row).Overdue(now, loc) {
			overdue++
			list.Overdue++
		}
	}
	lists := make([]ReminderListView, 0, len(byList))
	for _, list := range byList {
		lists = append(lists, *list)
	}
	sort.Slice(lists, func(i, j int) bool {
		if lists[i].Open != lists[j].Open {
			return lists[i].Open > lists[j].Open
		}
		return lists[i].Title < lists[j].Title
	})
	return open, overdue, lists
}

// SyncRemindersNow reads the reminders right away. It is the Refresh button.
func (a *App) SyncRemindersNow() error {
	if a == nil || a.remindersSync == nil {
		return errRemindersUnsupported
	}
	if !remindersAccessGranted() {
		return errRemindersNotAuthorized
	}
	if !a.isPermissionEnabled(remindersPermissionKey) {
		return errors.New("reminders reading is switched off")
	}
	_, err := a.remindersSync.Sync(context.Background(), "refresh")
	return err
}
