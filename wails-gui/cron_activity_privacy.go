package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func activityDayBounds(date string) (int64, int64, error) {
	t, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		return 0, 0, err
	}
	return t.UnixMilli(), t.AddDate(0, 0, 1).UnixMilli(), nil
}

// activitySessionsForExternalRead applies the model privacy gate before
// pagination. A protected row must not become a result or displace a safe row.
func (a *App) activitySessionsForExternalRead(limit, offset int, startMs, endMs int64, keyword string) ([]ActivitySessionSummary, error) {
	return a.externalActivityList(context.Background(), limit, offset, startMs, endMs, keyword, nil)
}

type activityReadFilter func(ActivitySessionRow) (bool, error)

func (a *App) externalActivityList(ctx context.Context, limit, offset int, startMs, endMs int64, keyword string, allowed activityReadFilter) ([]ActivitySessionSummary, error) {
	result := []ActivitySessionSummary{}
	if a == nil || a.activity == nil || a.activity.store == nil || a.IsClosedEyesModeEnabled() {
		return result, nil
	}
	store := a.activity.store
	const batchSize = 100
	for cursor, visible := 0, 0; ; cursor += batchSize {
		rows, err := store.GetFilteredSessions(batchSize, cursor, startMs, endMs, keyword)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			ok, err := a.activitySessionAllowsModelReadResultContext(ctx, store, row)
			if err == nil && ok && allowed != nil {
				ok, err = allowed(row)
			}
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			visible++
			if visible <= offset {
				continue
			}
			result = append(result, activitySessionExternalSummary(row))
			if len(result) >= limit {
				return result, nil
			}
		}
		if len(rows) < batchSize {
			return result, nil
		}
	}
}

func (a *App) activitySessionForExternalRead(id string, filter activityReadFilter) (*ActivitySessionDetail, error) {
	if a == nil || a.activity == nil || a.activity.store == nil || a.IsClosedEyesModeEnabled() {
		return nil, sql.ErrNoRows
	}
	store := a.activity.store
	session, err := store.GetSessionByID(id)
	if err != nil {
		return nil, err
	}
	ok, err := a.activitySessionAllowsModelReadResult(store, *session)
	if err == nil && ok && filter != nil {
		ok, err = filter(*session)
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, sql.ErrNoRows
	}
	events, err := store.GetSessionEvents(id)
	if err != nil {
		return nil, err
	}
	segments, err := store.GetSessionSegments(id)
	if err != nil {
		return nil, err
	}
	snapshots, err := store.GetSessionSnapshots(id)
	if err != nil {
		return nil, err
	}
	detail := &ActivitySessionDetail{
		ActivitySessionSummary: activitySessionExternalSummary(*session),
		Events:                 events,
		Segments:               segments,
		Snapshots:              snapshots,
	}
	// Individual key events can split a credential across many fields. The
	// CLI does not need raw keystrokes; leave originals in the local UI.
	for i := range detail.Events {
		detail.Events[i].KeyChar = ""
		detail.Events[i].KeyChars = nil
		detail.Events[i].KeyCode = 0
		detail.Events[i].TypedText = ""
	}
	// Traverse the serialized payload so speech, clipboard previews, AX/OCR,
	// URLs and future string fields all cross the same redaction boundary.
	raw, err := json.Marshal(detail)
	if err != nil {
		return nil, err
	}
	var payload any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	raw, err = json.Marshal(redactActivityExternalPayload(payload))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, detail); err != nil {
		return nil, err
	}
	return detail, nil
}

func redactActivityExternalPayload(value any) any {
	switch v := value.(type) {
	case string:
		// ParsedJSON is an encoded object inside the snapshot envelope.
		trimmed := strings.TrimSpace(v)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var nested any
			decoder := json.NewDecoder(strings.NewReader(v))
			decoder.UseNumber()
			if decoder.Decode(&nested) == nil {
				if raw, err := json.Marshal(redactActivityExternalPayload(nested)); err == nil {
					return string(raw)
				}
			}
		}
		return redactActivityModelText(v)
	case []any:
		for i := range v {
			v[i] = redactActivityExternalPayload(v[i])
		}
		return v
	case map[string]any:
		clean := make(map[string]any, len(v))
		for key, item := range v {
			clean[redactActivityModelText(key)] = redactActivityExternalPayload(item)
		}
		return clean
	default:
		return value
	}
}

func activitySessionExternalSummary(session ActivitySessionRow) ActivitySessionSummary {
	var apps []string
	_ = json.Unmarshal([]byte(session.AppNames), &apps)
	for i := range apps {
		apps[i] = redactActivityModelText(apps[i])
	}
	return ActivitySessionSummary{
		ID: session.ID, StartTime: session.StartTime, EndTime: session.EndTime,
		TriggerKind: session.TriggerKind, AppNames: apps,
		EventCount: session.EventCount, SnapshotCount: session.SnapshotCount,
		TriageStatus: session.TriageStatus, OCRStatus: session.OCRStatus,
	}
}

func (a *App) externalActivitySearch(ctx context.Context, query string, limit int, allowed activityReadFilter) ([]ActivitySemanticResult, error) {
	if a.IsClosedEyesModeEnabled() {
		return nil, nil
	}
	if a.activity == nil || a.activity.store == nil {
		return nil, errors.New("activity unavailable")
	}
	store := a.activity.store
	store.mu.RLock()
	embedder := store.embedder
	store.mu.RUnlock()
	if embedder == nil {
		return nil, errors.New("activity search unavailable")
	}
	hits, err := store.searchEmbeddedSessions(ctx, query, activitySemanticTopK, embedder, func(text string) string { return truncateText(redactActivityModelText(text), 200) })
	if err != nil {
		return nil, err
	}
	out := make([]ActivitySemanticResult, 0, len(hits))
	for _, hit := range hits {
		row, err := store.GetSessionByID(hit.SessionID)
		if err != nil {
			return nil, err
		}
		ok, err := a.activitySessionAllowsModelReadResultContext(ctx, store, *row)
		if err == nil && ok && allowed != nil {
			ok, err = allowed(*row)
		}
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, hit)
			if len(out) == limit {
				break
			}
		}
	}
	return out, ctx.Err()
}
