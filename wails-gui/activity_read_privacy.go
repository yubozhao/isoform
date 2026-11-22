package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/activitymon/appparser"
)

// Parser registration completes before this package initializes. Snapshots
// can store these stable parser keys instead of the localized app label.
var activityPrivacyParserApps = appparser.Catalog()
var activityPrivacyParserNames = func() map[string]struct{} {
	names := make(map[string]struct{}, len(activityPrivacyParserApps))
	for _, parser := range activityPrivacyParserApps {
		names[strings.ToLower(strings.TrimSpace(parser.Name))] = struct{}{}
	}
	return names
}()

func (a *App) activityDayAllowsModelRead(date string) bool {
	start, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil || a == nil || a.activity == nil || a.activity.store == nil {
		return false
	}
	return a.activityCachedSummaryAllowsModelRead(date, "daily", start.UnixMilli(), start.AddDate(0, 0, 1).UnixMilli())
}

// This fallback applies only to legacy derivatives without recorded source IDs.
// Protected capture rows were never eligible summary inputs.
func (a *App) activityRangeAllowsModelRead(startMs, endMs int64) bool {
	if a == nil || a.activity == nil || a.activity.store == nil || a.IsClosedEyesModeEnabled() || endMs <= startMs {
		return false
	}
	sessions, err := a.activity.store.modelSessionsOverlapping(startMs, endMs)
	if err != nil || len(sessions) == 0 {
		return false
	}
	policy, err := a.activityReadPrivacyPolicy()
	if err != nil {
		return false
	}
	for _, session := range sessions {
		if allowed, err := a.activitySessionAllowsWithPolicy(a.activity.store, session, policy); err != nil || !allowed {
			return false
		}
	}
	return true
}

// activitySessionAllowsModelRead rechecks capture provenance against today's
// privacy settings. The Activity UI may show the user's own protected rows;
// models and the CLI must never use that UI exemption.
func (a *App) activitySessionAllowsModelRead(store *ActivityStore, session ActivitySessionRow) bool {
	return a.activitySessionAllowsModelReadWithInputs(store, session, nil)
}

func (a *App) activitySessionAllowsModelReadWithInputs(store *ActivityStore, session ActivitySessionRow, cache *peopleReadGateCache) bool {
	allowed, err := a.activitySessionAllowsModelReadResultWithInputsAndCache(store, session, nil, cache)
	return err == nil && allowed
}

func (a *App) activitySessionProvenanceWithPeopleCache(store *ActivityStore, session ActivitySessionRow, inputs *activityPrivacyProvenanceInputs, cache *peopleReadGateCache) (activitySourceProvenance, error) {
	if cache != nil {
		if source, found := cache.sources[session.ID]; found {
			return source, nil
		}
	}
	source, err := store.activitySessionProvenanceWithInputs(session, inputs)
	if err == nil && cache != nil {
		if cache.sources == nil {
			cache.sources = make(map[string]activitySourceProvenance)
		}
		cache.sources[session.ID] = source
	}
	return source, err
}

// Storage errors are retryable; confirmed privacy exclusions are not. Callers
// that persist scheduling decisions must preserve this distinction.
func (a *App) activitySessionAllowsModelReadResult(store *ActivityStore, session ActivitySessionRow) (bool, error) {
	return a.activitySessionAllowsModelReadResultContext(context.Background(), store, session)
}

func (a *App) activitySessionAllowsModelReadResultContext(ctx context.Context, store *ActivityStore, session ActivitySessionRow) (bool, error) {
	return a.activitySessionAllowsModelReadResultWithInputsAndCacheContext(ctx, store, session, nil, nil)
}

func (a *App) activitySessionAllowsModelReadResultWithInputsAndCache(store *ActivityStore, session ActivitySessionRow, inputs *activityPrivacyProvenanceInputs, cache *peopleReadGateCache) (bool, error) {
	return a.activitySessionAllowsModelReadResultWithInputsAndCacheContext(context.Background(), store, session, inputs, cache)
}

func (a *App) activitySessionAllowsModelReadResultWithInputsAndCacheContext(ctx context.Context, store *ActivityStore, session ActivitySessionRow, inputs *activityPrivacyProvenanceInputs, cache *peopleReadGateCache) (bool, error) {
	if a == nil || store == nil || store.db == nil {
		return false, errors.New("activity store unavailable")
	}
	if a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx) || session.TriageStatus == "protected" {
		return false, nil
	}
	policy, err := a.activityReadPrivacyPolicyContextWithPeopleCache(ctx, cache)
	if err != nil {
		return false, err
	}
	return a.activitySessionAllowsWithPolicyAndInputsAndCache(store, session, policy, inputs, cache)
}

func (a *App) activitySessionAllowsWithPolicy(store *ActivityStore, session ActivitySessionRow, policy activityReadPrivacyPolicy) (bool, error) {
	return a.activitySessionAllowsWithPolicyAndInputs(store, session, policy, nil)
}

func (a *App) activitySessionAllowsWithPolicyAndInputs(store *ActivityStore, session ActivitySessionRow, policy activityReadPrivacyPolicy, inputs *activityPrivacyProvenanceInputs) (bool, error) {
	return a.activitySessionAllowsWithPolicyAndInputsAndCache(store, session, policy, inputs, nil)
}

func (a *App) activitySessionAllowsWithPolicyAndInputsAndCache(store *ActivityStore, session ActivitySessionRow, policy activityReadPrivacyPolicy, inputs *activityPrivacyProvenanceInputs, cache *peopleReadGateCache) (bool, error) {
	if a == nil || store == nil || store.db == nil {
		return false, errors.New("activity store unavailable")
	}
	declared := activitySourceProvenance{StartTime: session.StartTime, EndTime: session.EndTime, Private: session.TriageStatus == "protected"}
	if cache != nil {
		if session.AppNames != "" {
			apps, valid := a.peopleSessionAppNamesForRead(session, cache)
			if !valid {
				return false, nil
			}
			declared.Apps = append(append([]string(nil), apps...), session.MeetingApp)
		} else {
			declared.Apps = append(declared.Apps, session.MeetingApp)
		}
	} else {
		if session.AppNames != "" && json.Unmarshal([]byte(session.AppNames), &declared.Apps) != nil {
			return false, nil
		}
		declared.Apps = append(declared.Apps, session.MeetingApp)
	}
	if !policy.allows(declared) {
		return false, nil
	}
	source, err := a.activitySessionProvenanceWithPeopleCache(store, session, inputs, cache)
	if err != nil {
		if errors.Is(err, errMeetingTranscriptNotReadable) {
			return false, nil
		}
		return false, err
	}
	if !policy.allows(source) {
		return false, nil
	}
	if session.TranscriptPath != "" {
		current, err := store.GetSessionByID(session.ID)
		if err != nil {
			return false, err
		}
		if current == nil {
			return false, errors.New("transcript source unavailable")
		}
		session = *current
		if _, err := a.transcriptSourcesWithPolicy(store, session, "", policy); err != nil {
			if errors.Is(err, errMeetingTranscriptNotReadable) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

// Pause history is read once per operation; decisions are never cached across
// operations. Sensitive-app settings and closed eyes remain live checks.
type activityReadPrivacyPolicy struct {
	app         *App
	pauses      []observationPause
	pausedBrief bool
}

func (a *App) activityReadPrivacyPolicy() (activityReadPrivacyPolicy, error) {
	return a.activityReadPrivacyPolicyContext(context.Background())
}

// Registry callers carry their read deadline into the pause-history query.
func (a *App) activityReadPrivacyPolicyContext(ctx context.Context) (activityReadPrivacyPolicy, error) {
	return a.activityReadPrivacyPolicyContextWithPeopleCache(ctx, nil)
}

func (a *App) activityReadPrivacyPolicyContextWithPeopleCache(ctx context.Context, cache *peopleReadGateCache) (activityReadPrivacyPolicy, error) {
	policy := activityReadPrivacyPolicy{app: a, pausedBrief: pausedBriefReadAllowed(ctx)}
	if a == nil {
		return policy, errors.New("activity app unavailable")
	}
	if cache != nil && cache.policySet {
		// The cached source policy is reusable, but this narrow proof grant
		// belongs only to the current context, never to the cache.
		policy = cache.policy
		policy.pausedBrief = pausedBriefReadAllowed(ctx)
		return policy, cache.policyErr
	}
	if a.store != nil {
		var err error
		policy.pauses, err = a.observationPausesContextWithPeopleCache(ctx, cache)
		if err != nil {
			if cache != nil {
				cache.policySet = true
				cache.policyErr = err
			}
			return policy, err
		}
	}
	if cache != nil {
		cache.policySet = true
		cache.policy = policy
	}
	return policy, nil
}

func (p activityReadPrivacyPolicy) allows(source activitySourceProvenance) bool {
	if p.app == nil || p.app.IsClosedEyesModeEnabled() && !p.pausedBrief {
		return false
	}
	return p.allowsStoredDerivative(source)
}

// Owner-only stored derivatives retain direct access while Closed Eyes is on.
// Source privacy and the historical pause windows still apply.
func (p activityReadPrivacyPolicy) allowsStoredDerivative(source activitySourceProvenance) bool {
	if p.app == nil || source.Private {
		return false
	}
	for _, app := range source.Apps {
		if activitySourceAppIsSensitive(app, app) {
			return false
		}
	}
	for _, pause := range p.pauses {
		if source.EndTime > pause.Start && (pause.End == 0 || source.StartTime < pause.End) {
			return false
		}
	}
	return true
}

func (a *App) activitySourceProvenanceAllowsRead(source activitySourceProvenance) (bool, error) {
	return a.activitySourceProvenanceAllowsReadWithCache(source, nil)
}

func (a *App) activitySourceProvenanceAllowsReadWithCache(source activitySourceProvenance, cache *peopleReadGateCache) (bool, error) {
	policy, err := a.activityReadPrivacyPolicyContextWithPeopleCache(context.Background(), cache)
	if err != nil {
		return false, err
	}
	return policy.allows(source), nil
}

// Minimal provenance survives raw-data retention without retaining event text.
type activitySourceProvenance struct {
	SessionID string   `json:"sessionId"`
	StartTime int64    `json:"startTime"`
	EndTime   int64    `json:"endTime"`
	Apps      []string `json:"apps,omitempty"`
	Private   bool     `json:"private,omitempty"`
	Retained  bool     `json:"retained,omitempty"`
	// Explicit capture completeness is required for MCP derivative sharing.
	// Older receipts without this proof cannot authorize that stronger boundary.
	MCPVerified bool `json:"mcpVerified,omitempty"`
	// A non-nil list proves every snapshot was checked, including zero
	// snapshots. Persist identities only, never captured text or URL paths.
	MCPSnapshots *[]activitySnapshotPrivacyIdentity `json:"mcpSnapshots,omitempty"`
}

func (s *ActivityStore) decodeActivitySessionProvenance(tx *sql.Tx, session ActivitySessionRow) (activitySourceProvenance, error) {
	return s.decodeActivitySessionProvenanceContext(context.Background(), tx, session)
}

func (s *ActivityStore) decodeActivitySessionProvenanceContext(ctx context.Context, tx *sql.Tx, session ActivitySessionRow) (activitySourceProvenance, error) {
	var eventData []string
	rows, err := tx.QueryContext(ctx, `SELECT data FROM activity_events WHERE session_id = ? ORDER BY id`, session.ID)
	if err != nil {
		return activitySourceProvenance{}, err
	}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			rows.Close()
			return activitySourceProvenance{}, err
		}
		eventData = append(eventData, data)
	}
	readErr, closeErr := rows.Err(), rows.Close()
	if readErr != nil {
		return activitySourceProvenance{}, readErr
	}
	if closeErr != nil {
		return activitySourceProvenance{}, closeErr
	}
	var snapshots []activityPrivacySnapshotInput
	rows, err = tx.QueryContext(ctx, `SELECT COALESCE(app, ''), COALESCE(browser_url, ''), COALESCE(parsed_json, ''), privacy_held FROM activity_snapshots WHERE session_id = ? ORDER BY id`, session.ID)
	if err != nil {
		return activitySourceProvenance{}, err
	}
	for rows.Next() {
		var input activityPrivacySnapshotInput
		if err := rows.Scan(&input.app, &input.browserURL, &input.parsed, &input.held); err != nil {
			rows.Close()
			return activitySourceProvenance{}, err
		}
		snapshots = append(snapshots, input)
	}
	readErr, closeErr = rows.Err(), rows.Close()
	if readErr != nil {
		return activitySourceProvenance{}, readErr
	}
	if closeErr != nil {
		return activitySourceProvenance{}, closeErr
	}
	return s.decodeActivitySessionProvenanceInputs(session, eventData, snapshots)
}

func (s *ActivityStore) decodeActivitySessionProvenanceInputs(session ActivitySessionRow, eventData []string, snapshotInputs []activityPrivacySnapshotInput) (activitySourceProvenance, error) {
	s.readPrivacySources.sourceScans.Add(1)
	s.readPrivacySources.eventRows.Add(uint64(len(eventData)))
	s.readPrivacySources.snapshotRows.Add(uint64(len(snapshotInputs)))
	source := activitySourceProvenance{SessionID: session.ID, StartTime: session.StartTime, EndTime: session.EndTime, Private: session.TriageStatus == "protected"}
	if session.AppNames != "" && json.Unmarshal([]byte(session.AppNames), &source.Apps) != nil {
		source.Private = true
	}
	source.MCPVerified = len(source.Apps) > 0 && !source.Private
	source.Apps = append(source.Apps, session.MeetingApp)
	for _, data := range eventData {
		var event activitymon.ActivityEvent
		if json.Unmarshal([]byte(data), &event) != nil {
			source.Private = true
			source.MCPVerified = false
			continue
		}
		if strings.TrimSpace(event.AppBundleID) == "" || (mcpReadBrowser(event.AppBundleID, event.AppName) && strings.TrimSpace(event.WindowTitle) == "") {
			source.MCPVerified = false
		}
		source.Apps = append(source.Apps, event.AppBundleID, event.AppName, event.SpeechDictationApp)
		source.Private = source.Private || event.PrivateWindow || activitymon.IsPrivateBrowsing(event.WindowTitle)
	}
	if len(eventData) == 0 {
		source.MCPVerified = false
	}
	snapshots := make([]activitySnapshotPrivacyIdentity, 0)
	source.MCPSnapshots = &snapshots
	for _, input := range snapshotInputs {
		if input.held {
			source.Private = true
			source.MCPVerified = false
		}
		identity, identityErr := readActivitySnapshotPrivacyIdentity(input.app, input.browserURL, input.parsed)
		if identityErr != nil {
			source.MCPVerified = false
		} else {
			snapshots = append(snapshots, identity)
		}
		source.Apps = append(source.Apps, input.app)
		if strings.TrimSpace(input.parsed) != "" {
			var content struct {
				Title    string            `json:"title"`
				Metadata map[string]string `json:"metadata"`
			}
			if json.Unmarshal([]byte(input.parsed), &content) != nil {
				source.Private = true
				continue
			}
			source.Apps = append(source.Apps, content.Metadata["bundleId"])
			source.Private = source.Private || activitymon.IsPrivateBrowsing(content.Title)
		}
	}
	seen := make(map[string]bool)
	apps := source.Apps[:0]
	for _, app := range source.Apps {
		if app != "" && !seen[app] {
			seen[app] = true
			apps = append(apps, app)
		}
	}
	source.Apps = apps
	return source, nil
}

func activitySourceAppIsSensitive(bundleID, name string) bool {
	_, sensitive := pii.IsSensitiveApp(strings.TrimPrefix(strings.TrimSpace(bundleID), "app:"))
	if sensitive || pii.IsSensitiveAppName(name) {
		return true
	}
	if _, ok := activityPrivacyParserNames[strings.ToLower(strings.TrimSpace(name))]; !ok {
		return false
	}
	for _, parser := range activityPrivacyParserApps {
		if !strings.EqualFold(strings.TrimSpace(name), parser.Name) {
			continue
		}
		if pii.IsSensitiveAppName(parser.Label) {
			return true
		}
		for _, bundle := range parser.Bundles {
			if _, sensitive := pii.IsSensitiveApp(bundle); sensitive {
				return true
			}
		}
	}
	return false
}
