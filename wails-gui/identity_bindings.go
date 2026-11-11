package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/identity"
	"wails-gui/storage"
)

// PrincipalDTO is the JSON shape sent to/received from the frontend. It
// mirrors storage.PrincipalRow with json tags suitable for the Wails bridge.
type PrincipalDTO struct {
	MergeID       string         `json:"mergeId,omitempty"`
	ID            string         `json:"id"`
	ContactName   string         `json:"contactName"`
	ContactPhoto  string         `json:"contactPhoto"`
	ContactLinked bool           `json:"contactLinked"`
	Kind          string         `json:"kind"`
	DisplayName   string         `json:"displayName"`
	Relationship  string         `json:"relationship"`
	Notes         string         `json:"notes"`
	CreatedFrom   string         `json:"createdFrom"`
	Source        string         `json:"source"`
	ExternalRef   string         `json:"externalRef"`
	Confidence    float64        `json:"confidence"`
	Aliases       []string       `json:"aliases"`
	CreatedAt     int64          `json:"createdAt"`
	UpdatedAt     int64          `json:"updatedAt"`
	AppHandles    []AppHandleDTO `json:"appHandles,omitempty"`
	// QualifiedDisplayName combines the principal's app context with their
	// nickname so Anonymous principals from different apps can be told
	// apart at a glance in lists. Conventions:
	//   - kind=anonymous, exactly 1 distinct app in AppHandles →
	//     "{AppDisplayName} · {DisplayName}" (e.g. "WeChat · 妈")
	//   - kind=anonymous, 0 or 2+ apps → DisplayName (no qualifier;
	//     multi-app anonymous is effectively a soft contact and the
	//     prefix is misleading)
	//   - kind=self / contact → DisplayName (user has actively named
	//     this person; their chosen identity wins)
	// Computed in the DTO transformer — see qualifyDisplayName.
	QualifiedDisplayName string `json:"qualifiedDisplayName"`
}

// AppHandleDTO mirrors storage.AppHandleRow for the frontend.
type AppHandleDTO struct {
	ID            int64  `json:"id"`
	PrincipalID   string `json:"principalId"`
	App           string `json:"app"`
	HandleID      string `json:"handleId"`
	DisplayHandle string `json:"displayHandle"`
	CreatedAt     int64  `json:"createdAt"`
}

func principalToDTO(p storage.PrincipalRow) PrincipalDTO {
	if p.Aliases == nil {
		p.Aliases = []string{}
	}
	return PrincipalDTO{
		ID:                   p.ID,
		Kind:                 p.Kind,
		DisplayName:          p.DisplayName,
		Relationship:         p.Relationship,
		Notes:                p.Notes,
		CreatedFrom:          p.CreatedFrom,
		Source:               p.Source,
		ExternalRef:          p.ExternalRef,
		Confidence:           p.Confidence,
		Aliases:              p.Aliases,
		CreatedAt:            p.CreatedAt,
		UpdatedAt:            p.UpdatedAt,
		QualifiedDisplayName: qualifyDisplayName(p.Kind, p.DisplayName, nil),
	}
}

// qualifyDisplayName builds the at-a-glance label for a principal —
// see PrincipalDTO.QualifiedDisplayName for the convention. AppHandles
// drives the qualifier; passing nil OR an empty slice means "we don't
// know what apps this principal has yet" and the function falls back
// to the bare display name (which is safe — the caller can recompute
// after attaching handles).
func qualifyDisplayName(kind, displayName string, handles []AppHandleDTO) string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		return ""
	}
	if kind != storage.PrincipalKindAnonymous {
		return name
	}
	apps := map[string]struct{}{}
	for _, h := range handles {
		app := strings.TrimSpace(strings.ToLower(h.App))
		if app == "" {
			continue
		}
		apps[app] = struct{}{}
	}
	if len(apps) != 1 {
		return name
	}
	var app string
	for k := range apps {
		app = k
	}
	return identity.AppDisplayName(app) + " · " + name
}

func appHandleToDTO(h storage.AppHandleRow) AppHandleDTO {
	return AppHandleDTO{
		ID:            h.ID,
		PrincipalID:   h.PrincipalID,
		App:           h.App,
		HandleID:      h.HandleID,
		DisplayHandle: h.DisplayHandle,
		CreatedAt:     h.CreatedAt,
	}
}

// attachHandles adds the given handles to the DTO and re-qualifies the
// display name based on the resulting app set. Use after principalToDTO
// any time you have the principal's app_handles in hand — without it
// QualifiedDisplayName falls back to the bare display name for
// Anonymous (because the constructor didn't know what apps were bound).
func (d *PrincipalDTO) attachHandles(handles []storage.AppHandleRow) {
	for _, h := range handles {
		d.AppHandles = append(d.AppHandles, appHandleToDTO(h))
	}
	d.QualifiedDisplayName = qualifyDisplayName(d.Kind, d.DisplayName, d.AppHandles)
}

func (a *App) requireStore() (*storage.Store, error) {
	if a == nil || a.store == nil {
		return nil, errors.New("storage not initialized")
	}
	return a.store, nil
}

// ListPrincipals returns every principal (Self / Contact / ContentSubject /
// Anonymous) currently stored. Each entry is enriched with its aliases and
// every bound app handle for the convenience of the onboarding/management UI.
func (a *App) ListPrincipals() ([]PrincipalDTO, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	rows, err := store.ListPrincipals(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]PrincipalDTO, 0, len(rows))
	for _, p := range rows {
		dto := principalToDTO(p)
		handles, err := store.ListAppHandlesByPrincipal(context.Background(), p.ID)
		if err != nil {
			return nil, fmt.Errorf("list handles for %s: %w", p.ID, err)
		}
		dto.attachHandles(handles)
		out = append(out, dto)
	}
	dtos := make([]*PrincipalDTO, len(out))
	for i := range out {
		dtos[i] = &out[i]
	}
	if err := decorateContactDTOs(context.Background(), store, dtos); err != nil {
		return nil, err
	}
	return out, nil
}

// ListPrincipalsByKind filters ListPrincipals by kind. Frontends use this
// when rendering only the Contacts list, only the Self profile, etc.
func (a *App) ListPrincipalsByKind(kind string) ([]PrincipalDTO, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	rows, err := store.ListPrincipalsByKind(context.Background(), kind)
	if err != nil {
		return nil, err
	}
	out := make([]PrincipalDTO, 0, len(rows))
	for _, p := range rows {
		out = append(out, principalToDTO(p))
	}
	return out, nil
}

// EnsureSelfProfile creates or updates the singleton self principal. Use
// during first-run onboarding to capture the user's name and chat aliases.
// If a self principal already exists and the caller supplies a different
// display name, the stored name is updated so the user can rename via
// onboarding or settings.
func (a *App) EnsureSelfProfile(displayName string, aliases []string) (PrincipalDTO, error) {
	store, err := a.requireStore()
	if err != nil {
		return PrincipalDTO{}, err
	}
	row, err := store.EnsureSelf(context.Background(), displayName, aliases)
	if err != nil {
		return PrincipalDTO{}, err
	}

	// If the existing row has a different name, update it.
	if row.DisplayName != displayName && displayName != "" {
		row.DisplayName = displayName
		row, err = store.UpdatePrincipal(context.Background(), row)
		if err != nil {
			return PrincipalDTO{}, err
		}
	}

	// Invalidate the cached display name so getUserDisplayName picks
	// up the new value on its next call.
	a.mu.Lock()
	a.cachedDisplayName = ""
	a.mu.Unlock()

	return principalToDTO(row), nil
}

// CreateContact persists a new Contact principal with optional aliases and
// per-app handles. Returns the newly inserted principal including a fresh
// ID. handles is allowed to be nil for callers that bind handles separately.
func (a *App) CreateContact(displayName, relationship string, aliases []string, handles []AppHandleDTO) (PrincipalDTO, error) {
	store, err := a.requireStore()
	if err != nil {
		return PrincipalDTO{}, err
	}
	return createContactRecord(context.Background(), store, "manual", displayName, relationship, aliases, handles)
}

// createContactRecord inserts a Contact principal with its creation history,
// binds the given handles and returns the re-read DTO. Imports use the
// provenance-aware helper to also retain their source identifier.
func createContactRecord(ctx context.Context, store *storage.Store, createdFrom, displayName, relationship string, aliases []string, handles []AppHandleDTO) (PrincipalDTO, error) {
	return createContactRecordWithProvenance(ctx, store, createdFrom, "", "", displayName, relationship, aliases, handles)
}

// createContactRecordWithProvenance persists the source identifier in the same
// insert as the principal, so an import cannot leave an unmarked contact behind.
func createContactRecordWithProvenance(ctx context.Context, store *storage.Store, createdFrom, source, externalRef, displayName, relationship string, aliases []string, handles []AppHandleDTO) (PrincipalDTO, error) {
	created, err := store.CreatePrincipal(ctx, storage.PrincipalRow{
		Kind:         storage.PrincipalKindContact,
		DisplayName:  displayName,
		Relationship: relationship,
		CreatedFrom:  createdFrom,
		Source:       source,
		ExternalRef:  externalRef,
		Confidence:   1.0,
		Aliases:      aliases,
	})
	if err != nil {
		return PrincipalDTO{}, err
	}
	for _, h := range handles {
		if _, err := store.BindAppHandle(ctx, created.ID, h.App, h.HandleID, h.DisplayHandle); err != nil {
			return PrincipalDTO{}, fmt.Errorf("bind handle %+v: %w", h, err)
		}
	}
	// Re-read so the returned DTO includes both aliases and freshly
	// bound handles in one consistent payload.
	final, err := store.GetPrincipal(ctx, created.ID)
	if err != nil {
		return PrincipalDTO{}, err
	}
	dto := principalToDTO(final)
	finalHandles, err := store.ListAppHandlesByPrincipal(ctx, created.ID)
	if err != nil {
		return PrincipalDTO{}, err
	}
	dto.attachHandles(finalHandles)
	return dto, nil
}

// UpdatePrincipal replaces mutable fields and aliases. Use to fix typos or
// adjust relationships from the management UI. Use PromoteAnonymous to
// convert an anonymous bucket; this method refuses kind changes.
func (a *App) UpdatePrincipal(dto PrincipalDTO) (PrincipalDTO, error) {
	store, err := a.requireStore()
	if err != nil {
		return PrincipalDTO{}, err
	}
	existing, err := store.GetPrincipal(context.Background(), dto.ID)
	if err != nil {
		return PrincipalDTO{}, err
	}
	if dto.Kind != "" && dto.Kind != existing.Kind {
		return PrincipalDTO{}, fmt.Errorf("UpdatePrincipal cannot change kind (got %q, existing %q); use PromoteAnonymous instead", dto.Kind, existing.Kind)
	}
	updated, err := store.UpdatePrincipal(context.Background(), storage.PrincipalRow{
		ID:           dto.ID,
		Kind:         existing.Kind,
		DisplayName:  dto.DisplayName,
		Relationship: dto.Relationship,
		Notes:        dto.Notes,
		CreatedFrom:  existing.CreatedFrom,
		Source:       existing.Source,
		ExternalRef:  existing.ExternalRef,
		Confidence:   existing.Confidence,
		Aliases:      dto.Aliases,
	})
	if err != nil {
		return PrincipalDTO{}, err
	}
	return principalToDTO(updated), nil
}

// DeletePrincipal removes a principal and all of its bindings.
func (a *App) DeletePrincipal(id string) error {
	store, err := a.requireStore()
	if err != nil {
		return err
	}
	return store.DeletePrincipal(context.Background(), id)
}

// BindAppHandle attaches a per-app account identifier to a principal. Pass
// either a stable handleID, a humanDisplayHandle, or both — the binding is
// keyed by the (app, handleID, displayHandle) tuple.
func (a *App) BindAppHandle(principalID, app, handleID, displayHandle string) (AppHandleDTO, error) {
	store, err := a.requireStore()
	if err != nil {
		return AppHandleDTO{}, err
	}
	row, err := store.BindAppHandle(context.Background(), principalID, app, handleID, displayHandle)
	if err != nil {
		return AppHandleDTO{}, err
	}
	return appHandleToDTO(row), nil
}

// UnbindAppHandle removes a handle binding by row id.
func (a *App) UnbindAppHandle(id int64) error {
	store, err := a.requireStore()
	if err != nil {
		return err
	}
	return store.UnbindAppHandle(context.Background(), id)
}

// PrincipalActivitySessionDTO is one row in the Who you know page's "Activity
// sessions" drawer: a session this principal was seen in plus enough
// metadata for the row to be self-describing and click-routable to the
// Activity page detail.
type PrincipalActivitySessionDTO struct {
	SessionID   string   `json:"sessionId"`
	StartTime   int64    `json:"startTime"`
	EndTime     int64    `json:"endTime"`
	AppNames    []string `json:"appNames"`
	IsMeeting   bool     `json:"isMeeting,omitempty"`
	FirstSeenAt int64    `json:"firstSeenAt"`
	LastSeenAt  int64    `json:"lastSeenAt"`
}

// ListPrincipalActivitySessions returns up to limit activity sessions in
// which the given principal was seen, ordered by last_seen_at DESC. Each
// row is enriched with the matching ActivitySessionRow's StartTime /
// EndTime / AppNames (parsed from the stored JSON) so the frontend can
// render the row label without a second round-trip.
//
// Sessions that exist in principal_session_sightings but whose
// activity_sessions row is gone (manually pruned, etc.) are silently
// skipped — the People row should not break because the underlying
// session was deleted.
func (a *App) ListPrincipalActivitySessions(principalID string, limit int) (perfValue []PrincipalActivitySessionDTO, perfErr error) {
	defer logPageBinding("ListPrincipalActivitySessions", time.Now(), &perfValue)
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	if a.activity == nil || a.activity.store == nil {
		return []PrincipalActivitySessionDTO{}, nil
	}
	rows, err := store.ListPrincipalSessionSightings(context.Background(), principalID, limit)
	if err != nil {
		return nil, err
	}
	// Batch-fetch every session's metadata in ONE query instead of
	// N round-trips. Sessions that were pruned underneath us are
	// silently absent from the map; we skip those rows so the
	// frontend never gets a broken-link entry.
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.SessionID)
	}
	sessionByID, err := a.activity.store.GetSessionsByIDs(ids)
	if err != nil {
		return nil, err
	}
	out := make([]PrincipalActivitySessionDTO, 0, len(rows))
	for _, r := range rows {
		session, ok := sessionByID[r.SessionID]
		if !ok || session == nil {
			continue
		}
		var apps []string
		if session.AppNames != "" {
			_ = json.Unmarshal([]byte(session.AppNames), &apps)
		}
		out = append(out, PrincipalActivitySessionDTO{
			SessionID:   session.ID,
			StartTime:   session.StartTime,
			EndTime:     session.EndTime,
			AppNames:    apps,
			IsMeeting:   session.IsMeeting,
			FirstSeenAt: r.FirstSeenAt,
			LastSeenAt:  r.LastSeenAt,
		})
	}
	return out, nil
}

// SessionPrincipalDTO is the inverse of PrincipalActivitySessionDTO:
// one principal that appeared in a given Activity session. Surfaced
// in the Activity detail right pane so the user can see who Yansu
// identified during this session, and click to highlight a specific
// person's messages.
type SessionPrincipalDTO struct {
	PrincipalID          string `json:"principalId"`
	DisplayName          string `json:"displayName"`
	Kind                 string `json:"kind"`
	FirstSeenAt          int64  `json:"firstSeenAt"`
	LastSeenAt           int64  `json:"lastSeenAt"`
	QualifiedDisplayName string `json:"qualifiedDisplayName"`
}

// ListSessionPrincipals returns up to limit principals that were seen
// in the given activity session, newest last-seen first. App handles
// for each principal are batch-fetched so QualifiedDisplayName can be
// computed without N+1 round-trips.
func (a *App) ListSessionPrincipals(sessionID string, limit int) ([]SessionPrincipalDTO, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	rows, err := store.ListSessionPrincipalSightings(context.Background(), sessionID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]SessionPrincipalDTO, 0, len(rows))
	for _, r := range rows {
		var handleDTOs []AppHandleDTO
		// Only Anonymous needs handles for the qualifier; skip the
		// extra query for Self / Contact since their displayName wins.
		if r.Kind == storage.PrincipalKindAnonymous {
			handles, err := store.ListAppHandlesByPrincipal(context.Background(), r.PrincipalID)
			if err != nil {
				return nil, fmt.Errorf("list handles for %s: %w", r.PrincipalID, err)
			}
			handleDTOs = make([]AppHandleDTO, 0, len(handles))
			for _, h := range handles {
				handleDTOs = append(handleDTOs, appHandleToDTO(h))
			}
		}
		out = append(out, SessionPrincipalDTO{
			PrincipalID:          r.PrincipalID,
			DisplayName:          r.DisplayName,
			Kind:                 r.Kind,
			FirstSeenAt:          r.FirstSeenAt,
			LastSeenAt:           r.LastSeenAt,
			QualifiedDisplayName: qualifyDisplayName(r.Kind, r.DisplayName, handleDTOs),
		})
	}
	return out, nil
}

// CleanupReportDTO mirrors storage.CleanupReport for the frontend.
type CleanupReportDTO struct {
	Scanned           int      `json:"scanned"`
	Renamed           int      `json:"renamed"`
	Merged            int      `json:"merged"`
	CrossAppMerged    int      `json:"crossAppMerged"`
	MergedIntoNamed   int      `json:"mergedIntoNamed"`
	PrunedNonChat     int      `json:"prunedNonChat"`
	PrunedImplausible int      `json:"prunedImplausible"`
	MergedIDs         []string `json:"mergedIds,omitempty"`
}

// CleanupAnonymousPrincipals tidies the People → Unidentified list in
// three passes: prune any Anonymous principal whose sightings are all
// from non-chat apps (Mail.app newsletters, Sentry alerts, GitHub bot,
// etc.), normalize each surviving display name (strip timestamps /
// role badges / message body bleed), and fold duplicates that collapse
// to the same name. See storage.CleanupAnonymousPrincipals for the
// detailed conflict-resolution semantics.
func (a *App) CleanupAnonymousPrincipals() (CleanupReportDTO, error) {
	store, err := a.requireStore()
	if err != nil {
		return CleanupReportDTO{}, err
	}
	rep, err := store.CleanupAnonymousPrincipals(
		context.Background(),
		identity.NormalizeSenderName,
		identity.IsChatApp,
		func(name string) bool { return !identity.PlausibleAutoContactName(identity.NormalizeSenderName(name)) },
	)
	if err != nil {
		return CleanupReportDTO{}, err
	}
	return CleanupReportDTO{
		Scanned:           rep.Scanned,
		Renamed:           rep.Renamed,
		Merged:            rep.Merged,
		CrossAppMerged:    rep.CrossAppMerged,
		MergedIntoNamed:   rep.MergedIntoNamed,
		PrunedNonChat:     rep.PrunedNonChat,
		PrunedImplausible: rep.PrunedImplausible,
		MergedIDs:         rep.MergedIDs,
	}, nil
}

// MergePrincipals folds srcID into dstID, transferring aliases / handles /
// sightings / recent messages with conflict resolution (see
// storage.MergePeople). The source profile is hidden with an Undo receipt. Returns the
// resulting merged dst principal (now enriched with src's data).
//
// Use cases: collapsing duplicate Contacts (e.g. Zhang San bound to one
// row by WeChat and to a separate row by Telegram before the cross-app
// binding existed), or folding an Anonymous bucket into the real Contact
// it turned out to be.
func (a *App) MergePrincipals(srcID, dstID string) (PrincipalDTO, error) {
	store, err := a.requireStore()
	if err != nil {
		return PrincipalDTO{}, err
	}
	mergeID, merged, err := store.MergePeople(context.Background(), dstID, []string{srcID})
	if err != nil {
		return PrincipalDTO{}, err
	}
	dto := principalToDTO(merged)
	dto.MergeID = mergeID
	handles, err := store.ListAppHandlesByPrincipal(context.Background(), merged.ID)
	if err == nil {
		dto.attachHandles(handles)
	} else {
		log.Printf("[People] merge handle enrichment failed: %v", err)
	}
	return dto, nil
}

// PromoteAnonymousToContact converts an auto-created anonymous bucket into a
// named Contact. Use from the onboarding "this is X" affordance.
func (a *App) PromoteAnonymousToContact(principalID, displayName, relationship string, aliases []string) (PrincipalDTO, error) {
	store, err := a.requireStore()
	if err != nil {
		return PrincipalDTO{}, err
	}
	row, err := store.PromoteAnonymousToContact(context.Background(), principalID, displayName, relationship, aliases)
	if err != nil {
		return PrincipalDTO{}, err
	}
	return principalToDTO(row), nil
}

// ── People-page DTOs ──

// PrincipalSightingDTO mirrors storage.PrincipalSightingRow for the frontend.
type PrincipalSightingDTO struct {
	App          string `json:"app"`
	MessageCount int    `json:"messageCount"`
	FirstSeenAt  int64  `json:"firstSeenAt"`
	LastSeenAt   int64  `json:"lastSeenAt"`
}

// PrincipalRecentMessageDTO mirrors storage.PrincipalRecentMessageRow for the
// frontend's "recent messages" drawer on the People detail panel.
type PrincipalRecentMessageDTO struct {
	App     string `json:"app"`
	SeenAt  int64  `json:"seenAt"`
	Preview string `json:"preview"`
}

// PrincipalStatsDTO is the aggregate the Who you know page renders per person.
// Always non-nil — principals with no sightings yet still have a row with
// zero counters so they can be listed.
type PrincipalStatsDTO struct {
	PrincipalID    string                      `json:"principalId"`
	TotalMessages  int                         `json:"totalMessages"`
	FirstSeenAt    int64                       `json:"firstSeenAt"`
	LastSeenAt     int64                       `json:"lastSeenAt"`
	ByApp          []PrincipalSightingDTO      `json:"byApp"`
	RecentMessages []PrincipalRecentMessageDTO `json:"recentMessages"`
}

// PrincipalWithStatsDTO bundles a Principal with its aggregated stats so the
// Who you know page can render the full overview in one round-trip.
type PrincipalWithStatsDTO struct {
	Principal PrincipalDTO      `json:"principal"`
	Stats     PrincipalStatsDTO `json:"stats"`
}

// A snapshot is reusable only under the complete source and privacy revision.
// Publication still takes the same fences as a freshly calculated result.
type peopleStatsSnapshot struct {
	mu       sync.Mutex
	revision peopleReadRevision
	rows     []PrincipalWithStatsDTO
	valid    bool
}

func clonePeopleStats(rows []PrincipalWithStatsDTO) []PrincipalWithStatsDTO {
	out := slices.Clone(rows)
	for i := range out {
		out[i].Principal.Aliases = slices.Clone(rows[i].Principal.Aliases)
		out[i].Principal.AppHandles = slices.Clone(rows[i].Principal.AppHandles)
		out[i].Stats.ByApp = slices.Clone(rows[i].Stats.ByApp)
		out[i].Stats.RecentMessages = slices.Clone(rows[i].Stats.RecentMessages)
	}
	return out
}

func principalStatsToDTO(s storage.PrincipalStatsRow) PrincipalStatsDTO {
	byApp := make([]PrincipalSightingDTO, 0, len(s.ByApp))
	for _, sg := range s.ByApp {
		byApp = append(byApp, PrincipalSightingDTO{
			App:          sg.App,
			MessageCount: sg.MessageCount,
			FirstSeenAt:  sg.FirstSeenAt,
			LastSeenAt:   sg.LastSeenAt,
		})
	}
	recent := make([]PrincipalRecentMessageDTO, 0, len(s.RecentMessages))
	for _, r := range s.RecentMessages {
		recent = append(recent, PrincipalRecentMessageDTO{
			App:     r.App,
			SeenAt:  r.SeenAt,
			Preview: r.Preview,
		})
	}
	return PrincipalStatsDTO{
		PrincipalID:    s.PrincipalID,
		TotalMessages:  s.TotalMessages,
		FirstSeenAt:    s.FirstSeenAt,
		LastSeenAt:     s.LastSeenAt,
		ByApp:          byApp,
		RecentMessages: recent,
	}
}

// GetPrincipalStats returns aggregated sighting stats for a single principal.
// The principal must exist; missing IDs surface a "no rows" error so the
// frontend can show "this person was removed". The same read gates as the
// person detail apply: unreadable apps and previews stay out.
func (a *App) GetPrincipalStats(id string) (PrincipalStatsDTO, error) {
	store, err := a.requireStore()
	if err != nil {
		return PrincipalStatsDTO{}, err
	}
	p, err := store.GetPrincipal(context.Background(), id)
	if err != nil {
		return PrincipalStatsDTO{}, err
	}
	row, err := store.GetPrincipalStats(context.Background(), id)
	if err != nil {
		return PrincipalStatsDTO{}, err
	}
	return principalStatsToDTO(a.peopleStatsReadable(context.Background(), p, row, nil)), nil
}

// ListPrincipalsWithStats returns every principal plus its aggregated stats
// in a single round-trip. The Who you know page loads everyone at once; never-
// sighted principals come back with zero counters so they still show in the
// list. Stats pass the same read gates as the person detail: Closed Eyes,
// source lineage and per-app readability.
func (a *App) ListPrincipalsWithStats() (perfValue []PrincipalWithStatsDTO, perfErr error) {
	defer logPageBinding("ListPrincipalsWithStats", time.Now(), &perfValue)
	ctx := context.Background()
	stamp, err := a.peopleReadStamp(ctx)
	if err != nil {
		return nil, err
	}
	cache := &a.peopleStatsCache
	cache.mu.Lock()
	var rows []PrincipalWithStatsDTO
	if cache.valid && cache.revision == stamp {
		rows = clonePeopleStats(cache.rows)
	}
	cache.mu.Unlock()
	if rows == nil {
		rows, err = a.listPrincipalsWithStats(newPeopleReadGateCache())
		if err != nil {
			return nil, err
		}
	}
	release, err := a.lockPeoplePublication(ctx, stamp)
	if err != nil {
		return nil, err
	}
	defer release()
	cache.mu.Lock()
	cache.revision, cache.rows, cache.valid = stamp, clonePeopleStats(rows), true
	cache.mu.Unlock()
	return rows, nil
}

// listPrincipalsWithStats shares one lineage cache with the calling read, so
// a person sighted in many sessions is evaluated once per read.
func (a *App) listPrincipalsWithStats(cache *peopleReadGateCache) ([]PrincipalWithStatsDTO, error) {
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	principals, err := store.ListPrincipals(context.Background())
	if err != nil {
		return nil, err
	}
	statsMap, err := store.GetAllPrincipalStats(context.Background())
	if err != nil {
		return nil, err
	}
	a.preloadPeopleSessionRows(context.Background(), principals, cache)
	handlesByPrincipal, handlesErr := store.ListAppHandlesByPrincipals(context.Background())
	out := make([]PrincipalWithStatsDTO, 0, len(principals))
	for _, p := range principals {
		dto := principalToDTO(p)
		var handles []storage.AppHandleRow
		if handlesErr != nil {
			handles, err = store.ListAppHandlesByPrincipal(context.Background(), p.ID)
			if err != nil {
				return nil, fmt.Errorf("list handles for %s: %w", p.ID, err)
			}
		} else {
			handles = handlesByPrincipal[p.ID]
		}
		dto.attachHandles(handles)
		stats := statsMap[p.ID]
		if stats.PrincipalID == "" {
			stats.PrincipalID = p.ID
		}
		out = append(out, PrincipalWithStatsDTO{
			Principal: dto,
			Stats:     principalStatsToDTO(a.peopleStatsReadable(context.Background(), p, stats, cache)),
		})
	}
	dtos := make([]*PrincipalDTO, len(out))
	for i := range out {
		dtos[i] = &out[i].Principal
	}
	if err := decorateContactDTOs(context.Background(), store, dtos); err != nil {
		return nil, err
	}
	return out, nil
}
