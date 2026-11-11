package main

import (
	"context"
	"encoding/json"
	"log"
	"reflect"
	"sort"
	"strings"
	"time"

	"wails-gui/storage"
)

func mainChatProactiveTopicRefs(card mainChatProactive) []mainChatEvidenceRef {
	refs := append([]mainChatEvidenceRef{}, card.Evidence...)
	if card.ActivityOutcomeSource != "" {
		refs = append(refs, mainChatEvidenceRef{Kind: "activity_outcome", ID: card.ActivityOutcomeSource})
	}
	refs = append(refs, mainChatEvidenceRef{Kind: card.Target.Kind, ID: card.Target.ID})
	for _, child := range card.Items {
		refs = append(refs, mainChatProactiveTopicRefs(child)...)
	}
	return refs
}
func mainChatReportTopicRefs(report mainChatReport) []mainChatEvidenceRef {
	return append(append([]mainChatEvidenceRef{}, report.Evidence...), mainChatEvidenceRef{Kind: report.Target.Kind, ID: report.Target.ID})
}
func mainChatRowTopicRefs(row storage.StreamItemRow) []mainChatEvidenceRef {
	if row.Kind != "system" {
		return nil
	}
	switch row.ToolName {
	case mainChatProactiveTool:
		var card mainChatProactive
		if json.Unmarshal([]byte(row.ToolInput), &card) == nil {
			return mainChatProactiveTopicRefs(card)
		}
	case mainChatReportTool:
		var report mainChatReport
		if json.Unmarshal([]byte(row.ToolInput), &report) == nil {
			return mainChatReportTopicRefs(report)
		}
	}
	return nil
}

// mainChatRefMatchesTopic uses A3's Activity reader and thread matching, and
// Today's original-source resolver for goals and people. No evidence gate is
// reimplemented here, and a target is never permission to read its source.
func (a *App) mainChatRefMatchesTopic(ctx context.Context, ref mainChatEvidenceRef, keys ChatTopicKeys, sources todayChatSources) bool {
	if a.IsClosedEyesModeEnabled() || ref.ID == "" || ctx.Err() != nil {
		return false
	}
	wanted := map[string]bool{}
	for _, key := range keys.ThreadKeys {
		wanted[key] = true
	}
	switch ref.Kind {
	case "activity_outcome":
		if !a.threadOutcomeSourceReadable(ctx, ref.ID) {
			return false
		}
		end := strings.LastIndex(ref.ID, ":")
		if !strings.HasPrefix(ref.ID, threadOutcomeMomentPrefix) || end < len(threadOutcomeMomentPrefix) {
			return false
		}
		ref.ID = strings.TrimPrefix(ref.ID[:end], threadOutcomeMomentPrefix)
		ref.Kind = "activity"
		return a.mainChatRefMatchesTopic(ctx, ref, keys, sources)
	case "activity", "activity_moment", "moment":
		_, row, ok := a.chatTopicActivity(ctx, ref.ID)
		if !ok {
			return false
		}
		for key := range threadSureKeys(threadMomentFromRow(*row), nil) {
			if wanted[key] {
				return true
			}
		}
		var thread, purpose string
		err := a.activity.store.reader().QueryRowContext(ctx, `SELECT m.thread_id,COALESCE(t.purpose_key,'') FROM activity_thread_members m JOIN activity_threads t ON t.id=m.thread_id WHERE m.activity_id=?`, ref.ID).Scan(&thread, &purpose)
		if err != nil {
			return false
		}
		matched, err := a.chatTopicThreadMatches(ctx, thread, purpose, wanted)
		return err == nil && matched
	case "goal", "purpose", "commitment":
		_, err := a.resolveTodayChatItem(ctx, TodayChatContextRef{SourceKind: "purpose", SourceID: ref.ID}, sources, sources.Now)
		return err == nil && topicStringIn(keys.GoalIDs, ref.ID)
	case "principal", "person":
		_, err := a.resolveTodayChatItem(ctx, TodayChatContextRef{SourceKind: "principal", SourceID: ref.ID}, sources, sources.Now)
		if err != nil {
			return false
		}
		for _, person := range sources.People {
			if person.ID == ref.ID {
				for _, name := range append([]string{person.DisplayName}, person.Aliases...) {
					if name = threadNorm(name); name != "" && wanted["person:"+name] {
						return true
					}
				}
			}
		}
	}
	return false
}

func (a *App) matchingMainChatRowTopics(ctx context.Context, refs []mainChatEvidenceRef, ids []string, sources todayChatSources) ([]string, error) {
	result := []string{}
	if a.topicSource == nil || a.IsClosedEyesModeEnabled() || len(refs) == 0 || len(ids) == 0 {
		return result, ctx.Err()
	}
	for _, id := range ids {
		_, keys, err := a.chatTopicSnapshotFromSources(ctx, id, sources)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		for _, ref := range refs {
			if a.mainChatRefMatchesTopic(ctx, ref, keys, sources) {
				result = append(result, id)
				break
			}
		}
	}
	sort.Strings(result)
	return result, ctx.Err()
}

// tagMainChatRowTopics publishes only 1-2 deterministic topic matches. A stale
// registry/source snapshot rejects the write, leaving the chat row untouched.
func (a *App) tagMainChatRowTopics(ctx context.Context, itemID string, refs []mainChatEvidenceRef) error {
	if a == nil || a.store == nil || a.topicSource == nil || a.IsClosedEyesModeEnabled() || len(refs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Capture before reading the row so an edit cannot substitute its evidence
	// between the row read and the checked publication transaction.
	lister, ok := a.topicSource.(interface{ ListWorkTopicIDs() []string })
	if !ok {
		return nil
	}
	revision, err := a.captureTopicPageRevision(ctx)
	if err != nil {
		return err
	}
	if revision.sources.closedEyes {
		return nil
	}
	row, err := a.store.LoadStreamItem(ctx, itemID)
	if err != nil || row == nil {
		return err
	}
	if row.Kind != "system" || (row.ToolName != mainChatProactiveTool && row.ToolName != mainChatReportTool) {
		return nil
	}
	sources, err := a.topicPageSourcesForRevision(ctx, revision.sources)
	if err != nil {
		return err
	}
	storedRefs := mainChatRowTopicRefs(*row)
	ids, err := a.matchingMainChatRowTopics(ctx, storedRefs, lister.ListWorkTopicIDs(), sources)
	if err != nil || len(ids) == 0 || len(ids) > 2 {
		return err
	}
	if a.mainChatRowTopicsBeforePublish != nil {
		a.mainChatRowTopicsBeforePublish()
	}
	return a.publishMainChatRowTopics(ctx, *row, ids, revision)
}
func (a *App) publishMainChatRowTopics(ctx context.Context, row storage.StreamItemRow, ids []string, expected topicPageRevision) error {
	return a.publishMainChatRowTopicBatch(ctx, []storage.StreamItemTopicInsert{{ItemID: row.ID, SessionID: row.SessionLocalID, TopicIDs: ids}}, expected)
}
func (a *App) publishMainChatRowTopicBatch(ctx context.Context, batch []storage.StreamItemTopicInsert, expected topicPageRevision) error {
	return a.publishMainChatRowTopicBatchAndCompletion(ctx, batch, expected, false)
}

func (a *App) publishMainChatRowTopicBatchAndCompletion(ctx context.Context, batch []storage.StreamItemTopicInsert, expected topicPageRevision, backfill bool) error {
	short, cancel := context.WithTimeout(ctx, topicRegistrySnapshotBudget)
	defer cancel()
	svc := a.topicsSvc()
	release, err := svc.lockRegistrySnapshot(short, true)
	if err != nil {
		return err
	}
	defer release()
	current, err := svc.registryRevisionWithSessionFiles(short, &expected.sources.sessionFiles)
	current.files = expected.sources.files
	if err != nil || !reflect.DeepEqual(current, expected.sources) || proactiveVoiceHash(svc.store.load()) != expected.topics {
		return storage.ErrReadRevisionChanged
	}
	if current.activityStore != nil {
		release, err := storage.LockDatabaseRevision(short, current.activityStore.db, current.activity)
		if err != nil {
			return err
		}
		defer release()
	}
	validate := func() error {
		if err := svc.validateRegistryFiles(short, expected.sources); err != nil {
			return err
		}
		if proactiveVoiceHash(svc.store.load()) != expected.topics {
			return storage.ErrReadRevisionChanged
		}
		if backfill {
			return a.requireMainChatTopicBackfillReady(expected)
		}
		return nil
	}
	if backfill {
		return a.store.CompleteStreamItemTopicBackfillIfCurrent(short, batch, current.main, mainChatTopicBackfillKey, validate)
	}
	return a.store.InsertStreamItemTopicBatchIfCurrent(short, batch, current.main, validate)
}

// projectMainChatView batches persisted tags, then rechecks only system-row
// membership. User/assistant A3 tags retain their direct-inspection behavior.
// The returned release holds publication fences until the caller returns.
func (a *App) projectMainChatView(ctx context.Context, sessionID string, items []StreamItemData, now time.Time) (func(), error) {
	// Derived membership is optional enrichment. A racing source/database
	// write must never make the user's own conversation unavailable.
	compute, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	revision, err := a.computeMainChatView(compute, sessionID, items, now)
	if err == nil && revision != nil {
		release, validationErr := a.validateTopicPageRevision(compute, *revision)
		if validationErr == nil {
			return release, nil
		}
		err = validationErr
	}
	if err != nil {
		for i := range items {
			if items[i].Kind == "system" {
				items[i].TopicIDs = nil
			}
		}
	}
	return func() {}, nil
}
func (a *App) computeMainChatView(ctx context.Context, sessionID string, items []StreamItemData, now time.Time) (*topicPageRevision, error) {
	if a == nil || a.store == nil || len(items) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	metadata, err := a.store.LoadMainChatRowMetadata(ctx, sessionID, ids)
	if err != nil {
		return nil, err
	}
	// Only derived system tags need a publication fence. If the writer is
	// busy, omit those optional tags, retaining the conversation and metadata.
	var before storage.ReadRevision
	needsFence := false
	for _, m := range metadata {
		if m.Row.Kind == "system" && len(m.TopicIDs) > 0 {
			needsFence = true
			break
		}
	}
	if needsFence {
		fenceCtx := ctx
		cancel := func() {}
		if streamTimed, _ := ctx.Value(streamProjectionTimingKey{}).(bool); streamTimed {
			fenceCtx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
		}
		before, err = a.store.ReadRevision(fenceCtx)
		cancel()
		if err != nil {
			if incomplete, ok := ctx.Value(streamProjectionIncompleteKey{}).(*bool); ok {
				*incomplete = true
			}
			needsFence = false
		} else {
			// The revision must precede the metadata used to derive tags.
			metadata, err = a.store.LoadMainChatRowMetadata(ctx, sessionID, ids)
			if err != nil {
				return nil, err
			}
		}
	}
	derived := false
	var briefAt time.Time
	for _, m := range metadata {
		if m.Row.ToolName == mainChatProactiveTool {
			if svc, ok := a.mobileBriefSource().(*mobileBriefService); ok && svc != nil && svc.store != nil {
				if brief, ok := svc.store.latest(); ok && (brief.Slot == "morning" || brief.Slot == "evening") {
					briefAt, _ = time.Parse(time.RFC3339, brief.AsOf)
				}
			}
			break
		}
	}
	for i := range items {
		m, ok := metadata[items[i].ID]
		if !ok {
			continue
		}
		// Display state can withdraw an old outcome or mute a retained card.
		// Classify that projection, while keeping the stored row's day and identity.
		items[i].RowClass = string(mainChatRowClassForDisplay(m.Row, items[i].ToolInput, now))
		var card mainChatProactive
		if m.Row.ToolName == mainChatProactiveTool && json.Unmarshal([]byte(m.Row.ToolInput), &card) == nil && mainChatLeftOffFolded(card, m.Row.CreatedAt, briefAt) {
			items[i].RowClass = string(MainChatRowRoutine)
		}
		items[i].LocalDay = time.UnixMilli(m.Row.CreatedAt).In(time.Local).Format("2006-01-02")
		if m.Row.Kind == "system" {
			derived = derived || (needsFence && len(m.TopicIDs) > 0)
		} else {
			items[i].TopicIDs = m.TopicIDs
		}
	}
	if !derived || a.IsClosedEyesModeEnabled() || a.topicSource == nil {
		return nil, nil
	}
	revision, err := a.captureTopicPageRevision(ctx)
	if err != nil {
		return nil, err
	}
	if revision.sources.main != before {
		return nil, storage.ErrReadRevisionChanged
	}
	sources, err := a.topicPageSourcesForRevision(ctx, revision.sources)
	if err != nil {
		return nil, err
	}
	for i := range items {
		m := metadata[items[i].ID]
		if m.Row.Kind != "system" || len(m.TopicIDs) == 0 {
			continue
		}
		tags, err := a.matchingMainChatRowTopics(ctx, mainChatRowTopicRefs(m.Row), m.TopicIDs, sources)
		if err != nil {
			return nil, err
		}
		if len(tags) <= 2 {
			items[i].TopicIDs = tags
		}
	}
	if a.mainChatRowTopicsBeforeReadPublish != nil {
		a.mainChatRowTopicsBeforeReadPublish()
	}
	return &revision, nil
}

const mainChatTopicBackfillKey = "main_chat_topic_backfill_v1"

// startMainChatTopicBackfill waits for a checked local registry snapshot, with
// bounded retries and cancellation; it never invokes a model or search.
func (a *App) startMainChatTopicBackfill(ctx context.Context) {
	go func() {
		timer := time.NewTimer(time.Minute)
		defer timer.Stop()
		for attempt := 0; attempt < 14; attempt++ {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if !a.IsClosedEyesModeEnabled() {
				if err := a.backfillMainChatRowTopics(ctx, time.Now()); err == nil {
					return
				}
			}
			timer.Reset(time.Minute)
		}
	}()
}
func (a *App) backfillMainChatRowTopics(ctx context.Context, now time.Time) error {
	if !a.mainChatTopicBackfillRunning.CompareAndSwap(false, true) {
		return errTopicRegistryChanged
	}
	defer a.mainChatTopicBackfillRunning.Store(false)
	done, err := a.store.GetUIState(ctx, mainChatTopicBackfillKey)
	if err != nil || done == "true" {
		return err
	}
	if a.IsClosedEyesModeEnabled() {
		return errTodayChatObservationPaused
	}
	if _, ok := a.topicSource.(interface{ ListWorkTopicIDs() []string }); !ok {
		return errTodayChatItemUnavailable
	}
	if _, production := a.topicSource.(*registryTopicSource); production {
		svc := a.topicsSvc()
		if svc.registryWorkerRunning() {
			if c := svc.loadRegistryCache(); c == nil || !c.chatComplete {
				return errTopicRegistryChanged
			}
		} else if _, err := svc.registryViews(ctx); err != nil {
			return err
		}
	}
	revision, err := a.captureTopicPageRevision(ctx)
	if err != nil {
		return err
	}
	if err := a.requireMainChatTopicBackfillReady(revision); err != nil {
		a.topicsSvc().ensureRegistryRefresh()
		return err
	}
	rows, err := a.store.LoadMainChatTopicBackfillRows(ctx, now.AddDate(0, 0, -14).UnixMilli())
	if err != nil {
		return err
	}
	sources, err := a.topicPageSourcesForRevision(ctx, revision.sources)
	if err != nil {
		return err
	}
	ids := a.topicSource.(interface{ ListWorkTopicIDs() []string }).ListWorkTopicIDs()
	batch := []storage.StreamItemTopicInsert{}
	for _, row := range rows {
		if err = ctx.Err(); err != nil {
			return err
		}
		tags, err := a.matchingMainChatRowTopics(ctx, mainChatRowTopicRefs(row), ids, sources)
		if err != nil {
			return err
		}
		if len(tags) > 0 && len(tags) <= 2 {
			batch = append(batch, storage.StreamItemTopicInsert{ItemID: row.ID, SessionID: row.SessionLocalID, TopicIDs: tags})
		}
	}
	if a.mainChatRowTopicsBeforePublish != nil {
		a.mainChatRowTopicsBeforePublish()
	}
	if err = a.publishMainChatRowTopicBatchAndCompletion(ctx, batch, revision, true); err != nil {
		return err
	}
	log.Printf("[main-chat-topic-backfill] scanned=%d", len(rows))
	return nil
}

// A checked display can precede complete chat admission. Only the latter may
// finalize historical membership; a partial/last-good display is not readiness.
// This reads immutable in-memory proof metadata, including inside the DB fence.
func (a *App) requireMainChatTopicBackfillReady(expected topicPageRevision) error {
	if _, production := a.topicSource.(*registryTopicSource); !production {
		return nil
	}
	svc := a.topicsSvc()
	c := svc.loadRegistryCache()
	if c == nil || !c.chatComplete || !topicRegistryCacheMatches(c, expected.sources, svc.store.load(), svc.now()) {
		return errTopicRegistryChanged
	}
	return nil
}
