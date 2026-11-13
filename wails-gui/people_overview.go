package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"
	"wails-gui/internal/pii"

	"wails-gui/internal/identity"
	"wails-gui/storage"
)

type PersonLoop struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Since int64  `json:"since"`
	Title string `json:"title"`
	DueAt int64  `json:"dueAt"`
}
type PersonCard struct {
	ID           string       `json:"id"`
	Kind         string       `json:"kind"`
	Name         string       `json:"name"`
	Relationship string       `json:"relationship"`
	Photo        string       `json:"photo"`
	Loops        []PersonLoop `json:"loops"`
	LastSeenAt   int64        `json:"lastSeenAt"`
	LastSeenApp  string       `json:"lastSeenApp"`
}
type PeopleHeadline struct {
	RepliesOwed        int `json:"repliesOwed"`
	ThingsOwed         int `json:"thingsOwed"`
	PeopleWaitingOnYou int `json:"peopleWaitingOnYou"` // Distinct people with a you_owe commitment.
}
type DuplicateGroup struct {
	ID                string   `json:"id"`
	MemberIDs         []string `json:"memberIds"`
	SuggestedTargetID string   `json:"suggestedTargetId"`
	Reason            string   `json:"reason"`
	App               string   `json:"app"`
}
type PeopleOverview struct {
	Headline    PeopleHeadline    `json:"headline"`
	Open        []PersonCard      `json:"open"`
	Everyone    []PersonCard      `json:"everyone"`
	Duplicates  []DuplicateGroup  `json:"duplicates"`
	KnownCount  int               `json:"knownCount"`
	SelfGuesses []SelfSenderGuess `json:"selfGuesses"`
}

type PersonTogether struct {
	At         int64  `json:"at"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	App        string `json:"app"`
	DurationMs int64  `json:"durationMs"`
	// Direction says who wrote a message: "in" from the person, "out" from
	// you, "" when Isoform does not know.
	Direction string `json:"direction"`
}
type PersonTopic struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type PersonDetail struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Relationship    string           `json:"relationship"`
	Notes           string           `json:"notes"`
	ResearchSummary string           `json:"researchSummary"`
	Loops           []PersonLoop     `json:"loops"`
	Together        []PersonTogether `json:"together"`
	TogetherFailed  bool             `json:"togetherFailed"`
	SharedTopics    []PersonTopic    `json:"sharedTopics"`
	SeenIn          []string         `json:"seenIn"`
	MessageCount    int              `json:"messageCount"`
}

// peopleWorkTopic is one registry work topic with its person members as
// "person:<name>" keys. registryPeopleWorkTopics (people_topics_adapter.go)
// supplies only topics whose complete source lineage passed the registry's
// own publication gates.
type peopleWorkTopic struct {
	ID, Name        string
	Keys            []string
	ActivityIDs     []string
	RequireEvidence bool
}
type peopleWorkTopics interface {
	ReadableWorkTopics(context.Context) ([]peopleWorkTopic, error)
}

type peopleSessionAppNamesFact struct {
	apps  []string
	valid bool
}

// peopleTopicsRegistry returns the shared-topics source for the person
// detail; tests may inject a failing seam via App.peopleTopics.
func (a *App) peopleTopicsRegistry() peopleWorkTopics {
	if a.peopleTopics != nil {
		return a.peopleTopics
	}
	return registryPeopleWorkTopics{a}
}

// peopleReadGateCache memoizes people lineage reads for one overview, detail
// or stats read (one generation): each person's sighting lineage is loaded
// once, each person's profile and metadata gates are evaluated once, each
// merge owner's and each session's gates once — no matter how many of the
// read's paths touch them. A nil cache walks lineage per call, the behavior
// outside these reads.
type peopleReadGateCache struct {
	sightings   map[string][]storage.PrincipalSessionSightingRow
	metadata    map[string]bool                // metadata gate verdict by principal
	profile     map[string]bool                // profile model-read verdict by principal
	owners      map[string]bool                // merge-owner verdict by owner
	sessionRows map[string]*ActivitySessionRow // nil entry = missing session
	sessionOK   map[string]bool                // peopleSessionReadable by session
	depends     map[string]bool                // activityDependencyAllowsModelRead by session
	apps        map[string]bool                // peopleAppReadable verdict by app
	sessionApps map[string]peopleSessionAppNamesFact
	uiState     map[string]string // raw ui_state values by key
	sources     map[string]activitySourceProvenance
	pauseLog    []observationPause
	pauseLogSet bool
	pauseLogErr error
	policy      activityReadPrivacyPolicy
	policySet   bool
	policyErr   error
}

func newPeopleReadGateCache() *peopleReadGateCache {
	return &peopleReadGateCache{
		sightings:   map[string][]storage.PrincipalSessionSightingRow{},
		metadata:    map[string]bool{},
		profile:     map[string]bool{},
		owners:      map[string]bool{},
		sessionRows: map[string]*ActivitySessionRow{},
		sessionOK:   map[string]bool{},
		depends:     map[string]bool{},
		apps:        map[string]bool{},
		sessionApps: map[string]peopleSessionAppNamesFact{},
		uiState:     map[string]string{},
		sources:     map[string]activitySourceProvenance{},
	}
}

// principalSessionSightingsCached loads one person's sighting lineage once
// per read; the probe (tests) fires on each real load.
func (a *App) principalSessionSightingsCached(ctx context.Context, id string, cache *peopleReadGateCache) ([]storage.PrincipalSessionSightingRow, error) {
	if cache != nil {
		if sightings, seen := cache.sightings[id]; seen {
			return sightings, nil
		}
	}
	if a.peopleLineageProbe != nil {
		a.peopleLineageProbe()
	}
	sightings, err := a.principalSessionSightingsForRead(ctx, id)
	if err != nil {
		return nil, err
	}
	if cache != nil {
		cache.sightings[id] = sightings
	}
	return sightings, nil
}

// peopleSessionRow loads a session once per read. Load errors are not
// memoized, so callers keep their existing error behavior.
func (a *App) peopleSessionRow(id string, cache *peopleReadGateCache) (*ActivitySessionRow, error) {
	if a.activity == nil || a.activity.store == nil {
		return nil, nil
	}
	if cache != nil {
		if row, seen := cache.sessionRows[id]; seen {
			return row, nil
		}
	}
	session, err := a.activity.store.GetSessionByID(id)
	if err != nil {
		return nil, err
	}
	if cache != nil {
		cache.sessionRows[id] = session
	}
	return session, nil
}

// peopleSessionReadableCached memoizes the session read gate per session.
func (a *App) peopleSessionReadableCached(ctx context.Context, id string, cache *peopleReadGateCache) bool {
	if cache != nil {
		if ok, seen := cache.sessionOK[id]; seen {
			return ok
		}
	}
	session, err := a.peopleSessionRow(id, cache)
	ok := err == nil && session != nil && a.peopleSessionReadable(ctx, *session, cache)
	if cache != nil {
		cache.sessionOK[id] = ok
	}
	return ok
}

func personNames(name string, aliases []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range append([]string{name}, aliases...) {
		if key := threadNorm(value); key != "" {
			out[key] = true
		}
	}
	return out
}

func (a *App) peopleMergeOwnerReadable(ctx context.Context, owner storage.PeopleMergeOwner, cache *peopleReadGateCache) bool {
	if cache != nil {
		if ok, seen := cache.owners[owner.ID]; seen {
			return ok
		}
	}
	ok := owner.HasReceipt && a.peopleProfileLineageAllowsModelRead(ctx, owner.Profile, owner.Sessions, cache) && a.peopleMetadataLineageReadable(ctx, owner.Sessions, cache)
	if cache != nil {
		cache.owners[owner.ID] = ok
	}
	return ok
}

func peopleNamesAfterMerge(id, name string, aliases []string, owners []storage.PeopleMergeOwner, readable map[string]bool) map[string]bool {
	names := personNames(name, aliases)
	blocked := map[string]bool{}
	for _, owner := range owners {
		if owner.TargetID != id {
			continue
		}
		for key := range personNames(owner.Name, owner.Profile.Aliases) {
			if readable[owner.ID] {
				names[key] = true
			} else {
				blocked[key] = true
			}
		}
	}
	// Copied aliases retain their source's privacy. An unreadable claimant
	// cannot be admitted through the survivor's aggregate alias list.
	for key := range blocked {
		delete(names, key)
	}
	return names
}

func (a *App) personSharedTopics(ctx context.Context, p storage.PrincipalRow, registry peopleWorkTopics, cache *peopleReadGateCache) ([]PersonTopic, error) {
	out := []PersonTopic{}
	if registry == nil || a.IsClosedEyesModeEnabled() || !a.peopleProfileAllowsModelRead(ctx, p, cache) {
		return out, nil
	}
	topics, err := registry.ReadableWorkTopics(ctx)
	if err != nil {
		return nil, err
	}
	names := personNames(p.DisplayName, p.Aliases)
	owners, err := a.store.PeopleMergeOwners(ctx)
	if err != nil {
		return nil, err
	}
	readable := map[string]bool{}
	for _, owner := range owners {
		if owner.TargetID == p.ID {
			readable[owner.ID] = a.peopleMergeOwnerReadable(ctx, owner, cache)
		}
	}
	names = peopleNamesAfterMerge(p.ID, p.DisplayName, p.Aliases, owners, readable)
	seen := map[string]bool{}
	for _, topic := range topics {
		matched := false
		for _, key := range topic.Keys {
			if strings.HasPrefix(key, "person:") && names[strings.TrimPrefix(key, "person:")] {
				matched = true
				break
			}
		}
		if matched && !seen[topic.ID] && (!topic.RequireEvidence || a.peopleTopicSourceShared(ctx, topic, names)) {
			out = append(out, PersonTopic{ID: topic.ID, Name: topic.Name})
			seen[topic.ID] = true
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ListPeopleOverview returns the complete directory, ordered by readable loops.
func (a *App) ListPeopleOverview() (perfValue PeopleOverview, perfErr error) {
	defer logPageBinding("ListPeopleOverview", time.Now(), &perfValue)
	// A write during the read changes the revision it was checked against, and the
	// activity database changes constantly: retry that one case briefly, then fail.
	return derivedRead(a, derivedPeopleOverview, "", func() (PeopleOverview, error) {
		return retryPeopleRead(func() (PeopleOverview, error) {
			return a.listPeopleOverviewOnce(context.Background())
		}, peopleOverviewReadAttempts, peopleOverviewRetryWait)
	}, nil)
}

// retryPeopleRead runs read again only when a concurrent write invalidated it; any
// other error, or success, returns at once. After attempts it returns the last error.
func retryPeopleRead(read func() (PeopleOverview, error), attempts int, wait time.Duration) (PeopleOverview, error) {
	var value PeopleOverview
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		value, err = read()
		if !errors.Is(err, storage.ErrReadRevisionChanged) {
			return value, err
		}
		time.Sleep(wait)
	}
	return PeopleOverview{}, err
}

// peopleOverviewReadAttempts and peopleOverviewRetryWait bound the retry of a read
// that a concurrent write invalidated (2026-11-05: "source database changed before
// publication", rows=0 at 14:25 and 14:38).
const (
	peopleOverviewReadAttempts = 3
	peopleOverviewRetryWait    = 150 * time.Millisecond
)

func (a *App) listPeopleOverviewOnce(ctx context.Context) (PeopleOverview, error) {
	stamp, err := a.peopleReadStamp(ctx)
	if err != nil {
		return PeopleOverview{}, err
	}
	result, err := a.listPeopleOverview(newPeopleReadGateCache())
	if err != nil {
		return PeopleOverview{}, err
	}
	release, err := a.lockPeoplePublication(ctx, stamp)
	if err != nil {
		return PeopleOverview{}, err
	}
	defer release()
	return result, nil
}

// listPeopleOverview reads the directory with one shared lineage cache, so
// every path (stats, loops, cards, duplicates) sees each person and session
// evaluated once per read.
func (a *App) listPeopleOverview(cache *peopleReadGateCache) (PeopleOverview, error) {
	rows, err := a.listPrincipalsWithStats(cache)
	if err != nil {
		return PeopleOverview{}, err
	}
	ctx := context.Background()
	loops, err := a.peopleLoops(ctx, rows, time.Now(), cache)
	if err != nil {
		return PeopleOverview{}, err
	}
	result := PeopleOverview{Open: []PersonCard{}, Everyone: []PersonCard{}, Duplicates: []DuplicateGroup{}, KnownCount: len(rows)}
	for _, row := range rows {
		card := a.personCard(row, loops[row.Principal.ID], cache)
		waiting := false
		for _, loop := range card.Loops {
			switch loop.Kind {
			case "reply_owed":
				result.Headline.RepliesOwed++
			case "you_owe":
				result.Headline.ThingsOwed++
				waiting = true
			}
		}
		if waiting {
			result.Headline.PeopleWaitingOnYou++
		}
		if len(card.Loops) > 0 {
			result.Open = append(result.Open, card)
		} else {
			result.Everyone = append(result.Everyone, card)
		}
	}
	sort.Slice(result.Open, func(i, j int) bool {
		x, y := result.Open[i], result.Open[j]
		xr, xt := personPriority(x)
		yr, yt := personPriority(y)
		if xr != yr {
			return xr < yr
		}
		if xt != yt {
			return xt < yt
		}
		return x.ID < y.ID
	})
	sort.Slice(result.Everyone, func(i, j int) bool {
		if result.Everyone[i].LastSeenAt != result.Everyone[j].LastSeenAt {
			return result.Everyone[i].LastSeenAt > result.Everyone[j].LastSeenAt
		}
		return result.Everyone[i].ID < result.Everyone[j].ID
	})
	result.Duplicates = a.peopleDuplicateGroups(ctx, rows, cache)
	result.SelfGuesses = a.peopleSelfSenderGuesses(ctx, rows, cache)
	return result, nil
}

func (a *App) personCard(row PrincipalWithStatsDTO, loops []PersonLoop, cache *peopleReadGateCache) PersonCard {
	p := row.Principal
	card := PersonCard{ID: p.ID, Kind: p.Kind, Name: p.DisplayName, Relationship: p.Relationship, Photo: p.ContactPhoto, Loops: loops}
	if card.Loops == nil {
		card.Loops = []PersonLoop{}
	}
	if a.IsClosedEyesModeEnabled() || !a.peopleMetadataReadable(context.Background(), p.ID, cache) {
		return card
	}
	for _, app := range row.Stats.ByApp {
		if a.peopleAppReadableCached(context.Background(), app.App, cache) && (app.LastSeenAt > card.LastSeenAt || app.LastSeenAt == card.LastSeenAt && app.App < card.LastSeenApp) {
			card.LastSeenAt, card.LastSeenApp = app.LastSeenAt, app.App
		}
	}
	return card
}

func personPriority(card PersonCard) (int, int64) {
	rank, at := 4, int64(1<<63-1)
	for _, loop := range card.Loops {
		r, t := 4, loop.Since
		switch loop.Kind {
		case "reply_owed":
			r = 0
		case "you_owe":
			r = 1
			t = loop.DueAt
		case "waiting_on_them":
			r = 2
			if loop.DueAt > 0 {
				t = loop.DueAt
			}
		case "reappeared":
			r = 3
		}
		if t == 0 {
			t = 1<<63 - 1
		}
		if r < rank || r == rank && t < at {
			rank, at = r, t
		}
	}
	return rank, at
}

func (a *App) peopleLoops(ctx context.Context, rows []PrincipalWithStatsDTO, now time.Time, cache *peopleReadGateCache) (map[string][]PersonLoop, error) {
	out := map[string][]PersonLoop{}
	policy, err := a.activityReadPrivacyPolicyContextWithPeopleCache(ctx, cache)
	if err != nil {
		return nil, err
	}
	if a.IsClosedEyesModeEnabled() || pausedAt(policy.pauses, now) {
		return out, nil
	}
	people := map[string]PrincipalDTO{}
	for _, row := range rows {
		principal, err := a.store.GetPrincipal(ctx, row.Principal.ID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if a.peopleProfileAllowsModelRead(ctx, principal, cache) && a.peopleMetadataReadable(ctx, principal.ID, cache) {
			people[row.Principal.ID] = row.Principal
		}
	}
	owners, err := a.store.PeopleMergeOwners(ctx)
	if err != nil {
		return nil, err
	}
	survivors := map[string]string{}
	hiddenReadable := map[string]bool{}
	names := map[string]map[string]bool{}
	for _, owner := range owners {
		survivors[owner.ID] = owner.TargetID
		hiddenReadable[owner.ID] = a.peopleMergeOwnerReadable(ctx, owner, cache)
	}
	for id, p := range people {
		names[id] = peopleNamesAfterMerge(id, p.DisplayName, p.Aliases, owners, hiddenReadable)
	}
	for _, kind := range []string{"waiting_on_reply", "person_reappeared"} {
		moments, err := a.store.OpenEngagementMomentsOfKind(ctx, kind)
		if err != nil {
			return nil, err
		}
		for _, m := range moments {
			subject := m.SubjectID
			if target := survivors[subject]; target != "" {
				if !hiddenReadable[subject] {
					continue
				}
				subject = target
			}
			p, exists := people[subject]
			if !exists || m.State != storage.EngagementStatePending || m.ExpiresAt > 0 && m.ExpiresAt <= now.UnixMilli() {
				continue
			}
			loop := PersonLoop{ID: m.ID, Since: m.CreatedAt, Kind: "reappeared"}
			if kind == "waiting_on_reply" {
				if !a.isPermissionEnabledContext(ctx, mailPermissionKey) || !a.peopleAppReadableCached(ctx, "email", cache) || !policy.allows(activitySourceProvenance{StartTime: m.CreatedAt, EndTime: m.CreatedAt + 1, Apps: []string{"com.apple.mail", "Mail"}}) {
					continue
				}
				loop.Kind = "reply_owed"
				// The producer's expiry is exactly seven days after the waiting
				// message. This also recovers the original time for old moments.
				if m.ExpiresAt == 0 {
					continue
				}
				loop.Since = m.ExpiresAt - int64(mailWindowDays*24*time.Hour/time.Millisecond)
				if !policy.allows(activitySourceProvenance{StartTime: loop.Since, EndTime: loop.Since + 1, Apps: []string{"Mail"}}) {
					continue
				}
			} else {
				var params struct {
					Where string `json:"where"`
				}
				if json.Unmarshal([]byte(m.Params), &params) != nil || params.Where != "" && !a.peopleAppReadableCached(ctx, params.Where, cache) || !policy.allows(activitySourceProvenance{StartTime: m.CreatedAt, EndTime: m.CreatedAt + 1}) {
					continue
				}
				principal, err := a.store.GetPrincipal(ctx, p.ID)
				if err != nil {
					return nil, err
				}
				if !a.peopleProfileAllowsModelRead(ctx, principal, cache) || !a.peopleMetadataReadable(ctx, principal.ID, cache) {
					continue
				}
			}
			out[p.ID] = append(out[p.ID], loop)
		}
	}
	outbound, err := a.store.ListOutboundSendsWaiting(ctx)
	if err != nil {
		return nil, err
	}
	for _, send := range outbound {
		subject := send.PrincipalID
		if target := survivors[subject]; target != "" {
			if !hiddenReadable[subject] {
				continue
			}
			subject = target
		}
		if _, exists := people[subject]; !exists {
			continue
		}
		appName := send.Channel
		if appName == "mail" {
			appName = "email"
			if !a.isPermissionEnabledContext(ctx, mailPermissionKey) {
				continue
			}
		}
		if !a.peopleAppReadableCached(ctx, appName, cache) || !policy.allows(activitySourceProvenance{StartTime: send.SentAt, EndTime: send.SentAt + 1, Apps: []string{send.Channel}}) {
			continue
		}
		out[subject] = append(out[subject], PersonLoop{ID: mainChatSendReceiptID(send.MomentID, send.Revision), Kind: "waiting_on_them", Since: send.SentAt, DueAt: send.FollowupAt})
	}
	// Reuse the existing complete Purpose evidence closure. No generation and
	// no new chat context source: these are the same current read gates.
	if a.activity != nil && a.activity.store != nil {
		for _, detail := range a.peoplePurposeLoops(ctx, now) {
			event := detail.Current
			if detail.OpenLoop.Kind != PurposeKindCommitment || (event.LifecycleState != PurposeStateActive && event.LifecycleState != PurposeStateWaiting && event.LifecycleState != PurposeStateCandidate) || event.Title == "" || event.Counterparty == "" {
				continue
			}
			kind := ""
			switch event.Owner {
			case PurposeOwnerUser:
				kind = "you_owe"
			case PurposeOwnerOther:
				kind = "waiting_on_them"
			}
			if kind == "" {
				continue
			}
			for _, row := range rows {
				if _, readable := people[row.Principal.ID]; !readable {
					continue
				}
				if !names[row.Principal.ID][threadNorm(event.Counterparty)] {
					continue
				}
				out[row.Principal.ID] = append(out[row.Principal.ID], PersonLoop{ID: detail.OpenLoop.ID, Kind: kind, Since: detail.OpenLoop.CreatedAt, Title: event.Title, DueAt: personDueAt(event.DueKind, event.DueValue)})
			}
		}
	}
	// Activity's suggested follow-ups that name a person (N48): they show as
	// something you owe them, the same as an added follow-up, until dismissed.
	displayNames := map[string]string{}
	for id, p := range people {
		displayNames[id] = p.DisplayName
	}
	firstCounts := peopleFirstNames(displayNames)
	for _, line := range a.peopleNoticedFollowUps(now) {
		for id, p := range people {
			matched := false
			for _, name := range line.Names {
				matched = matched || noticedNamesPerson(name, names[id], p.DisplayName, firstCounts)
			}
			if !matched || loopWithTitle(out[id], line.Text) {
				continue
			}
			out[id] = append(out[id], PersonLoop{ID: "noticed-" + line.ID, Kind: "you_owe", Since: line.Since, Title: line.Text})
		}
	}
	for id := range out {
		sort.Slice(out[id], func(i, j int) bool {
			x, y := out[id][i], out[id][j]
			xr, xt := personPriority(PersonCard{Loops: []PersonLoop{x}})
			yr, yt := personPriority(PersonCard{Loops: []PersonLoop{y}})
			if xr != yr {
				return xr < yr
			}
			if xt != yt {
				return xt < yt
			}
			return x.ID < y.ID
		})
	}
	return out, nil
}

func personDueAt(kind, value string) int64 {
	if kind != PurposeDueDate && kind != PurposeDueWindow {
		return 0
	}
	if at, ok := parseEngagementDue(value, time.Local); ok {
		return at.UnixMilli()
	}
	return 0
}

func (a *App) peopleDuplicateGroups(ctx context.Context, rows []PrincipalWithStatsDTO, cache *peopleReadGateCache) []DuplicateGroup {
	result := []DuplicateGroup{}
	if a.IsClosedEyesModeEnabled() {
		return result
	}
	type signal struct{ reason, app, value string }
	buckets := map[signal]map[string]string{}
	byID := map[string]PrincipalWithStatsDTO{}
	visibleMessages := map[string]int{}
	for _, row := range rows {
		p := row.Principal
		byID[p.ID] = row
		for _, app := range row.Stats.ByApp {
			if a.peopleAppReadableCached(ctx, app.App, cache) {
				visibleMessages[p.ID] += app.MessageCount
			}
		}
		if p.Kind == storage.PrincipalKindSelf {
			continue
		}
		principal, err := a.store.GetPrincipal(ctx, p.ID)
		if err != nil || (!a.peopleProfileAllowsModelRead(ctx, principal, cache) || !a.peopleMetadataReadable(ctx, principal.ID, cache)) {
			continue
		}
		handles, err := a.peopleHandlesForModel(ctx, a.store, principal)
		if err != nil {
			continue
		}
		for _, h := range handles {
			if !a.peopleAppReadableCached(ctx, h.App, cache) {
				continue
			}
			key := signal{reason: "same_handle", app: h.App, value: strings.TrimSpace(h.HandleID)}
			if h.App == "email" {
				key.reason = "same_email"
				key.value = strings.ToLower(strings.TrimSpace(h.HandleID))
				if key.value == "" {
					key.value = strings.ToLower(strings.TrimSpace(h.DisplayHandle))
				}
			}
			if key.value == "" {
				continue
			}
			if buckets[key] == nil {
				buckets[key] = map[string]string{}
			}
			buckets[key][p.ID] = h.DisplayHandle
		}
	}
	// Profiles the person said are different people are never suggested.
	notSame, err := a.store.PeopleNotSame(ctx)
	if err != nil {
		return result
	}
	for key, members := range buckets {
		if len(members) < 2 {
			continue
		}
		displays := map[string]bool{}
		ids := []string{}
		for id, display := range members {
			ids = append(ids, id)
			displays[display] = true
		}
		if key.reason == "same_handle" && len(displays) < 2 {
			continue
		}
		sort.Strings(ids)
		if notSame(ids) {
			continue
		}
		target := ids[0]
		for _, id := range ids[1:] {
			x, y := byID[id], byID[target]
			if x.Principal.ContactLinked && !y.Principal.ContactLinked || x.Principal.ContactLinked == y.Principal.ContactLinked && visibleMessages[id] > visibleMessages[target] {
				target = id
			}
		}
		hash := sha256.Sum256([]byte(strings.Join(ids, "\x00")))
		result = append(result, DuplicateGroup{ID: hex.EncodeToString(hash[:]), MemberIDs: ids, SuggestedTargetID: target, Reason: key.reason, App: key.app})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		if result[i].Reason != result[j].Reason {
			return result[i].Reason < result[j].Reason
		}
		return result[i].App < result[j].App
	})
	// Multiple accounts may prove the same member set; show one stable group.
	unique := result[:0]
	for _, group := range result {
		if len(unique) == 0 || unique[len(unique)-1].ID != group.ID {
			unique = append(unique, group)
		}
	}
	return unique
}

// GetPersonDetail returns stored directory fields and current readable history.
func (a *App) GetPersonDetail(id string) (perfValue PersonDetail, perfErr error) {
	defer logPageBinding("GetPersonDetail", time.Now(), &perfValue)
	ctx := context.Background()
	stamp, err := a.peopleReadStamp(ctx)
	if err != nil {
		return PersonDetail{}, err
	}
	result, err := a.getPersonDetail(id, newPeopleReadGateCache())
	if err != nil {
		return PersonDetail{}, err
	}
	release, err := a.lockPeoplePublication(ctx, stamp)
	if err != nil {
		return PersonDetail{}, err
	}
	defer release()
	return result, nil
}

// getPersonDetail reads one person with one shared lineage cache, so the
// loops, metadata, together and topics paths share each lineage evaluation.
func (a *App) getPersonDetail(id string, cache *peopleReadGateCache) (PersonDetail, error) {
	store, err := a.requireStore()
	if err != nil {
		return PersonDetail{}, err
	}
	ctx := context.Background()
	p, err := store.GetPrincipal(ctx, id)
	if err != nil {
		return PersonDetail{}, err
	}
	stats, err := store.GetPrincipalStats(ctx, id)
	if err != nil {
		return PersonDetail{}, err
	}
	research, err := store.GetPrincipalResearch(ctx, id)
	if err != nil {
		return PersonDetail{}, err
	}
	detail := PersonDetail{ID: p.ID, Name: p.DisplayName, Relationship: p.Relationship, Notes: p.Notes, ResearchSummary: research.Summary, Loops: []PersonLoop{}, Together: []PersonTogether{}, SharedTopics: []PersonTopic{}, SeenIn: []string{}}
	rows := []PrincipalWithStatsDTO{{Principal: principalToDTO(p)}}
	loops, err := a.peopleLoops(ctx, rows, time.Now(), cache)
	if err != nil {
		return PersonDetail{}, err
	}
	if loops[id] != nil {
		detail.Loops = loops[id]
	}
	if a.IsClosedEyesModeEnabled() {
		return detail, nil
	}
	metadataReadable := a.peopleMetadataReadable(ctx, p.ID, cache)
	for _, sighting := range stats.ByApp {
		if !metadataReadable || !a.peopleAppReadableCached(ctx, sighting.App, cache) {
			continue
		}
		detail.SeenIn = append(detail.SeenIn, sighting.App)
		detail.MessageCount += sighting.MessageCount
	}
	sort.Strings(detail.SeenIn)
	// Latest message per app is metadata only; previews never enter this DTO.
	latest := map[string]int64{}
	for _, message := range stats.RecentMessages {
		if metadataReadable && a.peopleAppReadableCached(ctx, message.App, cache) && message.SeenAt > latest[message.App] {
			latest[message.App] = message.SeenAt
		}
	}
	// Recent messages are resolved senders, so on someone else's card each
	// one came from them.
	direction := "in"
	if p.Kind == storage.PrincipalKindSelf {
		direction = ""
	}
	for app, at := range latest {
		detail.Together = append(detail.Together, PersonTogether{Kind: "message", At: at, App: app, Direction: direction})
	}
	if err := a.personTogether(ctx, p, stats.RecentMessages, &detail, cache); err != nil {
		// A partial evidence read must not look complete or hide the rest of the person.
		detail.Together = []PersonTogether{}
		detail.TogetherFailed = true
	}
	// Shared topics enrich the detail. A topics read failure (for example a
	// registry revision change during capture) leaves them empty instead of
	// taking the rest of the detail down.
	if topics, topicsErr := a.personSharedTopics(ctx, p, a.peopleTopicsRegistry(), cache); topicsErr == nil {
		detail.SharedTopics = topics
	}
	sort.Slice(detail.Together, func(i, j int) bool {
		x, y := detail.Together[i], detail.Together[j]
		if x.At != y.At {
			return x.At > y.At
		}
		if x.Kind != y.Kind {
			return x.Kind < y.Kind
		}
		if x.App != y.App {
			return x.App < y.App
		}
		return x.Title < y.Title
	})
	if len(detail.Together) > 10 {
		detail.Together = detail.Together[:10]
	}
	return detail, nil
}

// personTogether lists the moments that involve the person: meetings they
// attend and Activity moments whose title names them as the other side (a
// message, reply or call with them, something made for them). Being seen in
// the same session, app or hour is not enough; that work stays in Activity.
func (a *App) personTogether(ctx context.Context, p storage.PrincipalRow, recent []storage.PrincipalRecentMessageRow, detail *PersonDetail, cache *peopleReadGateCache) error {
	if a.peopleTogetherProbe != nil {
		if err := a.peopleTogetherProbe(); err != nil {
			return err
		}
	}
	if a.IsClosedEyesModeEnabled() {
		return nil
	}
	if a.isPermissionEnabledContext(ctx, calendarPermissionKey) && a.peopleAppReadableCached(ctx, "calendar", cache) && a.peopleAppReadableCached(ctx, "com.apple.iCal", cache) {
		privacy, err := a.mcpReadPrivacy(ctx)
		if err == nil {
			events, err := a.store.ListCalendarEvents(ctx, 0, time.Now().UnixMilli()+1, false)
			if err != nil {
				return err
			}
			for _, event := range events {
				if event.StartAt > time.Now().UnixMilli() || !privacy.calendarAllowed(event) {
					continue
				}
				for _, attendee := range event.Attendees {
					if attendee.PrincipalID == p.ID {
						title := strings.TrimSpace(personTogetherMeetingID.ReplaceAllString(event.Title, ""))
						detail.Together = append(detail.Together, PersonTogether{Kind: "meeting", At: event.StartAt, Title: title, App: "calendar", DurationMs: event.EndAt - event.StartAt})
						break
					}
				}
			}
		}
	}
	if a.activity == nil || a.activity.store == nil {
		return nil
	}
	sightings, err := a.principalSessionSightingsCached(ctx, p.ID, cache)
	if err != nil {
		return err
	}
	policy, err := a.activityReadPrivacyPolicyContextWithPeopleCache(ctx, cache)
	if err != nil {
		return err
	}
	names := personTogetherNames(p)
	seen := map[string]bool{}
	for _, sighting := range sightings {
		session, err := a.peopleSessionRow(sighting.SessionID, cache)
		if err != nil {
			return err
		}
		if session == nil || !a.peopleSessionReadableCached(ctx, sighting.SessionID, cache) {
			continue
		}
		moments, err := a.activity.store.GetActivitiesInRange(session.StartTime, session.EndTime+1)
		if err != nil {
			return err
		}
		moments = a.filterComposerModelCandidates(a.activity.store, moments)
		for _, moment := range moments {
			if seen[moment.ID] || !personTogetherParticipant(moment.Title, names) || !a.peopleActivityReadable(ctx, moment, policy.pauses) {
				continue
			}
			var runIDs []string
			if json.Unmarshal([]byte(moment.RunIDs), &runIDs) != nil {
				continue
			}
			associated := false
			for _, runID := range runIDs {
				run, err := a.activity.store.GetTimelineRunByIDContext(ctx, runID)
				if err != nil {
					return err
				}
				if run == nil {
					continue
				}
				for _, sessionID := range run.SessionIDs {
					if sessionID == session.ID {
						associated = true
					}
				}
			}
			if !associated {
				continue
			}
			seen[moment.ID] = true
			apps, _ := topicAppTime(moment.ActiveMsByApp)
			app := ""
			if len(apps) > 0 {
				app = apps[0]
			}
			detail.Together = append(detail.Together, PersonTogether{Kind: "activity", At: moment.StartMs, Title: personTogetherTitle(moment.Title, app), App: app, DurationMs: moment.EndMs - moment.StartMs})
		}
	}
	return nil
}

func (a *App) peopleUIStateValue(ctx context.Context, key string, cache *peopleReadGateCache) (string, error) {
	if a == nil || a.store == nil {
		return "", errors.New("the store is not ready")
	}
	if cache != nil {
		if value, found := cache.uiState[key]; found {
			return value, nil
		}
	}
	value, err := a.store.GetUIState(ctx, key)
	if err == nil && cache != nil {
		if cache.uiState == nil {
			cache.uiState = make(map[string]string)
		}
		cache.uiState[key] = value
	}
	return value, err
}

func (a *App) peopleAppReadable(ctx context.Context, app string) bool {
	return a.peopleAppReadableWithCache(ctx, app, nil)
}

func (a *App) peopleAppReadableWithCache(ctx context.Context, app string, cache *peopleReadGateCache) bool {
	if a == nil || a.store == nil || a.IsClosedEyesModeEnabled() || personResearchAppProtected(app) {
		return false
	}
	raw, err := a.peopleUIStateValue(ctx, "sensitive_apps", cache)
	if err != nil {
		return false
	}
	var hidden map[string]string
	if raw != "" && json.Unmarshal([]byte(raw), &hidden) != nil {
		return false
	}
	values := []string{app, identity.AppDisplayName(app)}
	canonical := strings.ToLower(strings.TrimSpace(app))
	switch canonical {
	case "email":
		canonical = "mail"
		values = append(values, "Mail", "com.apple.mail")
	case "imessage":
		canonical = "messages"
		values = append(values, "Messages", "com.apple.MobileSMS")
	case "calendar":
		values = append(values, "Calendar", "com.apple.iCal")
	}
	for _, value := range values {
		if personResearchAppProtected(value) {
			return false
		}
	}
	for _, parser := range activityPrivacyParserApps {
		if strings.EqualFold(parser.Name, canonical) {
			values = append(values, parser.Name, parser.Label)
			values = append(values, parser.Bundles...)
		}
	}
	for _, value := range values {
		for bundle, name := range hidden {
			if bundle != "" && strings.HasPrefix(strings.ToLower(value), strings.ToLower(bundle)) || name != "" && strings.EqualFold(value, name) {
				return false
			}
		}
	}
	return true
}

func (a *App) peopleAppReadableCached(ctx context.Context, app string, cache *peopleReadGateCache) bool {
	if cache == nil {
		return a.peopleAppReadable(ctx, app)
	}
	if ok, seen := cache.apps[app]; seen {
		return ok
	}
	ok := a.peopleAppReadableWithCache(ctx, app, cache)
	cache.apps[app] = ok
	return ok
}

// peopleMetadataReadable evaluates the metadata lineage gate once per person
// per read generation when a cache is shared.
func (a *App) peopleMetadataReadable(ctx context.Context, id string, cache *peopleReadGateCache) bool {
	if a.IsClosedEyesModeEnabled() {
		return false
	}
	if cache != nil {
		if ok, seen := cache.metadata[id]; seen {
			return ok
		}
	}
	sightings, err := a.principalSessionSightingsCached(ctx, id, cache)
	ok := err == nil && a.peopleMetadataLineageReadable(ctx, sightings, cache)
	if cache != nil && err == nil {
		cache.metadata[id] = ok
	}
	return ok
}

// peopleStatsReadable applies the person detail's read gates to stored stats:
// Closed Eyes and the metadata lineage gate zero the row; each app must pass
// peopleAppReadable. Previews are message text, so they also need the
// profile's model-read gate. Counts and first/last times describe only the
// rows that remain. cache shares the read's lineage evaluations.
func (a *App) peopleStatsReadable(ctx context.Context, p storage.PrincipalRow, stats storage.PrincipalStatsRow, cache *peopleReadGateCache) storage.PrincipalStatsRow {
	gated := storage.PrincipalStatsRow{PrincipalID: stats.PrincipalID}
	if !a.peopleMetadataReadable(ctx, p.ID, cache) {
		return gated
	}
	for _, sighting := range stats.ByApp {
		if !a.peopleAppReadableCached(ctx, sighting.App, cache) {
			continue
		}
		gated.ByApp = append(gated.ByApp, sighting)
		gated.TotalMessages += sighting.MessageCount
		if gated.FirstSeenAt == 0 || sighting.FirstSeenAt < gated.FirstSeenAt {
			gated.FirstSeenAt = sighting.FirstSeenAt
		}
		if sighting.LastSeenAt > gated.LastSeenAt {
			gated.LastSeenAt = sighting.LastSeenAt
		}
	}
	if !a.peopleProfileAllowsModelRead(ctx, p, cache) {
		return gated
	}
	for _, message := range stats.RecentMessages {
		if a.peopleAppReadableCached(ctx, message.App, cache) {
			gated.RecentMessages = append(gated.RecentMessages, message)
		}
	}
	return gated
}

func (a *App) peopleMetadataLineageReadable(ctx context.Context, sightings []storage.PrincipalSessionSightingRow, cache *peopleReadGateCache) bool {
	if ctx.Err() != nil || a.IsClosedEyesModeEnabled() {
		return false
	}
	for _, sighting := range sightings {
		if a.activity == nil || a.activity.store == nil {
			return false
		}
		if !a.peopleSessionReadableCached(ctx, sighting.SessionID, cache) {
			return false
		}
	}
	return true
}

func (a *App) peopleSessionReadable(ctx context.Context, session ActivitySessionRow, cache *peopleReadGateCache) bool {
	if a.activity == nil || a.activity.store == nil || !a.activitySessionAllowsModelReadWithInputs(a.activity.store, session, cache) {
		return false
	}
	if cache != nil {
		apps, valid := a.peopleSessionAppNamesForRead(session, cache)
		if !valid {
			return false
		}
		for _, app := range append(append([]string(nil), apps...), session.MeetingApp) {
			if app != "" && !a.peopleAppReadableCached(ctx, app, cache) {
				return false
			}
		}
		return true
	}
	var apps []string
	if json.Unmarshal([]byte(session.AppNames), &apps) != nil {
		return false
	}
	for _, app := range append(apps, session.MeetingApp) {
		if app != "" && !a.peopleAppReadableCached(ctx, app, cache) {
			return false
		}
	}
	return true
}

func (a *App) peopleSessionAppNamesForRead(session ActivitySessionRow, cache *peopleReadGateCache) ([]string, bool) {
	if cache != nil {
		if fact, found := cache.sessionApps[session.AppNames]; found {
			return fact.apps, fact.valid
		}
	}
	var apps []string
	valid := json.Unmarshal([]byte(session.AppNames), &apps) == nil
	if cache != nil {
		if cache.sessionApps == nil {
			cache.sessionApps = make(map[string]peopleSessionAppNamesFact)
		}
		cache.sessionApps[session.AppNames] = peopleSessionAppNamesFact{apps: apps, valid: valid}
	}
	return apps, valid
}

func (a *App) peopleActivityReadable(ctx context.Context, moment ActivityRow, pauses []observationPause) bool {
	if !a.todayChatActivityAllowedContext(ctx, moment, pauses) {
		return false
	}
	apps, _ := topicAppTime(moment.ActiveMsByApp)
	for _, app := range apps {
		if !a.peopleAppReadable(ctx, app) {
			return false
		}
	}
	for _, id := range composeParseStrings(moment.RunIDs) {
		run, err := a.activity.store.GetTimelineRunByIDContext(ctx, id)
		if err != nil || run == nil {
			return false
		}
		for _, sessionID := range run.SessionIDs {
			session, err := a.activity.store.GetSessionByID(sessionID)
			if err != nil || session == nil || !a.peopleSessionReadable(ctx, *session, nil) {
				return false
			}
		}
	}
	return true
}

// Every projection is fenced against local/external DB writes and policy
// changes during collection. Directory fields remain available while paused.
type peopleReadRevision struct {
	Store           storage.ReadRevision
	Activity        storage.ReadRevision
	ActivityPrivacy uint64
	ClosedEyes      bool
	Sensitive       string
	Files           [sha256.Size]byte
}

func (a *App) peopleReadStamp(ctx context.Context) (peopleReadRevision, error) {
	var stamp peopleReadRevision
	store, err := a.requireStore()
	if err != nil {
		return stamp, err
	}
	stamp.Store, err = store.ReadRevision(ctx)
	if err != nil {
		return stamp, err
	}
	stamp.ClosedEyes = a.IsClosedEyesModeEnabled()
	sensitive, err := json.Marshal(pii.GetUserSensitiveApps())
	if err != nil {
		return stamp, err
	}
	stamp.Sensitive = string(sensitive)
	digest := sha256.New()
	consentPath, err := contactsConsentPath()
	if err != nil {
		return stamp, err
	}
	if err := todayChatAdmissionFile(ctx, digest, "contacts-consent", consentPath); err != nil {
		return stamp, err
	}
	if a.activity != nil && a.activity.store != nil {
		stamp.Activity, err = storage.DatabaseRevision(ctx, a.activity.store.db)
		if err != nil {
			return stamp, err
		}
		stamp.ActivityPrivacy = a.activity.store.readPrivacyRevision.Load()
		if stamp.ActivityPrivacy%2 != 0 {
			return stamp, storage.ErrReadRevisionChanged
		}
		if !stamp.ClosedEyes {
			for _, directory := range []func() (string, error){memoryDir, knowledgeDir} {
				dir, err := directory()
				if err != nil {
					return stamp, err
				}
				if err := chatContextDirectoryStamp(ctx, digest, dir); err != nil {
					return stamp, err
				}
			}
		}
	}
	copy(stamp.Files[:], digest.Sum(nil))
	return stamp, nil
}

func (a *App) validatePeopleRead(ctx context.Context, expected peopleReadRevision) error {
	current, err := a.peopleReadStamp(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, current) {
		return storage.ErrReadRevisionChanged
	}
	return nil
}

func (a *App) lockPeoplePublication(ctx context.Context, expected peopleReadRevision) (func(), error) {
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
		releases = nil
	}
	locks := []struct {
		try     func() bool
		release func()
	}{
		{a.closedEyes.mu.TryLock, a.closedEyes.mu.Unlock},
		{memoryMutationMu.TryLock, memoryMutationMu.Unlock},
		{memoryFileLocks.publication.TryLock, memoryFileLocks.publication.Unlock},
		{pii.TryLockSensitiveAppsForPublication, pii.UnlockSensitiveAppsForPublication},
	}
	if a.activity != nil && a.activity.store != nil {
		locks = append(locks, struct {
			try     func() bool
			release func()
		}{a.activity.store.readPrivacyMu.TryLock, a.activity.store.readPrivacyMu.Unlock})
	}
	if err := lockTodayChatPublication(ctx, func() bool {
		for _, lock := range locks {
			if !lock.try() {
				release()
				return false
			}
			releases = append(releases, lock.release)
		}
		return true
	}); err != nil {
		release()
		return nil, err
	}
	if err := a.validatePeopleRead(ctx, expected); err != nil {
		release()
		return nil, err
	}
	unlock, err := a.store.LockReadRevision(ctx, expected.Store)
	if err != nil {
		release()
		return nil, err
	}
	releases = append(releases, unlock)
	if a.activity != nil && a.activity.store != nil {
		unlock, err := storage.LockDatabaseRevision(ctx, a.activity.store.db, expected.Activity)
		if err != nil {
			release()
			return nil, err
		}
		releases = append(releases, unlock)
	}
	return release, nil
}

// loopWithTitle reports whether a person already has an open loop with this title.
func loopWithTitle(loops []PersonLoop, title string) bool {
	for _, loop := range loops {
		if loop.Title != "" && noticedNormalize(loop.Title) == noticedNormalize(title) {
			return true
		}
	}
	return false
}
