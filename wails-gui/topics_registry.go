package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/interests"
)

const (
	TopicKindProject   = "project"
	TopicKindFollowing = "following"

	TopicStatusWaitingOnYou  = "waiting_on_you"
	TopicStatusWaitingOnThem = "waiting_on_them"
	TopicStatusToday         = "today"
	TopicStatusNewStories    = "new_stories"
	TopicStatusMoving        = "moving"

	topicRegistryLimit          = 12
	topicRegistryMovingFor      = 3 * 24 * time.Hour
	topicRegistryPeopleMax      = 3
	topicRegistryActivityLimit  = 1024
	topicRegistryHistoryLimit   = 256
	topicRegistryGoalLimit      = 64
	topicRegistryOutcomeLimit   = 128
	topicRegistrySnapshotBudget = 150 * time.Millisecond
)

// TopicMoment is source text and the time associated with it.
type TopicMoment struct {
	Text string `json:"text"`
	At   string `json:"at,omitempty"`
}

// TopicPeopleLine contains at most three names and the total number of people.
type TopicPeopleLine struct {
	Names []string `json:"names"`
	Count int      `json:"count"`
}

// TopicWhy carries the existing following-topic reason, without generated copy.
type TopicWhy struct {
	LastSeenAt  string `json:"lastSeenAt"` // RFC 3339, when available.
	InterestWhy string `json:"interestWhy,omitempty"`
}

// TopicView is one work or followed topic on Today.
type TopicView struct {
	ID              string           `json:"id"`
	Kind            string           `json:"kind"`
	Name            string           `json:"name"`
	Status          string           `json:"status"`
	Why             *TopicWhy        `json:"why,omitempty"`
	NewStories      int              `json:"newStories,omitempty"`
	LatestFind      *TopicMoment     `json:"latestFind,omitempty"`
	Latest          *TopicMoment     `json:"latest,omitempty"`
	Next            *TopicMoment     `json:"next,omitempty"`
	PeopleLine      *TopicPeopleLine `json:"peopleLine,omitempty"`
	PinnedToSidebar bool             `json:"pinnedToSidebar"`
	Truncated       bool             `json:"truncated,omitempty"`
	SourceCounts    map[string]int   `json:"sourceCounts,omitempty"`
	// ActivityDays are the local days, newest first and at most 14, on which
	// the person worked on the topic. A topic card needs two of them in the
	// last seven days (today-review v1.1.4 #8): one visit isn't a topic.
	ActivityDays []string `json:"activityDays"`
}

// TopicKeySet identifies the local records that make up a topic.
type TopicKeySet struct {
	ThreadKeys []string `json:"threadKeys"`
	GoalIDs    []string `json:"goalIds"`
	InterestID string   `json:"interestId,omitempty"`
}

// TopicRegistry is the local read interface for topic-aware features.
type TopicRegistry interface {
	TopicByID(id string) (TopicView, bool)
	TopicKeys(id string) (TopicKeySet, bool)
}

type topicProject struct {
	Deferred    bool     `json:"deferred,omitempty"`
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	MemberKeys  []string `json:"memberKeys,omitempty"`
	GoalIDs     []string `json:"goalIds,omitempty"`
	ActivityIDs []string `json:"activityIds,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	// PeopleKeys are the topic's person members as "person:<name>" keys — the
	// uncapped member list the PeopleLine display truncates. They re-derive
	// from the same gated observations on every registry build.
	PeopleKeys []string `json:"peopleKeys,omitempty"`
	CreatedAt  string   `json:"createdAt"`
	LastSeenAt string   `json:"lastSeenAt"`
}

type topicGoalObservation struct {
	ID               string
	Title            string
	State            string
	DesiredOutcome   string
	WaitingCondition string
	At               int64
	ActivityIDs      []string
}

type topicMeetingObservation struct {
	ID      string
	Title   string
	URL     string
	StartAt int64
	EndAt   int64
	People  []string
}

type topicActivityObservation struct {
	Row      ActivityRow
	Keys     []string
	KeyNames map[string]string
	People   []string
}

type topicProjectBuild struct {
	project    topicProject
	activities []topicActivityObservation
	goals      []topicGoalObservation
	meetings   []topicMeetingObservation
}

type topicRegistryData struct {
	deferredActivities map[string]bool
	deferredGoals      map[string]bool
	truncated          bool
	existing           []topicProject
	activities         []ActivityRow
	goals              []topicGoalObservation
	meetings           []topicMeetingObservation
	following          UserTopicsView
	finds              []TopicFind
	findsRanAt         string
	sidebarPins        map[string]bool
	withheld           map[string]bool
	now                time.Time
	closedEyes         bool
	selfName           string
	waitingOnThem      map[string]int64
	outcome            func(ActivityRow) *ActivityThreadOutcome
}

type topicRegistryResult struct {
	projects  []topicProject
	views     []TopicView
	keys      map[string]TopicKeySet
	canonical map[string]string
	chat      []topicChatSnapshot
}

var (
	errTopicNotFound        = errors.New("topic_not_found")
	errTopicIDRequired      = errors.New("topic_id_required")
	errTopicRegistryChanged = errors.New("topic_registry_changed")
)

func (a *App) topicRegistry() TopicRegistry {
	return a.topicsSvc()
}

func buildTopicRegistry(in topicRegistryData) topicRegistryResult {
	result := topicRegistryResult{views: []TopicView{}, keys: map[string]TopicKeySet{}, canonical: map[string]string{}}
	if in.closedEyes {
		result.views = topicFollowingViews(in)
		for _, view := range result.views {
			result.keys[view.ID] = topicFollowingKeys(in.following, view.ID)
		}
		sortTopicViews(result.views)
		return result
	}

	builds := make([]topicProjectBuild, 0, len(in.existing))
	for _, stored := range in.existing {
		if topicProjectWithheld(stored, in.withheld) {
			stored = topicProjectReservation(stored)
		}
		stored = migrateTopicDisplayName(stored)
		stored.MemberKeys = append([]string(nil), stored.MemberKeys...)
		stored.GoalIDs = append([]string(nil), stored.GoalIDs...)
		stored.ActivityIDs = append([]string(nil), stored.ActivityIDs...)
		stored.Aliases = append([]string(nil), stored.Aliases...)
		// Person membership re-derives from this build's readable sources.
		stored.PeopleKeys = nil
		builds = append(builds, topicProjectBuild{project: stored})
	}
	sort.SliceStable(builds, func(i, j int) bool {
		if builds[i].project.CreatedAt != builds[j].project.CreatedAt {
			return builds[i].project.CreatedAt < builds[j].project.CreatedAt
		}
		return builds[i].project.ID < builds[j].project.ID
	})

	activities := map[string]topicActivityObservation{}
	for _, row := range in.activities {
		observation := newTopicActivityObservation(row, in.selfName)
		activities[row.ID] = observation
		if len(observation.Keys) == 0 {
			continue
		}
		var target int
		builds, target = attachTopicProject(builds, observation.Keys, "", topicActivityProjectName(observation), observation.Keys[0], in.now)
		builds[target].addActivity(observation)
	}

	for _, goal := range in.goals {
		// Unsafe subjects cannot allocate projects, including after saved
		// identity has been retired to an opaque id/alias reservation.
		if topicHumanName(goal.Title) == "" {
			continue
		}
		keys := map[string]bool{}
		for _, activityID := range goal.ActivityIDs {
			for _, key := range activities[activityID].Keys {
				keys[key] = true
			}
		}
		keyList := sortedTopicKeys(keys)
		seed := "goal:" + goal.ID
		var target int
		builds, target = attachTopicProject(builds, keyList, goal.ID, goal.Title, seed, in.now)
		builds[target].addGoal(goal)
		for _, activityID := range goal.ActivityIDs {
			if observation, ok := activities[activityID]; ok {
				builds[target].addActivity(observation)
			}
		}
	}

	for _, meeting := range in.meetings {
		keys := topicMeetingKeys(meeting, in.selfName)
		for i := range builds {
			if !topicProjectHasAnyKey(builds[i].project, keys) && !topicNameWithin(meeting.Title, builds[i].project.Name) {
				continue
			}
			builds[i].addMeeting(meeting)
		}
	}

	mappedInterests := map[string]bool{}
	followingProjects := topicFollowingProjectIDs(builds, in.following)
	for _, build := range builds {
		if build.project.Name == "" || topicProjectWithheld(build.project, in.withheld) {
			result.projects = append(result.projects, build.project)
			continue
		}
		var matched []UserTopic
		for i := range in.following.Topics {
			followed := in.following.Topics[i]
			if mappedInterests[followed.ID] || followingProjects[followed.ID] != build.project.ID {
				continue
			}
			mappedInterests[followed.ID] = true
			matched = append(matched, followed)
		}
		build.project.PeopleKeys = topicProjectPeopleKeys(build, in.selfName)
		view := topicProjectView(build, matched, in)
		view.Truncated = in.truncated
		result.projects = append(result.projects, build.project)
		result.views = append(result.views, view)
		interestID := ""
		if len(matched) > 0 {
			interestID = matched[0].ID
		}
		goalKeys := []string{}
		for _, goal := range build.goals {
			goalKeys = append(goalKeys, goal.ID)
		}
		sort.Strings(goalKeys)
		result.keys[build.project.ID] = TopicKeySet{
			ThreadKeys: append([]string{}, build.project.MemberKeys...),
			GoalIDs:    goalKeys,
			InterestID: interestID,
		}
		for _, followed := range matched {
			keys := result.keys[build.project.ID]
			keys.InterestID = followed.ID
			result.keys[followed.ID] = keys
			result.canonical[followed.ID] = build.project.ID
		}
		for _, alias := range build.project.Aliases {
			result.keys[alias] = result.keys[build.project.ID]
			result.canonical[alias] = build.project.ID
		}
	}
	for _, view := range topicFollowingViews(in) {
		if mappedInterests[view.ID] {
			continue
		}
		result.views = append(result.views, view)
		result.keys[view.ID] = topicFollowingKeys(in.following, view.ID)
	}
	sort.SliceStable(result.projects, func(i, j int) bool { return result.projects[i].ID < result.projects[j].ID })
	sortTopicViews(result.views)
	return result
}

func attachTopicProject(builds []topicProjectBuild, keys []string, goalID, name, seed string, now time.Time) ([]topicProjectBuild, int) {
	matches := []int{}
	for i := range builds {
		if topicProjectHasAnyKey(builds[i].project, keys) || goalID != "" && topicStringIn(builds[i].project.GoalIDs, goalID) {
			matches = append(matches, i)
		}
	}
	if len(matches) == 0 {
		project := topicProject{ID: uniqueTopicProjectID(seed, builds), Name: topicHumanName(name), CreatedAt: topicRegistryTime(now.UnixMilli())}
		builds = append(builds, topicProjectBuild{project: project})
		return builds, len(builds) - 1
	}
	target := matches[0]
	for i := len(matches) - 1; i >= 1; i-- {
		index := matches[i]
		builds[target].merge(builds[index])
		builds = append(builds[:index], builds[index+1:]...)
	}
	return builds, target
}

func uniqueTopicProjectID(seed string, builds []topicProjectBuild) string {
	id := topicProjectID(seed)
	for n := 2; ; n++ {
		taken := false
		for _, build := range builds {
			if build.project.ID == id || topicStringIn(build.project.Aliases, id) {
				taken = true
				break
			}
		}
		if !taken {
			return id
		}
		id = topicProjectID(fmt.Sprintf("%s#%d", seed, n))
	}
}

func topicProjectID(seed string) string {
	sum := sha256.Sum256([]byte("topic-project\x00" + seed))
	return "project_" + hex.EncodeToString(sum[:8])
}

func (b *topicProjectBuild) merge(other topicProjectBuild) {
	b.project.Deferred = b.project.Deferred || other.project.Deferred
	b.project.Aliases = append(b.project.Aliases, other.project.ID)
	b.project.Aliases = append(b.project.Aliases, other.project.Aliases...)
	sort.Strings(b.project.Aliases)
	if b.project.CreatedAt == "" || other.project.CreatedAt != "" && other.project.CreatedAt < b.project.CreatedAt {
		b.project.CreatedAt = other.project.CreatedAt
	}
	if b.project.Name == "" {
		b.project.Name = other.project.Name
	}
	for _, key := range other.project.MemberKeys {
		b.addKey(key)
	}
	for _, goalID := range other.project.GoalIDs {
		b.addGoalID(goalID)
	}
	for _, activityID := range other.project.ActivityIDs {
		b.addActivityID(activityID)
	}
	b.project.LastSeenAt = newerTopicTime(b.project.LastSeenAt, other.project.LastSeenAt)
	for _, activity := range other.activities {
		b.addActivity(activity)
	}
	for _, goal := range other.goals {
		b.addGoal(goal)
	}
	for _, meeting := range other.meetings {
		b.addMeeting(meeting)
	}
}

func (b *topicProjectBuild) addKey(key string) {
	if !topicWorkKey(key) || topicStringIn(b.project.MemberKeys, key) {
		return
	}
	b.project.MemberKeys = append(b.project.MemberKeys, key)
	sort.Strings(b.project.MemberKeys)
}

func (b *topicProjectBuild) addGoalID(goalID string) {
	if goalID == "" || topicStringIn(b.project.GoalIDs, goalID) {
		return
	}
	b.project.GoalIDs = append(b.project.GoalIDs, goalID)
	sort.Strings(b.project.GoalIDs)
}

func (b *topicProjectBuild) addActivityID(activityID string) {
	if activityID == "" || topicStringIn(b.project.ActivityIDs, activityID) {
		return
	}
	b.project.ActivityIDs = append(b.project.ActivityIDs, activityID)
	sort.Strings(b.project.ActivityIDs)
}

func (b *topicProjectBuild) addActivity(activity topicActivityObservation) {
	b.addActivityID(activity.Row.ID)
	for _, key := range activity.Keys {
		b.addKey(key)
	}
	for _, existing := range b.activities {
		if existing.Row.ID == activity.Row.ID {
			return
		}
	}
	b.activities = append(b.activities, activity)
	b.project.LastSeenAt = newerTopicTime(b.project.LastSeenAt, topicRegistryTime(topicActivityAt(activity.Row)))
}

func (b *topicProjectBuild) addGoal(goal topicGoalObservation) {
	b.addGoalID(goal.ID)
	for _, existing := range b.goals {
		if existing.ID == goal.ID {
			return
		}
	}
	b.goals = append(b.goals, goal)
	b.project.LastSeenAt = newerTopicTime(b.project.LastSeenAt, topicRegistryTime(goal.At))
}

func (b *topicProjectBuild) addMeeting(meeting topicMeetingObservation) {
	for _, existing := range b.meetings {
		if existing.ID == meeting.ID {
			return
		}
	}
	b.meetings = append(b.meetings, meeting)
}

func topicProjectView(build topicProjectBuild, matched []UserTopic, in topicRegistryData) TopicView {
	project := build.project
	view := TopicView{
		ID: project.ID, Kind: TopicKindProject, Name: project.Name,
		PinnedToSidebar: topicProjectPinned(project, in.sidebarPins), SourceCounts: map[string]int{},
	}
	if len(build.activities) > 0 {
		view.SourceCounts["activity"] = len(build.activities)
	}
	if len(build.goals) > 0 {
		view.SourceCounts["goal"] = len(build.goals)
	}
	if len(build.meetings) > 0 {
		view.SourceCounts["meeting"] = len(build.meetings)
	}
	for _, followed := range matched {
		view.PinnedToSidebar = view.PinnedToSidebar || in.sidebarPins[followed.ID]
		view.SourceCounts["interest"]++
	}

	activities := append([]topicActivityObservation(nil), build.activities...)
	sort.SliceStable(activities, func(i, j int) bool {
		left, right := topicActivityAt(activities[i].Row), topicActivityAt(activities[j].Row)
		if left != right {
			return left > right
		}
		return activities[i].Row.ID < activities[j].Row.ID
	})
	waiting := false
	today := false
	moving := false
	people := []string{}
	var latestActivityAt int64
	activityDays := []string{}
	for _, latest := range activities {
		var outcome *ActivityThreadOutcome
		if in.outcome != nil {
			outcome = in.outcome(latest.Row)
		}
		moment := topicActivityLatest(latest.Row, outcome)
		if moment == nil {
			continue
		}
		latestActivityAt = topicActivityAt(latest.Row)
		view.Latest = moment
		view.Next = topicActivityNext(latest.Row, outcome)
		waiting = topicOutcomeWaiting(outcome)
		people = append(people, latest.People...)
		break
	}
	for _, activity := range activities {
		var outcome *ActivityThreadOutcome
		if in.outcome != nil {
			outcome = in.outcome(activity.Row)
		}
		if topicActivityIsHousekeeping(activity.Row, outcome) {
			continue
		}
		if topicOutcomeWaiting(outcome) {
			waiting = true
			if view.Next == nil {
				view.Next = topicActivityNext(activity.Row, outcome)
			}
		}
		at := topicActivityAt(activity.Row)
		if topicRegistrySameDay(at, in.now) {
			today = true
		}
		activityDays = topicAddActivityDay(activityDays, at, in.now)
		people = append(people, activity.People...)
	}
	if latestActivityAt > 0 && !time.UnixMilli(latestActivityAt).After(in.now) && in.now.Sub(time.UnixMilli(latestActivityAt)) <= topicRegistryMovingFor {
		moving = true
	}

	if view.Latest == nil && len(build.goals) > 0 {
		goals := append([]topicGoalObservation(nil), build.goals...)
		sort.SliceStable(goals, func(i, j int) bool {
			if goals[i].At != goals[j].At {
				return goals[i].At > goals[j].At
			}
			return goals[i].ID < goals[j].ID
		})
		latest := goals[0]
		view.Latest = &TopicMoment{Text: latest.Title, At: topicRegistryTime(latest.At)}
	}
	for _, goal := range build.goals {
		if goal.State == PurposeStateWaiting {
			waiting = true
			if text := strings.TrimSpace(goal.WaitingCondition); text != "" {
				view.Next = newerTopicMoment(view.Next, &TopicMoment{Text: text, At: topicRegistryTime(goal.At)})
			}
			continue
		}
		if goal.State == PurposeStateActive && !topicNamesEqual(goal.DesiredOutcome, goal.Title) {
			view.Next = newerTopicMoment(view.Next, &TopicMoment{Text: strings.TrimSpace(goal.DesiredOutcome), At: topicRegistryTime(goal.At)})
		}
	}

	for _, meeting := range build.meetings {
		today = true
		people = append(people, meeting.People...)
		if meeting.StartAt > in.now.UnixMilli() {
			candidate := &TopicMoment{Text: meeting.Title, At: topicRegistryTime(meeting.StartAt)}
			if view.Next == nil || topicMomentTime(candidate).Before(topicMomentTime(view.Next)) {
				view.Next = candidate
			}
			continue
		}
		at := meeting.EndAt
		if at == 0 {
			at = meeting.StartAt
		}
		activityDays = topicAddActivityDay(activityDays, at, in.now)
		view.Latest = newerTopicMoment(view.Latest, &TopicMoment{Text: meeting.Title, At: topicRegistryTime(at)})
	}
	for _, followed := range matched {
		view.LatestFind = newerTopicMoment(view.LatestFind, topicLatestFind(followed, in.finds, in.findsRanAt))
		finds := topicNewFinds(followed, in.finds)
		if len(finds) > 0 {
			view.SourceCounts["find"] += len(finds)
			latest := finds[0]
			text := strings.TrimSpace(latest.WhatsNew)
			if text == "" {
				text = strings.TrimSpace(latest.Title)
			}
			view.Latest = newerTopicMoment(view.Latest, &TopicMoment{Text: text, At: topicRegistryTime(topicFindAt(latest, in.findsRanAt))})
		}
	}
	view.PeopleLine = topicPeopleLine(people, in.selfName)
	sort.Sort(sort.Reverse(sort.StringSlice(activityDays)))
	view.ActivityDays = activityDays
	waitingOnThem := false
	for _, key := range project.PeopleKeys {
		waitingOnThem = waitingOnThem || in.waitingOnThem[key] > 0
	}
	if waiting {
		view.Status = TopicStatusWaitingOnYou
	} else if waitingOnThem {
		view.Status = TopicStatusWaitingOnThem
	} else if today {
		view.Status = TopicStatusToday
	} else if moving {
		view.Status = TopicStatusMoving
	}
	if len(view.SourceCounts) == 0 {
		view.SourceCounts = nil
	}
	return view
}

func topicActivityLatest(row ActivityRow, outcome *ActivityThreadOutcome) *TopicMoment {
	at := topicRegistryTime(topicActivityAt(row))
	if topicActivityIsHousekeeping(row, outcome) {
		return nil
	}
	if outcome != nil && strings.TrimSpace(outcome.Summary) != "" {
		text := strings.TrimSpace(outcome.Summary)
		return &TopicMoment{Text: text, At: at}
	}
	for _, text := range []string{row.LatestChange, row.Description, row.Title} {
		if clean := strings.TrimSpace(text); clean != "" {
			return &TopicMoment{Text: clean, At: at}
		}
	}
	return nil
}

func topicActivityIsHousekeeping(row ActivityRow, outcome *ActivityThreadOutcome) bool {
	if outcome != nil && strings.TrimSpace(outcome.Summary) != "" {
		return topicActivityHousekeeping(outcome.Summary)
	}
	for _, text := range []string{row.LatestChange, row.Description, row.Title} {
		if strings.TrimSpace(text) != "" {
			return topicActivityHousekeeping(text)
		}
	}
	return false
}

// topicActivityHousekeeping recognizes only explicit routine actions. A missed
// chore is preferable to hiding an ambiguous update that may matter.
func topicActivityHousekeeping(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range []string{
		"emptied the trash", "emptying the trash", "empty trash",
		"closed a window", "closed the window", "closing a window", "closing the window",
		"opened system settings", "changed system settings", "updated system settings",
		"moved a file in finder", "moved files in finder", "moving files in finder",
		"renamed a file in finder", "renamed files in finder", "renaming files in finder",
	} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func topicActivityNext(row ActivityRow, outcome *ActivityThreadOutcome) *TopicMoment {
	if outcome == nil {
		return nil
	}
	for _, text := range []string{outcome.NextStep, outcome.Question} {
		if clean := strings.TrimSpace(text); clean != "" {
			return &TopicMoment{Text: clean, At: topicRegistryTime(topicActivityAt(row))}
		}
	}
	return nil
}

func topicOutcomeWaiting(outcome *ActivityThreadOutcome) bool {
	if outcome == nil || outcome.Kind == "assistant" && outcome.UserActed == "observed" {
		return false
	}
	return strings.TrimSpace(outcome.Question) != "" || threadOutcomeUnfinished(outcome)
}

func topicFollowingViews(in topicRegistryData) []TopicView {
	views := []TopicView{}
	for _, followed := range in.following.Topics {
		if strings.TrimSpace(followed.Why) == "" || topicHumanName(followed.Label) == "" {
			continue
		}
		view := TopicView{
			ID: followed.ID, Kind: TopicKindFollowing, Name: topicHumanName(followed.Label),
			PinnedToSidebar: in.sidebarPins[followed.ID], SourceCounts: map[string]int{}, ActivityDays: []string{},
		}
		// Closed Eyes keeps its existing stored-label projection; observed
		// timestamps and explanations stay withheld, as in UserTopicsView.
		if !in.closedEyes {
			view.Why = &TopicWhy{LastSeenAt: followed.LastSeenAt, InterestWhy: followed.Why}
		}
		view.LatestFind = topicLatestFind(followed, in.finds, in.findsRanAt)
		finds := topicNewFinds(followed, in.finds)
		if len(finds) > 0 {
			view.SourceCounts["find"] = len(finds)
			latest := finds[0]
			text := strings.TrimSpace(latest.WhatsNew)
			if text == "" {
				text = strings.TrimSpace(latest.Title)
			}
			view.Latest = &TopicMoment{Text: text, At: topicRegistryTime(topicFindAt(latest, in.findsRanAt))}
		}
		if len(finds) > 0 {
			view.Status = TopicStatusNewStories
			view.NewStories = len(finds)
		}
		if len(view.SourceCounts) == 0 {
			view.SourceCounts = nil
		}
		views = append(views, view)
	}
	return views
}

func topicFollowingKeys(following UserTopicsView, id string) TopicKeySet {
	keys := TopicKeySet{ThreadKeys: []string{}, GoalIDs: []string{}}
	if !following.Interests {
		return keys
	}
	for _, topic := range following.Topics {
		if topic.ID == id {
			keys.InterestID = id
			break
		}
	}
	return keys
}

// topicLatestFind is independent of unread counts: opening a story does not
// erase the latest headline from its topic card.
func topicLatestFind(topic UserTopic, finds []TopicFind, ranAt string) *TopicMoment {
	var latest *TopicMoment
	for _, find := range finds {
		if find.TopicID != topic.ID || strings.TrimSpace(find.Title) == "" {
			continue
		}
		latest = newerTopicMoment(latest, &TopicMoment{Text: strings.TrimSpace(find.Title), At: topicRegistryTime(topicFindAt(find, ranAt))})
	}
	return latest
}

func topicNewFinds(topic UserTopic, finds []TopicFind) []TopicFind {
	lastSeen := topicRegistryParseTime(topic.LastSeenAt)
	out := []TopicFind{}
	for _, find := range finds {
		if find.TopicID != topic.ID || find.Opened {
			continue
		}
		if find.PublishedAt != "" && !lastSeen.IsZero() {
			published, err := time.ParseInLocation("2006-01-02", find.PublishedAt, time.Local)
			if err == nil && published.In(time.Local).Format("2006-01-02") < lastSeen.In(time.Local).Format("2006-01-02") {
				continue
			}
		}
		out = append(out, find)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := topicFindAt(out[i], ""), topicFindAt(out[j], "")
		if left != right {
			return left > right
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func topicFindAt(find TopicFind, ranAt string) int64 {
	if find.PublishedAt != "" {
		if at, err := time.ParseInLocation("2006-01-02", find.PublishedAt, time.Local); err == nil {
			return at.UnixMilli()
		}
	}
	return topicRegistryParseTime(ranAt).UnixMilli()
}

func newTopicActivityObservation(row ActivityRow, selfName string) topicActivityObservation {
	moment := threadMomentFromRow(row)
	// Container names and masked fragments cannot join otherwise unrelated
	// projects through a shared member key. Keep only admitted real subjects.
	entities := make([]Entity, 0, len(moment.Entities))
	for _, entity := range moment.Entities {
		switch entity.Type {
		case entityRepo, entityProject, entityPR, entityChannel:
			if topicHumanName(entity.Name) == "" {
				continue
			}
		}
		entities = append(entities, entity)
	}
	moment.Entities = entities
	selfNames := threadSelfNames(selfName)
	keys := threadSureKeys(moment, selfNames)
	observation := topicActivityObservation{Row: row, KeyNames: map[string]string{}}
	for key := range keys {
		if topicWorkKey(key) {
			observation.Keys = append(observation.Keys, key)
		}
	}
	sort.Strings(observation.Keys)
	for _, entity := range moment.Entities {
		single := threadSureKeys(threadMoment{Entities: []Entity{entity}, Apps: moment.Apps}, selfNames)
		for key := range single {
			if topicWorkKey(key) && observation.KeyNames[key] == "" {
				observation.KeyNames[key] = strings.TrimSpace(entity.Name)
			}
		}
		if entity.Type == entityPerson {
			name := strings.TrimSpace(entity.Name)
			if name != "" && !selfNames[threadNorm(name)] && !threadToolNames[threadNorm(name)] {
				observation.People = append(observation.People, name)
			}
		}
	}
	return observation
}

func topicActivityProjectName(activity topicActivityObservation) string {
	keys := append([]string(nil), activity.Keys...)
	sort.SliceStable(keys, func(i, j int) bool {
		left, right := topicWorkKeyRank(keys[i]), topicWorkKeyRank(keys[j])
		if left != right {
			return left < right
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys {
		if name := activity.KeyNames[key]; name != "" {
			return topicActivityDisplayName(activity, name)
		}
	}
	if name := strings.TrimSpace(activity.Row.Title); name != "" {
		return topicActivityDisplayName(activity, name)
	}
	if len(keys) > 0 {
		return topicActivityDisplayName(activity, keys[0])
	}
	return ""
}

func topicWorkKey(key string) bool {
	prefix, _, _ := strings.Cut(key, ":")
	switch prefix {
	case "pr", "obj", "channel":
		return true
	default:
		return false
	}
}

func topicWorkKeyRank(key string) int {
	prefix, _, _ := strings.Cut(key, ":")
	switch prefix {
	case "obj":
		return 0
	case "pr":
		return 1
	case "channel":
		return 2
	default:
		return 3
	}
}

func topicMeetingKeys(meeting topicMeetingObservation, selfName string) []string {
	entities := extractEntities(Observation{Title: meeting.Title, URL: meeting.URL})
	keys := threadSureKeys(threadMoment{Entities: entities}, threadSelfNames(selfName))
	out := map[string]bool{}
	for key := range keys {
		if topicWorkKey(key) {
			out[key] = true
		}
	}
	return sortedTopicKeys(out)
}

func topicProjectHasAnyKey(project topicProject, keys []string) bool {
	for _, key := range keys {
		if topicStringIn(project.MemberKeys, key) {
			return true
		}
	}
	return false
}

// topicProjectPeopleKeys is the uncapped person member list of one project,
// as "person:<name>" keys in the form threadSureKeys gives person entities.
// It is built from the same gated activity and meeting people as the view's
// PeopleLine, which truncates to three display names.
func topicProjectPeopleKeys(build topicProjectBuild, selfName string) []string {
	people := []string{}
	for _, activity := range build.activities {
		people = append(people, activity.People...)
	}
	for _, meeting := range build.meetings {
		people = append(people, meeting.People...)
	}
	selfNames := threadSelfNames(selfName)
	unique := map[string]bool{}
	out := []string{}
	for _, name := range people {
		key := threadNorm(name)
		if key == "" || selfNames[key] || threadToolNames[key] || unique[key] {
			continue
		}
		unique[key] = true
		out = append(out, "person:"+key)
	}
	sort.Strings(out)
	return out
}

func topicPeopleLine(names []string, selfName string) *TopicPeopleLine {
	selfNames := threadSelfNames(selfName)
	unique := map[string]string{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		key := threadNorm(name)
		if key == "" || selfNames[key] || threadToolNames[key] {
			continue
		}
		if _, ok := unique[key]; !ok {
			unique[key] = name
		}
	}
	ordered := make([]string, 0, len(unique))
	for _, name := range unique {
		ordered = append(ordered, name)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return strings.ToLower(ordered[i]) < strings.ToLower(ordered[j]) })
	count := len(ordered)
	if count == 0 {
		return nil
	}
	if len(ordered) > topicRegistryPeopleMax {
		ordered = ordered[:topicRegistryPeopleMax]
	}
	return &TopicPeopleLine{Names: append([]string{}, ordered...), Count: count}
}

func sortTopicViews(views []TopicView) {
	sort.SliceStable(views, func(i, j int) bool {
		left, right := topicStatusRank(views[i].Status), topicStatusRank(views[j].Status)
		if left != right {
			return left < right
		}
		leftAt, rightAt := topicViewLatestTime(views[i]), topicViewLatestTime(views[j])
		if !leftAt.Equal(rightAt) {
			return leftAt.After(rightAt)
		}
		leftName, rightName := strings.ToLower(views[i].Name), strings.ToLower(views[j].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return views[i].ID < views[j].ID
	})
}

func topicStatusRank(status string) int {
	switch status {
	case TopicStatusWaitingOnYou:
		return 0
	case TopicStatusWaitingOnThem:
		return 1
	case TopicStatusToday:
		return 2
	case TopicStatusNewStories:
		return 3
	case TopicStatusMoving:
		return 4
	default:
		return 5
	}
}

func topicViewLatestTime(view TopicView) time.Time {
	if view.Latest != nil {
		return topicMomentTime(view.Latest)
	}
	return time.Time{}
}

func newerTopicMoment(current, candidate *TopicMoment) *TopicMoment {
	if candidate == nil || strings.TrimSpace(candidate.Text) == "" {
		return current
	}
	if current == nil || topicMomentTime(candidate).After(topicMomentTime(current)) {
		return candidate
	}
	return current
}

func topicMomentTime(moment *TopicMoment) time.Time {
	if moment == nil {
		return time.Time{}
	}
	return topicRegistryParseTime(moment.At)
}

func topicActivityAt(row ActivityRow) int64 {
	if row.EndMs > 0 {
		return row.EndMs
	}
	if row.UpdatedAt > 0 {
		return row.UpdatedAt
	}
	return row.StartMs
}

func topicRegistrySameDay(at int64, now time.Time) bool {
	if at <= 0 {
		return false
	}
	day := time.UnixMilli(at).In(time.Local)
	localNow := now.In(time.Local)
	return day.Year() == localNow.Year() && day.YearDay() == localNow.YearDay()
}

func topicRegistryTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

func topicRegistryParseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return at
}

func newerTopicTime(left, right string) string {
	if topicRegistryParseTime(right).After(topicRegistryParseTime(left)) {
		return right
	}
	return left
}

func topicNamesEqual(left, right string) bool {
	return topicNameKey(left) != "" && topicNameKey(left) == topicNameKey(right)
}

func topicNameKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func topicNameWithin(text, name string) bool {
	text, name = topicNameKey(text), topicNameKey(name)
	return len(name) >= 3 && strings.Contains(" "+text+" ", " "+name+" ")
}

func topicInterestName(topic UserTopic) string {
	if strings.TrimSpace(topic.Original) != "" {
		return topic.Original
	}
	return topic.Label
}

func sortedTopicKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func topicStringIn(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (a *App) topicRegistrySelfName(ctx context.Context) string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	name := a.cachedDisplayName
	a.mu.Unlock()
	if name != "" || a.store == nil {
		return name
	}
	rows, err := a.store.ListPrincipalsByKind(ctx, "self")
	if err == nil && len(rows) > 0 {
		return rows[0].DisplayName
	}
	return ""
}

func (s *topicsService) registryViews(ctx context.Context) (topicRegistryResult, error) {
	if s.registryWorkerRunning() {
		if s.app.IsClosedEyesModeEnabled() {
			return s.closedEyesRegistry(s.store.load(), s.now()), nil
		}
		views, _, _ := s.snapshotRead()
		result := s.snapshotRegistryResult(views)
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return topicRegistryResult{}, err
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if s.app.IsClosedEyesModeEnabled() {
		return s.closedEyesRegistry(s.store.load(), s.now()), nil
	}
	if !s.registryMu.TryLock() {
		// A slow background rebuild must not queue UI reads behind its collection.
		// Independently verify the immutable last-good result before returning it.
		if c := s.loadRegistryCache(); c != nil {
			revision, err := s.captureRegistryRevision(ctx)
			if err == nil && topicRegistryCacheMatches(c, revision, s.store.load(), s.now()) {
				return c.result, nil
			}
			// While a rebuild holds the lock, the last-good result may still
			// be read when no privacy action happened since it was built:
			// same privacy epoch, policy and Closed Eyes, same topics state,
			// and inside its validity window. Source churn alone (new
			// activity) waits for the rebuild instead of failing the read.
			if err == nil && topicRegistryCachePrivacyMatches(c, revision, s.store.load(), s.now()) {
				return c.result, nil
			}
		}
		if err := lockTodayChatPublication(ctx, s.registryMu.TryLock); err != nil {
			return topicRegistryResult{}, s.registryReadFailure("revision", err, started, 0, 0)
		}
	}
	defer s.registryMu.Unlock()
	return s.registryViewsLocked(ctx)
}

func (s *topicsService) registryViewsLocked(ctx context.Context) (topicRegistryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := s.collectRegistryViewsLocked(ctx, false)
	if err == nil {
		return result, nil
	}
	// Retry a failed source read once before calling it persistent. Revision
	// churn and deadlines remain bounded; a later app-lifecycle retry is single-flight.
	var failure *topicRegistryReadError
	if errors.As(err, &failure) && failure.kind != "revision_changed" && ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		result, err = s.collectRegistryViewsLocked(ctx, false)
	}
	if err != nil {
		s.retryRegistryInBackground()
	}
	return result, err
}

func (s *topicsService) collectRegistryViewsLocked(ctx context.Context, refresh bool) (topicRegistryResult, error) {
	if refresh {
		s.registryDeferredChat = nil // A newer collection supersedes the old chat job.
	}
	parent := ctx
	started := time.Now()
	activitiesCount, goalsCount := 0, 0
	stepAt := started
	step := func(name string) {
		elapsed := time.Since(stepAt)
		log.Printf("[topics] registry_step step=%s elapsed_ms=%d", name, elapsed.Milliseconds())
		stepAt = time.Now()
	}
	fail := func(step string, err error) (topicRegistryResult, error) {
		log.Printf("[topics] registry_step step=%s elapsed_ms=%d", step, time.Since(stepAt).Milliseconds())
		return topicRegistryResult{}, s.registryReadFailure(step, err, started, activitiesCount, goalsCount)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	state := s.store.load()
	if s.app.IsClosedEyesModeEnabled() {
		return s.closedEyesRegistry(state, s.now()), nil
	}
	if s.registryBeforeSnapshot != nil {
		s.registryBeforeSnapshot()
	}
	revision, err := s.captureRegistryRevision(ctx)
	if err != nil {
		return fail("revision", err)
	}
	step("revision")
	if revision.closedEyes {
		return s.closedEyesRegistry(state, s.now()), nil
	}
	if !refresh && s.registryBeforeSnapshot == nil && s.registryBeforePublish == nil && s.registryAfterPublish == nil && s.registryDuringCollect == nil && s.registryChatContext == nil && s.registryDuringChat == nil && s.registryFilesHashed == nil {
		if cached, ok := s.cachedRegistryViews(revision, state, s.now()); ok {
			return cached, nil
		}
	}
	if s.registryCollectContext != nil {
		var collectCancel context.CancelFunc
		ctx, collectCancel = s.registryCollectContext(ctx)
		defer collectCancel()
	}
	if s.registryDuringCollect != nil {
		s.registryDuringCollect()
	}
	now := s.now()
	if err := s.refreshRegistryNames(ctx, now); err != nil {
		return fail("activities", err)
	}
	activitiesDone := timeTopicStep("registry_step", "activities_limited")
	activities, deferredActivities, activitiesTruncated, err := s.app.topicRegistryActivitiesLimited(ctx, now, state.Projects)
	activitiesDone("rows", len(activities), "deferred", len(deferredActivities), "truncated", activitiesTruncated)
	if err != nil {
		return fail("activities", err)
	}
	step("activities")
	// The bounded activity read has now verified the same source-fact cache
	// that following uses; its cold read never needs a second raw scan.
	if refresh {
		s.app.interestsSvc().invalidateFollowingVerification()
	}
	// Warm activity provenance before following labels are checked against
	// their captured run/session closure.
	activityRefs, activityRefsErr := s.app.topicActivitySourceReferences(ctx, activities)
	if activityRefsErr != nil {
		activityRefs = nil
	}
	followingDone := timeTopicStep("registry_step", "following")
	following, state, err := s.registryFollowing(ctx, state, revision, false)
	followingDone("topics", len(following.Topics))
	if err != nil {
		return fail("following", err)
	}
	activitiesCount = len(activities)
	allowedActivities := topicRowsByID(activities)
	goals, deferredGoals, goalsTruncated, err := s.app.topicRegistryGoalsLimited(ctx, now, allowedActivities)
	if err != nil {
		return fail("goals", err)
	}
	step("goals")
	goalsCount = len(goals)
	in := topicRegistryData{
		deferredActivities: deferredActivities, deferredGoals: deferredGoals, truncated: activitiesTruncated || goalsTruncated,
		existing: state.Projects, activities: activities,
		goals: goals, meetings: s.app.topicRegistryMeetings(ctx, now),
		following: following, finds: s.registryFinds(state, now), findsRanAt: state.Finds.RanAt,
		sidebarPins: state.SidebarPins, now: now, selfName: s.app.topicRegistrySelfName(ctx),
		waitingOnThem: s.app.topicOutboundWaiting(ctx),
	}
	outcomes := map[string]*ActivityThreadOutcome{}
	outcomeReads := 0
	in.outcome = func(row ActivityRow) *ActivityThreadOutcome {
		if outcome, ok := outcomes[row.ID]; ok {
			return outcome
		}
		if row.threadOutcomeRecord == "" {
			outcomes[row.ID] = nil
			return nil
		}
		outcomeReads++
		if outcomeReads > topicRegistryOutcomeLimit || ctx.Err() != nil {
			in.truncated = true
			return nil
		}
		outcome := s.app.readableThreadOutcome(ctx, row)
		outcomes[row.ID] = outcome
		return outcome
	}
	in.existing, in.withheld = s.app.readableTopicProjects(ctx, in.existing, in.activities, in.goals, now, in.deferredActivities, in.deferredGoals)
	result := buildTopicRegistry(in)
	log.Printf("[topics] today level=info stage=full full_read_count=%d projects=%d following=%d", len(result.views), len(result.projects), len(following.Topics))
	if ctx.Err() != nil {
		return fail("snapshot", ctx.Err())
	}
	if in.truncated {
		for i := range result.views {
			if result.views[i].Kind == TopicKindProject {
				result.views[i].Truncated = true
			}
		}
	}
	var candidate *topicSnapshot
	candidateReady := s.snapshots != nil && activityRefs != nil
	if candidateReady {
		candidate = buildTopicSnapshot(result, now, revision.policy, activityRefs, s.app.interestsSvc().store.load().Interests)
	}
	manual := false
	if refresh {
		s.registryWorkerMu.Lock()
		manual = s.registryBuildManual
		s.registryWorkerMu.Unlock()
	}
	// Today's complete display is independent of contextual chat admission.
	// Publish it before auxiliary chat derivation can exhaust the build budget.
	// Keep previous chat proofs intact; they still validate every original ref
	// at Open/Send. Synchronous callers retain their all-or-nothing contract.
	lifecycle := refresh && s.registryWorkerRunning()
	publish := func(chatComplete bool) error {
		cache := &topicRegistryCache{result: result, revision: revision, at: now, until: topicChatValidUntil(in), chatComplete: chatComplete}
		cache.databaseFiles, err = s.registryDatabaseFiles(ctx)
		if err != nil {
			return err
		}
		return s.publishRegistrySnapshot(ctx, revision, state, result, cache, candidate)
	}
	if lifecycle {
		if s.registryBeforePublish != nil {
			s.registryBeforePublish()
		}
		result.chat = state.ChatTopics
		if err := publish(false); err != nil {
			return fail("snapshot", err)
		}
		replaced := candidateReady && s.commitBuiltSnapshot(candidate, true, manual)
		state = s.store.load()
		if replaced {
			s.app.emitBrowserEvent("topics:updated")
		}
		// Auxiliary proofs get their own bounded background phase; a slow
		// collection must not leave them only the collection's last few ms.
		var chatPhaseCancel context.CancelFunc
		ctx, chatPhaseCancel = context.WithTimeout(parent, 30*time.Second)
		defer chatPhaseCancel()
	}
	// Derive within the full registry budget. An incomplete pass must never
	// replace saved chat snapshots with empty or partially derived metadata.
	chatCtx, chatCancel := ctx, func() {}
	if s.registryChatContext != nil {
		chatCtx, chatCancel = s.registryChatContext(ctx)
	}
	result.chat, err = s.deriveChatTopics(chatCtx, result, in, outcomes, revision)
	chatCancel()
	if err != nil {
		if lifecycle {
			// A chat deadline cannot discard an already-published full display
			// or restart the expensive registry collection on every UI poll.
			log.Printf("[topics] today level=info stage=chat_deferred kind=%s", topicRegistryErrorKind(err))
			result.chat = state.ChatTopics
			s.registryDeferredChat = &topicRegistryChatWork{result: result, in: in, outcomes: outcomes, cache: s.loadRegistryCache()}
			step("snapshot")
			return result, nil
		}
		return fail("snapshot", err)
	}
	if !lifecycle && s.registryBeforePublish != nil {
		s.registryBeforePublish()
	}
	if ctx.Err() != nil {
		return fail("snapshot", ctx.Err())
	}
	if err := publish(true); err != nil {
		return fail("snapshot", err)
	}
	if !lifecycle && candidateReady {
		s.commitBuiltSnapshot(candidate, lifecycle, manual)
	}
	step("snapshot")
	if s.registryAfterPublish != nil {
		s.registryAfterPublish()
	}
	return result, nil
}

func (s *topicsService) registryFollowing(ctx context.Context, state topicsStateFile, revision topicRegistryRevision, closed bool) (UserTopicsView, topicsStateFile, error) {
	if err := ctx.Err(); err != nil {
		return UserTopicsView{}, state, err
	}
	if closed {
		svc := s.app.interestsSvc()
		list := svc.store.load().Interests
		svc.namesMu.Lock()
		names := append([]string(nil), svc.names...)
		svc.namesMu.Unlock()
		user := svc.userChoices()
		list = interests.Recheck(list, interests.Rules{Names: names, OwnNames: interestsOwnNames, Removed: user.Removed}, user.Pinned)
		for i := range list {
			if interests.HasReDerivedRef(list[i]) && !list[i].WhyVerified {
				list[i].Why = ""
			}
		}
		return userTopicsWithInterests(userTopicsLimit(interestTopics(list), state, len(list)+len(state.Overrides)), list), state, ctx.Err()
	}
	if s.following != nil {
		list, err := s.following(ctx, revision)
		if err != nil {
			return UserTopicsView{}, state, err
		}
		if len(list) > 0 {
			return userTopicsWithInterests(userTopicsLimit(interestTopics(list), state, len(list)+len(state.Overrides)), list), state, ctx.Err()
		}
	} else if s.interests != nil {
		list := s.interests()
		if len(list) > 0 {
			return userTopicsWithInterests(userTopicsLimit(interestTopics(list), state, len(list)+len(state.Overrides)), list), state, ctx.Err()
		}
	}
	ranked, state := s.rankedPhrases(false)
	return userTopics(ranked, state), state, ctx.Err()
}

func (s *topicsService) closedEyesRegistry(state topicsStateFile, now time.Time) topicRegistryResult {
	following, _, _ := s.registryFollowing(context.Background(), state, topicRegistryRevision{}, true)
	return buildTopicRegistry(topicRegistryData{
		following: following, finds: s.registryFinds(state, now), findsRanAt: state.Finds.RanAt,
		sidebarPins: state.SidebarPins, now: now, closedEyes: true,
	})
}

const topicRegistryRecentQuery = `SELECT ` + activityColumns + ` FROM activities WHERE end_ms>=? AND start_ms<=? ORDER BY end_ms DESC,id DESC LIMIT ?`

func (a *App) topicRegistryActivities(ctx context.Context, now time.Time, projects []topicProject) ([]ActivityRow, error) {
	rows, _, _, err := a.topicRegistryActivitiesLimited(ctx, now, projects)
	return rows, err
}
func (a *App) topicRegistryActivitiesLimited(ctx context.Context, now time.Time, projects []topicProject) ([]ActivityRow, map[string]bool, bool, error) {
	deferred := map[string]bool{}
	if err := ctx.Err(); err != nil {
		return nil, deferred, false, err
	}
	if a == nil || a.activity == nil || a.activity.store == nil {
		return nil, deferred, false, nil
	}
	store := a.activity.store
	recentQueryDone := timeTopicStep("registry_step", "activities_recent_query")
	cursor, err := store.reader().QueryContext(ctx, topicRegistryRecentQuery, now.Add(-topicWindow).UnixMilli(), now.UnixMilli(), topicRegistryActivityLimit+1)
	recentQueryDone("limit", topicRegistryActivityLimit+1)
	if err != nil {
		return nil, deferred, false, err
	}
	recentRowsDone := timeTopicStep("registry_step", "activities_recent_rows")
	rows, err := scanActivityRowsContext(ctx, cursor)
	cursor.Close()
	recentRowsDone("rows", len(rows))
	if err != nil {
		return nil, deferred, false, err
	}
	truncated := len(rows) > topicRegistryActivityLimit
	if truncated {
		rows = rows[:topicRegistryActivityLimit]
	}
	wanted := map[string]bool{}
	refsDone := timeTopicStep("registry_step", "activities_project_refs")
	defer func() { refsDone("projects", len(projects), "rows", len(rows), "wanted", len(wanted)) }()
	have := map[string]bool{}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, deferred, truncated, err
		}
		have[row.ID] = true
	}
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return nil, deferred, truncated, err
		}
		for _, id := range project.ActivityIDs {
			if err := ctx.Err(); err != nil {
				return nil, deferred, truncated, err
			}
			if !have[id] {
				wanted[id] = true
			}
		}
	}
	refsDone("projects", len(projects), "rows", len(rows), "wanted", len(wanted))
	if len(wanted) > 0 {
		ids := make([]string, 0, len(wanted))
		for id := range wanted {
			if err := ctx.Err(); err != nil {
				return nil, deferred, truncated, err
			}
			ids = append(ids, id)
		}
		encoded, _ := json.Marshal(ids)
		// Read identities in time order before fetching bounded row bodies. Missing
		// rows spend no budget; only existing omitted rows remain opaque/deferred.
		idsQueryDone := timeTopicStep("registry_step", "activities_history_ids_query")
		cursor, err := store.reader().QueryContext(ctx, `SELECT id FROM activities WHERE id IN (SELECT value FROM json_each(?)) ORDER BY end_ms DESC,id DESC`, string(encoded))
		idsQueryDone("requested", len(wanted))
		if err != nil {
			return nil, deferred, truncated, err
		}
		idsDone := timeTopicStep("registry_step", "activities_history_ids_rows")
		selected := []string{}
		matched := 0
		defer func() {
			idsDone("requested", len(wanted), "matched", matched, "selected", len(selected), "deferred", len(deferred))
		}()
		for cursor.Next() {
			matched++
			if err := ctx.Err(); err != nil {
				cursor.Close()
				return nil, deferred, truncated, err
			}
			var id string
			if err := cursor.Scan(&id); err != nil {
				cursor.Close()
				return nil, deferred, truncated, err
			}
			if len(selected) < topicRegistryHistoryLimit {
				selected = append(selected, id)
			} else {
				deferred[id], truncated = true, true
			}
		}
		err = cursor.Err()
		cursor.Close()
		idsDone("requested", len(wanted), "matched", matched, "selected", len(selected), "deferred", len(deferred))
		if err != nil {
			return nil, deferred, truncated, err
		}
		encoded, _ = json.Marshal(selected)
		historyQueryDone := timeTopicStep("registry_step", "activities_history_rows_query")
		cursor, err = store.reader().QueryContext(ctx, `SELECT `+activityColumns+` FROM activities WHERE id IN (SELECT value FROM json_each(?)) ORDER BY end_ms DESC,id DESC`, string(encoded))
		historyQueryDone("requested", len(selected))
		if err != nil {
			return nil, deferred, truncated, err
		}
		historyDone := timeTopicStep("registry_step", "activities_history_rows_scan")
		history, err := scanActivityRowsContext(ctx, cursor)
		cursor.Close()
		historyDone("requested", len(selected), "rows", len(history))
		if err != nil {
			return nil, deferred, truncated, err
		}
		rows = append(rows, history...)
	}
	sortDone := timeTopicStep("registry_step", "activities_sort")
	sort.SliceStable(rows, func(i, j int) bool {
		if topicActivityAt(rows[i]) != topicActivityAt(rows[j]) {
			return topicActivityAt(rows[i]) > topicActivityAt(rows[j])
		}
		return rows[i].ID > rows[j].ID
	})
	sortDone("rows", len(rows))
	allowed, err := a.filterTopicRegistryActivities(ctx, rows)
	return allowed, deferred, truncated, err
}

// Reuse the composer's exact source policy with per-request run/session caches.
// Limits apply before N+1 reads; an over-budget collection never erases state.
func (a *App) filterTopicRegistryActivities(ctx context.Context, rows []ActivityRow) ([]ActivityRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store := a.activity.store
	policyDone := timeTopicStep("source", "policy")
	policy, err := a.activityReadPrivacyPolicyContext(ctx)
	policyDone("rows", len(rows))
	if err != nil {
		return nil, err
	}
	// Fetch the bounded candidates' run/session metadata in batches instead
	// of preparing two SQLite queries for every activity. Source provenance
	// and the live policy below are still checked for every distinct session.
	runIDs := map[string]bool{}
	runRefsDone := timeTopicStep("source", "run_refs")
	defer func() { runRefsDone("rows", len(rows), "runs", len(runIDs)) }()
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ids := composeParseStrings(row.RunIDs)
		if len(ids) > 64 {
			continue
		}
		composerCandidateAllowsModelRead(row, func(id string) bool {
			runIDs[id] = true
			return true
		})
	}
	runRefsDone("rows", len(rows), "runs", len(runIDs))
	runs := map[string]ActivityTimelineRunRow{}
	sessionIDs := map[string]bool{}
	if len(runIDs) > 0 {
		ids := make([]string, 0, len(runIDs))
		for id := range runIDs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		encoded, _ := json.Marshal(ids)
		runsQueryDone := timeTopicStep("source", "runs_query")
		cursor, err := store.reader().QueryContext(ctx, `SELECT `+timelineRunColumns+` FROM activity_timeline_groups WHERE id IN (SELECT value FROM json_each(?))`, string(encoded))
		runsQueryDone("requested", len(runIDs))
		if err != nil {
			return nil, err
		}
		runsDone := timeTopicStep("source", "runs_rows")
		batch := []ActivityTimelineRunRow{}
		defer func() { runsDone("requested", len(runIDs), "rows", len(batch)) }()
		batch, err = scanTimelineRunRowsContext(ctx, cursor)
		cursor.Close()
		runsDone("requested", len(runIDs), "rows", len(batch))
		if err != nil {
			return nil, err
		}
		runSessionsDone := timeTopicStep("source", "run_sessions")
		checked := 0
		defer func() { runSessionsDone("rows", len(batch), "checked", checked, "sessions", len(sessionIDs)) }()
		for _, run := range batch {
			checked++
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			runs[run.ID] = run
			if !activitySourceAppIsSensitive(run.AppKey, run.AppName) && len(run.SessionIDs) <= 64 {
				for _, id := range run.SessionIDs {
					sessionIDs[id] = true
				}
			}
		}
		runSessionsDone("rows", len(batch), "checked", checked, "sessions", len(sessionIDs))
	}
	ids := make([]string, 0, len(sessionIDs))
	for id := range sessionIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	sessionsDone := timeTopicStep("source", "sessions_sql")
	sessions, err := store.GetSessionsByIDsContext(ctx, ids)
	sessionsDone("requested", len(ids), "rows", len(sessions))
	if err != nil {
		return nil, err
	}
	checkedRuns, allowedRuns := map[string]bool{}, map[string]bool{}
	checkedSessions, allowedSessions := map[string]bool{}, map[string]bool{}
	var readErr error
	allowRun := func(id string) bool {
		if ctx.Err() != nil {
			readErr = ctx.Err()
			return false
		}
		if !checkedRuns[id] {
			checkedRuns[id] = true
			run, found := runs[id]
			if !found || activitySourceAppIsSensitive(run.AppKey, run.AppName) || len(run.SessionIDs) == 0 || len(run.SessionIDs) > 64 {
				return false
			}
			ok := true
			for _, sourceID := range run.SessionIDs {
				if err := ctx.Err(); err != nil {
					readErr = err
					return false
				}
				if !checkedSessions[sourceID] {
					checkedSessions[sourceID] = true
					if session := sessions[sourceID]; session != nil {
						allowed, err := a.activitySessionAllowsWithPolicy(store, *session, policy)
						if err != nil {
							readErr = err
							return false
						}
						allowedSessions[sourceID] = allowed
					}
				}
				ok = ok && allowedSessions[sourceID]
			}
			allowedRuns[id] = ok
		}
		return allowedRuns[id]
	}
	out := []ActivityRow{}
	checksDone := timeTopicStep("source", "source_checks")
	checked := 0
	defer func() {
		checksDone("rows", len(rows), "checked", checked, "runs", len(checkedRuns), "sessions", len(checkedSessions), "allowed", len(out))
	}()
	for _, row := range rows {
		checked++
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if len(composeParseStrings(row.RunIDs)) > 64 {
			continue
		}
		if composerCandidateAllowsModelRead(row, allowRun) {
			out = append(out, row)
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	checksDone("rows", len(rows), "checked", checked, "runs", len(checkedRuns), "sessions", len(checkedSessions), "allowed", len(out))
	return out, ctx.Err()
}

func (a *App) topicRegistryGoalsLimited(ctx context.Context, now time.Time, activities map[string]ActivityRow) ([]topicGoalObservation, map[string]bool, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, false, err
	}
	if a == nil {
		return nil, nil, false, nil
	}
	store, err := a.purposeProductStore()
	if err != nil {
		return nil, nil, false, nil
	}
	details, err := store.ListDetailsAllContext(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	subjects, err := mobileGoalSubjects(a)
	if err != nil {
		return nil, nil, false, err
	}
	out := []topicGoalObservation{}
	deferred := map[string]bool{}
	sort.SliceStable(details, func(i, j int) bool {
		if details[i].Current.OccurredAt != details[j].Current.OccurredAt {
			return details[i].Current.OccurredAt > details[j].Current.OccurredAt
		}
		return details[i].OpenLoop.ID > details[j].OpenLoop.ID
	})
	sourceRecords := make([]todayChatRecord, 0, len(activities))
	for _, row := range activities {
		if err := ctx.Err(); err != nil {
			return nil, nil, false, err
		}
		sourceRecords = append(sourceRecords, todayChatActivityRecord(row))
	}
	for _, detail := range details {
		if ctx.Err() != nil {
			return nil, nil, false, ctx.Err()
		}
		switch detail.OpenLoop.Kind {
		case PurposeKindGoal, PurposeKindCommitment:
		default:
			continue
		}
		if !mobileGoalVisible(detail) {
			continue
		}
		if len(out) >= topicRegistryGoalLimit {
			deferred[detail.OpenLoop.ID] = true
			continue
		}
		readable, ok := a.topicRegistryPurpose(ctx, detail, now, sourceRecords, subjects)
		if !ok || !mobileGoalVisible(readable) || strings.TrimSpace(readable.Current.Title) == "" {
			continue
		}
		goal := topicGoalObservation{
			ID: readable.OpenLoop.ID, Title: strings.TrimSpace(readable.Current.Title),
			State: readable.Current.LifecycleState, DesiredOutcome: readable.Current.DesiredOutcome,
			WaitingCondition: readable.Current.WaitingCondition, At: readable.Current.OccurredAt,
		}
		for _, evidence := range readable.Evidence {
			if err := ctx.Err(); err != nil {
				return nil, nil, false, err
			}
			switch evidence.SourceKind {
			case activityFollowUpSourceKind, "activity":
				if _, ok := activities[evidence.SourceID]; ok && !topicStringIn(goal.ActivityIDs, evidence.SourceID) {
					goal.ActivityIDs = append(goal.ActivityIDs, evidence.SourceID)
				}
			default:
			}
		}
		sort.Strings(goal.ActivityIDs)
		out = append(out, goal)
	}
	return out, deferred, len(deferred) > 0, ctx.Err()
}

func (a *App) topicRegistryMeetings(ctx context.Context, now time.Time) []topicMeetingObservation {
	if a == nil || a.store == nil {
		return nil
	}
	day := startOfDayLocal(now.UnixMilli())
	rows, err := a.store.ListCalendarEvents(ctx, day, time.UnixMilli(day).In(time.Local).AddDate(0, 0, 1).UnixMilli(), false)
	if err != nil {
		return nil
	}
	out := []topicMeetingObservation{}
	for _, row := range rows {
		if row.DeletedAt != 0 || strings.EqualFold(row.Status, "canceled") || strings.EqualFold(row.MyStatus, "declined") {
			continue
		}
		if _, ok := a.proactiveVoiceCalendarEvent(ctx, row.ID); !ok {
			continue
		}
		meeting := topicMeetingObservation{
			ID: row.ID, Title: strings.TrimSpace(row.Title), StartAt: row.StartAt, EndAt: row.EndAt,
			URL: row.URL,
		}
		if meeting.URL == "" {
			meeting.URL = row.ConferenceURL
		}
		for _, attendee := range row.Attendees {
			if !attendee.IsMe && strings.TrimSpace(attendee.Name) != "" {
				meeting.People = append(meeting.People, attendee.Name)
			}
		}
		out = append(out, meeting)
	}
	return out
}

// TodayTopicsView preserves the last useful display while a build is running,
// including on an empty bootstrap result. It grants no topic chat admission.
type TodayTopicsView struct {
	// Record presence is display metadata, never authority to replay a label.
	RefreshIntervalMS int64       `json:"refreshIntervalMs"`
	HasTopics         bool        `json:"hasTopics,omitempty"`
	FirstRun          bool        `json:"firstRun,omitempty"`
	Topics            []TopicView `json:"topics"`
	Refreshing        bool        `json:"refreshing"`
	Stale             bool        `json:"stale,omitempty"`
	// Clear invalidates a retained UI copy even when the next build is pending.
	Clear bool `json:"clear,omitempty"`
}

// GetTodayTopics returns at most twelve topics without waiting for collection.
func (a *App) GetTodayTopics() (perfValue TodayTopicsView, perfErr error) {
	defer logPageBinding("GetTodayTopics", time.Now(), &perfValue)
	displayCtx, cancelDisplay := context.WithTimeout(context.Background(), topicTodayReadBudget)
	defer cancelDisplay()
	svc := a.topicsSvc()
	views, refreshing, firstRun := svc.snapshotRead()
	closedEyes := a.IsClosedEyesModeEnabled()
	if len(views) > topicRegistryLimit {
		views = views[:topicRegistryLimit]
	}
	known := len(views) > 0 || svc.hasTopicRecords()
	clear := closedEyes || (!firstRun && len(views) == 0)
	log.Printf("[topics] today level=info stage=read read_count=%d refreshing=%t clear=%t has_topics=%t", len(views), refreshing, clear, known)
	return TodayTopicsView{
		RefreshIntervalMS: svc.store.load().backgroundRefreshInterval().Milliseconds(),
		Topics:            topicCardsWithoutPrivate(displayModelCopy(views, a.currentUILanguageContext(displayCtx))),
		Refreshing:        refreshing, Stale: svc.topicsStale(), Clear: clear, HasTopics: known,
		FirstRun: firstRun && !closedEyes,
	}, nil
}

// Counts retained, non-removed candidates; original sources must still authorize
// every returned name. A completed withheld read is not a new store.
func (s *topicsService) hasTopicRecords() bool {
	state := s.store.load()
	for _, project := range state.Projects {
		// Withdrawal leaves content-free IDs for stable identity. Those alone
		// must not prevent a genuinely empty store from showing its empty state.
		if !state.Overrides[project.ID].Removed && (project.Name != "" || project.Deferred) {
			return true
		}
	}
	for _, item := range s.app.interestsSvc().store.load().Interests {
		if !state.Overrides[item.ID].Removed {
			return true
		}
	}
	return false
}

// Internal array contract shared with the phone and registry callers.
func (a *App) todayTopicViews() ([]TopicView, error) {
	displayCtx, cancelDisplay := context.WithTimeout(context.Background(), topicTodayReadBudget)
	defer cancelDisplay()
	views, _, _ := a.topicsSvc().snapshotRead()
	if len(views) > topicRegistryLimit {
		views = views[:topicRegistryLimit]
	}
	return displayModelCopy(views, a.currentUILanguageContext(displayCtx)), nil
}

// PinTopicToSidebar sets the sidebar pin without changing Your topics order.
func (a *App) PinTopicToSidebar(id string, pinned bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errTopicIDRequired
	}
	svc := a.topicsSvc()
	svc.registryMu.Lock()
	defer svc.registryMu.Unlock()
	result, err := svc.registryViewsLocked(context.Background())
	if err != nil {
		return err
	}
	if pinned {
		if _, ok := result.keys[id]; !ok {
			return errTopicNotFound
		}
	}
	if canonical := result.canonical[id]; canonical != "" {
		id = canonical
	}
	_, err = svc.store.update(func(state *topicsStateFile) {
		if state.SidebarPins == nil {
			state.SidebarPins = map[string]bool{}
		}
		if pinned {
			state.SidebarPins[id] = true
		} else {
			delete(state.SidebarPins, id)
			for alias, canonical := range result.canonical {
				if canonical == id {
					delete(state.SidebarPins, alias)
				}
			}
			for _, project := range state.Projects {
				if project.ID == id || topicStringIn(project.Aliases, id) {
					delete(state.SidebarPins, project.ID)
					for _, alias := range project.Aliases {
						delete(state.SidebarPins, alias)
					}
				}
			}
		}
		if len(state.SidebarPins) == 0 {
			state.SidebarPins = nil
		}
	})
	if err == nil {
		a.emitBrowserEvent("topics:updated")
	}
	return err
}

// ListSidebarTopics returns pinned topics in Today priority order without the Today limit.
func (a *App) ListSidebarTopics() (perfValue []TopicView, perfErr error) {
	defer logPageBinding("ListSidebarTopics", time.Now(), &perfValue)
	displayCtx, cancelDisplay := context.WithTimeout(context.Background(), topicTodayReadBudget)
	defer cancelDisplay()
	snapshotViews, _, _ := a.topicsSvc().snapshotRead()
	views := []TopicView{}
	for _, view := range snapshotViews {
		if view.PinnedToSidebar {
			views = append(views, view)
		}
	}
	return displayModelCopy(views, a.currentUILanguageContext(displayCtx)), nil
}

func (s *topicsService) TopicByID(id string) (TopicView, bool) {
	result, err := s.registryViews(context.Background())
	if err != nil {
		return TopicView{}, false
	}
	for _, view := range result.views {
		if view.ID == id || view.ID == result.canonical[id] {
			return view, true
		}
	}
	return TopicView{}, false
}

func (s *topicsService) TopicKeys(id string) (TopicKeySet, bool) {
	result, err := s.registryViews(context.Background())
	if err != nil {
		return TopicKeySet{ThreadKeys: []string{}, GoalIDs: []string{}}, false
	}
	keys, ok := result.keys[id]
	if !ok {
		return TopicKeySet{ThreadKeys: []string{}, GoalIDs: []string{}}, false
	}
	keys.ThreadKeys = append([]string{}, keys.ThreadKeys...)
	keys.GoalIDs = append([]string{}, keys.GoalIDs...)
	return keys, true
}

func topicProjectWithheld(project topicProject, withheld map[string]bool) bool {
	if withheld[project.ID] {
		return true
	}
	for _, alias := range project.Aliases {
		if withheld[alias] {
			return true
		}
	}
	return false
}

func topicProjectPinned(project topicProject, pins map[string]bool) bool {
	if pins[project.ID] {
		return true
	}
	for _, alias := range project.Aliases {
		if pins[alias] {
			return true
		}
	}
	return false
}

// readableTopicProjects reauthorizes saved identity from its original sources.
// Missing/deleted/protected sources remove derived identity, retaining only a
// content-free id reservation and sidebar choices. Rebuilding names and keys
// prevents an edited source from authorizing stale text. This path never contributes to the search ranking.
func (a *App) readableTopicProjects(ctx context.Context, projects []topicProject, rows []ActivityRow, goals []topicGoalObservation, now time.Time, deferredActivities, deferredGoals map[string]bool) ([]topicProject, map[string]bool) {
	activities := map[string]topicActivityObservation{}
	self := a.topicRegistrySelfName(ctx)
	for _, row := range rows {
		activities[row.ID] = newTopicActivityObservation(row, self)
	}
	byGoal := map[string]topicGoalObservation{}
	for _, goal := range goals {
		byGoal[goal.ID] = goal
	}
	out := make([]topicProject, 0, len(projects))
	withheld := map[string]bool{}
	for _, project := range projects {
		keys := map[string]bool{}
		names := []string{}
		activityIDs, goalIDs := []string{}, []string{}
		deferredActivityIDs, deferredGoalIDs := []string{}, []string{}
		ids := map[string]bool{}
		for _, id := range project.ActivityIDs {
			ids[id] = true
		}
		for _, id := range sortedTopicKeys(ids) {
			activity, ok := activities[id]
			if !ok {
				if deferredActivities[id] {
					deferredActivityIDs = append(deferredActivityIDs, id)
				}
				continue
			}
			activityIDs = append(activityIDs, id)
			for _, key := range activity.Keys {
				keys[key] = true
			}
			if len(activity.Keys) > 0 {
				names = append(names, topicActivityProjectName(activity))
			}
		}
		for _, id := range project.GoalIDs {
			goal, ok := byGoal[id]

			if !ok {
				if deferredGoals[id] {
					deferredGoalIDs = append(deferredGoalIDs, id)
				}
				continue
			}
			goalIDs = append(goalIDs, id)
			names = append(names, goal.Title)
		}
		name := ""
		for _, candidate := range names {
			candidate = topicHumanName(candidate)
			if name == "" || topicNamesEqual(candidate, topicHumanName(project.Name)) {
				name = candidate
			}
			if topicNamesEqual(candidate, topicHumanName(project.Name)) {
				break
			}
		}
		if name == "" {
			// Retired identity is content-free and cannot claim later work.
			// Sidebar choices remain attached to this id, never transferred.
			withheld[project.ID] = true
			project = topicProject{ID: project.ID, Aliases: append([]string(nil), project.Aliases...), ActivityIDs: deferredActivityIDs, GoalIDs: deferredGoalIDs, Deferred: len(deferredActivityIDs)+len(deferredGoalIDs) > 0}
		} else {
			project.Name, project.MemberKeys = name, sortedTopicKeys(keys)
			project.ActivityIDs, project.GoalIDs = append(activityIDs, deferredActivityIDs...), append(goalIDs, deferredGoalIDs...)
			project.Deferred = len(deferredActivityIDs)+len(deferredGoalIDs) > 0
		}
		out = append(out, project)
	}
	return out, withheld
}

// registryFinds requires the complete original search evidence, rather than
// borrowing a newly refreshed interest's permission for an older result.
func (s *topicsService) registryFinds(state topicsStateFile, now time.Time) []TopicFind {
	finds := topicFindsFresh(state, now)
	if len(finds) == 0 || len(state.Finds.Sent) == 0 {
		return nil
	}
	var readable func(interests.Interest) bool
	if s.app != nil {
		readable = s.app.interestsReadFilter()
	}
	matched := map[string]bool{}
	for _, sent := range state.Finds.Sent {
		if state.Overrides[sent.ID].Removed || sent.ReadSources == nil || sent.ReadSources.Version != 1 {
			return nil
		}
		if sent.ReadSources.UserNamed {
			if len(sent.ReadSources.Evidence) != 0 || len(sent.Queries) != 0 {
				return nil
			}
		} else if readable == nil || !readable(interests.Interest{Evidence: sent.ReadSources.Evidence}) {
			return nil
		}
		matched[sent.ID] = true
	}
	out := []TopicFind{}
	for _, find := range finds {
		if matched[find.TopicID] {
			out = append(out, find)
		}
	}
	return out
}

func topicRowsByID(rows []ActivityRow) map[string]ActivityRow {
	out := map[string]ActivityRow{}
	for _, row := range rows {
		out[row.ID] = row
	}
	return out
}

// Activity follow-ups are explicit user input on Today, while activity-derived
// goal fields must still resolve against readable rows. Keep those existing
// Today rules without relaxing the stricter proactive voice source policy.
func (a *App) topicRegistryPurpose(ctx context.Context, detail PurposeOpenLoopDetail, now time.Time, activities []todayChatRecord, subjects []string) (PurposeOpenLoopDetail, bool) {
	if !detail.Eligible || detail.Current.Owner != PurposeOwnerUser {
		return PurposeOpenLoopDetail{}, false
	}
	// Check the same field lineage as proactive voice, using this batch's ledger
	// projection instead of reloading the whole ledger for each goal.
	events := map[string]bool{detail.Current.ID: true}
	complete := true
	for index := range todayChatPurposeTextFields(&detail.Current) {
		ids, ok := todayChatPurposeFieldEvents(detail, index)
		complete = complete && ok
		for _, id := range ids {
			events[id] = true
		}
	}
	for eventID := range events {
		evidence := todayChatPurposeEventEvidence(detail, eventID)
		if len(evidence) == 0 {
			complete = false
		}
		for _, item := range evidence {
			if ctx.Err() != nil || item.PrivacyDecision == TaskContextPrivacyExclude || !a.proactiveVoicePurposeSourceAllowed(ctx, item) {
				complete = false
				break
			}
		}
	}
	if complete && !mobileGoalEchoesMail(detail, mobileGoalView(detail, now), subjects) {
		return detail, true
	}
	sources := todayChatSources{Purposes: []PurposeOpenLoopDetail{detail}, Activities: activities}
	for _, readable := range a.filterTodayChatPurposesContext(ctx, sources) {
		if reflect.DeepEqual(readable.Current, detail.Current) && !mobileGoalEchoesMail(readable, mobileGoalView(readable, now), subjects) {
			return readable, true
		}
	}
	return PurposeOpenLoopDetail{}, false
}

// Budget deferral retains opaque original identities for later validation,
// never the names/keys that would let unread sources claim fresh work.
func topicProjectReservation(p topicProject) topicProject {
	out := topicProject{ID: p.ID, Aliases: append([]string(nil), p.Aliases...)}
	if p.Deferred {
		out.Deferred = true
		out.ActivityIDs = p.ActivityIDs
		out.GoalIDs = p.GoalIDs
	}
	return out
}

// ListWorkTopicIDs returns every published work-topic identity, including those
// outside Today's twelve-card window. Callers validate keys before using them.
// It does not rebuild the registry or create a followed topic.
func (s *topicsService) ListWorkTopicIDs() []string {
	ids := []string{}
	for _, project := range s.store.load().Projects {
		if !project.Deferred {
			ids = append(ids, project.ID)
		}
	}
	sort.Strings(ids)
	return ids
}
