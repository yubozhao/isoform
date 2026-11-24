package main

import (
	"context"
	"path/filepath"
	"testing"

	"wails-gui/storage"
)

// A Settings row never shows a switch it couldn't read as on: the read
// reports the failure instead of the switch's default.
func TestPermissionSwitchReadReportsAFailedRead(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "switch.db"))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{store: store}
	ctx := context.Background()
	if on, err := app.permissionSwitchRead(ctx, mailPermissionKey); err != nil || on != !permissionsOffByDefault[mailPermissionKey] {
		t.Fatalf("a switch never saved reads as its default: on=%v err=%v", on, err)
	}
	if err := store.SetUIState(ctx, permissionUIStateKey(filesPermissionKey), "false"); err != nil {
		t.Fatal(err)
	}
	if on, err := app.permissionSwitchRead(ctx, filesPermissionKey); err != nil || on {
		t.Fatalf("a saved off reads off: on=%v err=%v", on, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{mailPermissionKey, filesPermissionKey, calendarPermissionKey, remindersPermissionKey} {
		if _, err := app.permissionSwitchRead(ctx, key); err == nil {
			t.Errorf("%s: a failed read must report an error", key)
		}
	}
	if _, err := app.GetMailOverview(); err == nil {
		t.Error("Mail's overview must fail when its switch can't be read")
	}
	if _, err := app.GetFilesOverview(); err == nil {
		t.Error("Files' overview must fail when its switch can't be read")
	}
	if _, err := app.GetCalendarOverview(); err == nil {
		t.Error("Calendar's overview must fail when its switch can't be read")
	}
	if _, err := app.GetRemindersOverview(); err == nil {
		t.Error("Reminders' overview must fail when its switch can't be read")
	}
	if _, err := (&App{}).permissionSwitchRead(ctx, mailPermissionKey); err == nil {
		t.Error("no store must report an error, not the default")
	}
}
