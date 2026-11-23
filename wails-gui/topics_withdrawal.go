package main

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	"wails-gui/internal/privacy"
)

type topicWithdrawal struct {
	kind          string
	ids           map[string]bool
	goals         map[string]bool
	versionHashes map[string]string
	projection    string
}

// Resolve only local dependency identities before unlink/ledger erasure. The
// later scrub is file-only, so it is safe inside memory mutation/SQL fences.
func (a *App) topicWithdrawalForSource(kind string, ids []string, tx *sql.Tx, contents ...string) (topicWithdrawal, error) {
	w := topicWithdrawal{kind: kind, ids: map[string]bool{}, goals: map[string]bool{}, versionHashes: map[string]string{}}
	for _, content := range contents {
		w.versionHashes[taskContextHash(content)[:16]] = taskContextHash(strings.TrimSpace(content))[:16]
	}
	for _, id := range ids {
		w.ids[id] = true
		if kind == "purpose" {
			w.goals[id] = true
		}
	}
	state := a.topicsSvc().store.load()
	w.projection = topicWithdrawalProjection(state)
	needGoals := false
	for _, p := range state.Projects {
		needGoals = needGoals || len(p.GoalIDs) > 0
	}
	for _, snapshot := range state.ChatTopics {
		for _, ref := range snapshot.Topic.Refs {
			needGoals = needGoals || ref.SourceKind == "purpose"
		}
	}
	if !needGoals {
		return w, nil
	}
	var details []PurposeOpenLoopDetail
	var err error
	if tx != nil {
		details, err = loadPurposeLedger(tx)
	} else if a.activity != nil && a.activity.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		details, err = NewPurposeMapStore(a.activity.store.db).ListDetailsAllContext(ctx)
	} else {
		// No ledger can prove independence; erase saved goal-backed identity.
		for _, p := range state.Projects {
			for _, id := range p.GoalIDs {
				w.goals[id] = true
			}
		}
	}
	if err != nil {
		return w, err
	}
	for changed := true; changed; {
		changed = false
		for _, d := range details {
			if w.goals[d.OpenLoop.ID] {
				continue
			}
			for _, e := range d.Evidence {
				affected := false
				switch e.SourceKind {
				case "memory", "memory_markdown":
					ref := e.SourceID
					if len(e.SourceHash) >= 16 && memoryForgetVersionHash(e.SourceHash[:16]) {
						hash := e.SourceHash[:16]
						if normalized := w.versionHashes[hash]; normalized != "" {
							hash = normalized
						}
						ref += ":" + e.SourceVersion + ":" + strconv.FormatInt(e.SourceObservedAt, 10) + ":" + hash
					}
					affected = w.matchesSource("memory", ref)
				case "knowledge", "knowledge_markdown":
					affected = w.matchesSource("knowledge", e.SourceID)
				case "activity", "activity_summary":
					affected = kind == "activity" && w.ids[e.SourceID]
				case "purpose", "purpose_open_loop":
					affected = w.goals[e.SourceID]
				}
				if affected {
					w.goals[d.OpenLoop.ID] = true
					changed = true
					break
				}
			}
		}
	}
	return w, nil
}

// Sanitize before irreversible source mutation while excluding registry writers.
// Opaque original membership makes an interrupted/failed operation recoverable:
// the next checked build restores the same ID only if its source is readable.
func (a *App) beginTopicWithdrawal(w topicWithdrawal) (func(bool) error, error) {
	svc := a.topicsSvc()
	svc.invalidateChatSources()
	// Followed topics can still be display-only while chat derivation waits.
	// Include their original evidence, not just completed chat snapshots.
	followingWithdrawn := a.followedTopicWithdrawn(w)
	store := svc.store
	store.publication.Lock()
	store.mu.Lock()
	before := store.loadLocked()
	if w.projection != topicWithdrawalProjection(before) {
		store.mu.Unlock()
		store.publication.Unlock()
		return nil, errTopicRegistryChanged
	}
	st := before
	st.Projects = append([]topicProject(nil), before.Projects...)
	scrubTopicState(&st, w, true)
	// An unrelated composer purge also invalidates the generic disk snapshot;
	// it must not clear readable on-screen cards. Notify only actual contributors.
	withdrawn := followingWithdrawn || len(st.ChatTopics) != len(before.ChatTopics)
	for i := range st.Projects {
		withdrawn = withdrawn || !reflect.DeepEqual(st.Projects[i], before.Projects[i])
	}
	changed := proactiveVoiceHash(st) != proactiveVoiceHash(before)
	if changed {
		if _, err := store.saveLocked(st); err != nil {
			store.mu.Unlock()
			store.publication.Unlock()
			return nil, err
		}
	}
	store.mu.Unlock()
	return func(committed bool) error {
		defer func() {
			store.publication.Unlock()
			if committed && withdrawn {
				a.emitBrowserEvent("topics:withdrawn")
			}
		}()
		svc.invalidateChatSources()
		if !changed {
			return nil
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		if !committed {
			_, err := store.saveLocked(before)
			return err
		}
		current := store.loadLocked()
		scrubTopicState(&current, w, false)
		if proactiveVoiceHash(current) == proactiveVoiceHash(st) {
			return nil
		}
		_, err := store.saveLocked(current)
		return err
	}, nil
}

func (a *App) withTopicWithdrawal(w topicWithdrawal, mutate func() error) error {
	finish, err := a.beginTopicWithdrawal(w)
	if err != nil {
		return err
	}
	err = mutate()
	return errors.Join(err, finish(err == nil))
}

func scrubTopicState(st *topicsStateFile, w topicWithdrawal, keepMembers bool) {
	// Display derivatives must be erased along with the original sources.
	st.Today = nil
	affected := map[string]bool{}
	for i, p := range st.Projects {
		hit := false
		for _, id := range p.ActivityIDs {
			hit = hit || w.kind == "activity" && w.ids[id]
		}
		for _, id := range p.GoalIDs {
			hit = hit || w.goals[id]
		}
		if !hit {
			continue
		}
		affected[p.ID] = true
		for _, alias := range p.Aliases {
			affected[alias] = true
		}
		activities, goals := []string{}, []string{}
		for _, id := range p.ActivityIDs {
			if keepMembers || w.kind != "activity" || !w.ids[id] {
				activities = append(activities, id)
			}
		}
		for _, id := range p.GoalIDs {
			if keepMembers || !w.goals[id] {
				goals = append(goals, id)
			}
		}
		st.Projects[i] = topicProject{ID: p.ID, Aliases: p.Aliases, ActivityIDs: activities, GoalIDs: goals, Deferred: len(activities)+len(goals) > 0}
	}
	kept := []topicChatSnapshot{}
	for _, snapshot := range st.ChatTopics {
		hit := affected[snapshot.Topic.ID]
		for _, ref := range snapshot.Topic.Refs {
			hit = hit || w.matchesSource(ref.SourceKind, ref.SourceID) || ref.SourceKind == "purpose" && w.goals[ref.SourceID]
		}
		if !hit {
			kept = append(kept, snapshot)
		}
	}
	st.ChatTopics = kept
}

// Match the known identity before interpreting a version suffix: IDs may contain
// colons. Historical-only forget selectors preserve the corrected live record.
func (w topicWithdrawal) matchesSource(kind, id string) bool {
	if kind != w.kind {
		return false
	}
	if w.ids[id] || w.ids[strings.TrimSuffix(id, ".md")] {
		return true
	}
	if kind == "memory" {
		ref := "memory:" + id
		for source := range w.ids {
			if strings.HasPrefix(source, memoryForgetHistoricalPrefix) {
				continue
			}
			if _, matches := memoryForgetRefVersion(ref, source); matches {
				return true
			}
		}
		return memoryForgetHistoricalRefMatches(ref, []string{ref}, w.ids)
	}
	return false
}

// Activity deletion invalidates cached context and erases local derivatives
// before returning. Only an explicit user deletion publishes a withdrawal;
// composer purge and merge share the scrub without withdrawing their survivor.
func (a *App) deleteActivityWithTopicScrub(store *ActivityStore, id string) error {
	return a.deleteActivityWithTopicScrubPrivacy(store, id, false)
}

func (a *App) deleteActivityWithPrivacyWithdrawal(store *ActivityStore, id string) error {
	return a.deleteActivityWithTopicScrubPrivacy(store, id, true)
}

func (a *App) deleteActivityWithTopicScrubPrivacy(store *ActivityStore, id string, withdraw bool) error {
	if a == nil {
		return store.DeleteActivity(id)
	}
	withdrawal, err := a.topicWithdrawalForSource("activity", []string{id}, nil)
	if err != nil {
		return err
	}
	deleted := false
	err = a.withTopicWithdrawal(withdrawal, func() error {
		err := store.DeleteActivity(id)
		deleted = err == nil
		return err
	})
	if withdraw && deleted {
		publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Source, SourceKind: "activity", ID: id})
	}
	return err
}

// Dependency resolution precedes SQL mutation; reject if a new registry
// projection was published before the scrub acquired its publication lock.
func topicWithdrawalProjection(state topicsStateFile) string {
	return proactiveVoiceHash(struct {
		Projects []topicProject
		Chat     []topicChatSnapshot
	}{state.Projects, state.ChatTopics})
}

func (a *App) followedTopicWithdrawn(w topicWithdrawal) bool {
	followingWithdrawn := false
	for _, interest := range a.interestsSvc().store.load().Interests {
		for _, ref := range interest.Evidence {
			kind := string(ref.Source)
			followingWithdrawn = followingWithdrawn || w.matchesSource(kind, strings.TrimPrefix(ref.Key, kind+":"))
		}
	}
	return followingWithdrawn
}
