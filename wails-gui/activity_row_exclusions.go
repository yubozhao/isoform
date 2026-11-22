package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

type activityTopicExclusion struct {
	Moment  string `json:"moment"`
	Topic   string `json:"topic"`
	At      string `json:"at"`
	EntryID string `json:"entryId"`
}

type activityTopicExclusions []activityTopicExclusion

func (x activityTopicExclusions) rejects(p topicProject, id string) bool {
	for _, e := range x {
		if e.Moment == id && (e.Topic == p.ID || slices.Contains(p.Aliases, e.Topic)) {
			return true
		}
	}
	return false
}

// When the last member goes, readableTopicProjects reserves its old ID. Do
// not let that same moment allocate a suffixed copy of its rejected seed.
func (x activityTopicExclusions) rejectsAttachment(a topicActivityObservation, builds []topicProjectBuild) bool {
	if !slices.ContainsFunc(x, func(e activityTopicExclusion) bool { return e.Moment == a.Row.ID }) {
		return false
	}
	matched := false
	for _, b := range builds {
		if topicProjectHasAnyKey(b.project, a.Keys) {
			matched = true
			if !x.rejects(b.project, a.Row.ID) {
				return false
			}
		}
	}
	return matched || x.rejects(topicProject{ID: topicProjectID(a.Keys[0])}, a.Row.ID)
}

// Filter after attachment and merging, before building any display or chat
// derivatives. Deferred members have IDs but no current observation.
func (x activityTopicExclusions) apply(builds []topicProjectBuild) {
	if len(x) == 0 {
		return
	}
	for i := range builds {
		b := &builds[i]
		b.project.ActivityIDs = slices.DeleteFunc(b.project.ActivityIDs, func(id string) bool { return x.rejects(b.project, id) })
		b.activities = slices.DeleteFunc(b.activities, func(a topicActivityObservation) bool { return x.rejects(b.project, a.Row.ID) })
	}
}

func (s *topicsStore) activityExclusionPath() string {
	return filepath.Join(filepath.Dir(s.path), "activity-topic-exclusions.json")
}

// The sidecar is optional until the first correction. An existing, unreadable
// file must never be overwritten or treated as an empty list.
func (a *App) activityExclusionsLocked(ctx context.Context, s *topicsStore) (activityTopicExclusions, error) {
	if s.path == "" {
		// No topics folder, so no sidecar can exist: there are no exclusions.
		// Writers (NotThisTopic) still fail on the missing folder.
		return nil, nil
	}
	raw, err := os.ReadFile(s.activityExclusionPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var x activityTopicExclusions
	if err := json.Unmarshal(raw, &x); err != nil {
		return nil, err
	}
	if x == nil {
		return nil, errors.New("invalid activity exclusions")
	}
	if len(x) == 0 {
		return x, nil
	}
	if a.activity == nil || a.activity.store == nil {
		return nil, errActivityFeedNotReady
	}
	rows, err := a.activity.store.reader().QueryContext(ctx, `SELECT id FROM activities WHERE id IN
		(SELECT json_extract(value,'$.moment') FROM json_each(?))`, string(raw))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	live := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		live[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	original := len(x)
	for _, e := range x {
		if e.Moment == "" || e.Topic == "" || e.At == "" || e.EntryID == "" {
			return nil, errors.New("invalid activity exclusion")
		}
	}
	x = slices.DeleteFunc(x, func(e activityTopicExclusion) bool { return !live[e.Moment] })
	if len(x) != original {
		err = writeMobilePrivateJSON(s.activityExclusionPath(), x)
	}
	return x, err
}

func (svc *topicsService) activityExclusions(ctx context.Context) (activityTopicExclusions, error) {
	s := svc.store
	s.mu.Lock()
	defer s.mu.Unlock()
	return svc.app.activityExclusionsLocked(ctx, s)
}

func (s *topicsStore) activityTopicsStateLocked() (topicsStateFile, error) {
	var state *topicsStateFile
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return topicsStateFile{}, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return topicsStateFile{}, err
	}
	if state == nil {
		return topicsStateFile{}, errors.New("invalid topics state")
	}
	return *state, nil
}

func (a *App) activityRowTopics(ctx context.Context) (map[string]*topicProject, error) {
	s := a.topicsSvc().store
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.activityTopicsStateLocked()
	if err != nil {
		return nil, err
	}
	x, err := a.activityExclusionsLocked(ctx, s)
	if err != nil {
		return nil, err
	}
	out := map[string]*topicProject{}
	for i := range state.Projects {
		p := &state.Projects[i]
		if p.Deferred || strings.TrimSpace(p.Name) == "" {
			continue
		}
		for _, id := range p.ActivityIDs {
			if x.rejects(*p, id) {
				continue
			}
			prev := out[id]
			if prev == nil || len(p.ActivityIDs) > len(prev.ActivityIDs) || (len(p.ActivityIDs) == len(prev.ActivityIDs) && p.ID < prev.ID) {
				out[id] = p
			}
		}
	}
	return out, nil
}

func (a *App) NotThisTopic(kind, id, topicID string) error {
	if topicID == "" {
		return errTopicIDRequired
	}
	if _, err := a.activityRowStore(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	svc := a.topicsSvc()
	s := svc.store
	s.publication.Lock()
	defer s.publication.Unlock()
	// The periodic worker reads the store while holding its scheduling mutex.
	// Mark the saved projection stale after releasing the store mutex.
	membershipChanged := false
	defer func() {
		if membershipChanged {
			svc.markTopicsStale()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := a.activityScreenMoment(ctx, kind, id)
	if err != nil {
		return err
	}
	state, err := s.activityTopicsStateLocked()
	if err != nil {
		return err
	}
	x, err := a.activityExclusionsLocked(ctx, s)
	if err != nil {
		return err
	}
	var choice activityTopicExclusion
	for _, e := range x {
		if e.Topic == topicID && e.Moment == id {
			choice = e
			break
		}
	}
	var project *topicProject
	for i := range state.Projects {
		p := &state.Projects[i]
		if (p.ID == topicID || slices.Contains(p.Aliases, topicID)) && slices.Contains(p.ActivityIDs, id) {
			project = p
			break
		}
	}
	recorded := false
	if choice.EntryID == "" {
		if project == nil || project.ID != topicID || project.Deferred || strings.TrimSpace(project.Name) == "" {
			return errors.New("moment is not in this topic")
		}
		choice = activityTopicExclusion{Moment: id, Topic: topicID, At: mobileTimestamp(time.Now()), EntryID: uuid.NewString()}
		if err := writeMobilePrivateJSON(s.activityExclusionPath(), append(x, choice)); err != nil {
			return err
		}
	} else {
		entries, err := a.answersLedger().readEntriesSkippingMalformed()
		if err != nil {
			return err
		}
		recorded = slices.ContainsFunc(entries, func(r AnswerRecord) bool { return r.EntryID == choice.EntryID })
	}
	if project != nil {
		project.ActivityIDs = slices.DeleteFunc(project.ActivityIDs, func(member string) bool { return member == id })
		state.ChatTopics = slices.DeleteFunc(state.ChatTopics, func(snapshot topicChatSnapshot) bool {
			return snapshot.Topic.ID == project.ID || slices.Contains(snapshot.Aliases, project.ID)
		})
		if _, err := s.saveLocked(state); err != nil {
			return err
		}
		membershipChanged = true
		svc.invalidateChatSources()
	}
	if !recorded {
		_, err = a.recordAnswer(AnswerRecord{EntryID: choice.EntryID, At: choice.At, ItemID: "activity:" + id, Kind: "activity", Source: "topic", Action: answerActionNotThisTopic, Title: row.Title, Surface: "computer"})
	}
	return err
}
