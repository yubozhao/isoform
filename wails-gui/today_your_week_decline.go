package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

const todayYourWeekDeclinePrefix = "today.your_week.declined."

func todayYourWeekDeclineKey(start string) (string, error) {
	day, err := time.Parse("2006-01-02", start)
	if err != nil || day.Format("2006-01-02") != start {
		return "", errors.New("a valid week start is required")
	}
	return todayYourWeekDeclinePrefix + start, nil
}

// The first phone implementation stored a digest of both dates. Keep reading
// it while all new writes use the desktop's shared week key.
func todayYourWeekLegacyDeclineKey(start, end string) string {
	digest := sha256.Sum256([]byte(start + "\x00" + end))
	return "phone_your_week_declined:" + hex.EncodeToString(digest[:])
}

func (a *App) todayYourWeekDeclined(ctx context.Context, start, end string) (bool, error) {
	if !featureOn(featureBriefExtras) {
		return false, nil
	}
	key, err := todayYourWeekDeclineKey(start)
	if err != nil {
		return false, err
	}
	if a == nil || a.store == nil {
		return false, errors.New("the store is not ready")
	}
	value, err := a.store.GetUIState(ctx, key)
	if err != nil || value == "true" {
		return value == "true", err
	}
	// Briefs can end on different workdays. Check each possible end in the
	// same week so an earlier phone choice is visible to a later brief too.
	day, _ := time.Parse("2006-01-02", start)
	ends := []string{end}
	for offset := 0; offset < 7; offset++ {
		ends = append(ends, day.AddDate(0, 0, offset).Format("2006-01-02"))
	}
	seen := make(map[string]bool, len(ends))
	for _, candidate := range ends {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		value, err = a.store.GetUIState(ctx, todayYourWeekLegacyDeclineKey(start, candidate))
		if err != nil || value == "true" {
			return value == "true", err
		}
	}
	return false, nil
}

// GetTodayYourWeekDeclined reads the person's decision for this specific week.
func (a *App) GetTodayYourWeekDeclined(start string) (bool, error) {
	_, err := todayYourWeekDeclineKey(start)
	if err != nil {
		return false, err
	}
	return a.todayYourWeekDeclined(context.Background(), start, "")
}

// DeclineTodayYourWeek saves Not now for this week only.
func (a *App) DeclineTodayYourWeek(start string) error {
	if !featureOn(featureBriefExtras) {
		return errFeatureOff(featureBriefExtras)
	}
	key, err := todayYourWeekDeclineKey(start)
	if err != nil {
		return err
	}
	if a == nil || a.store == nil {
		return errors.New("the store is not ready")
	}
	return a.store.SetUIState(context.Background(), key, "true")
}
