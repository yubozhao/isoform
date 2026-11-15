package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Near-duplicate beliefs learned from assistant history.
//
// Pass 2 reads a project again on every new session, and each read could
// restate a belief already in About you in slightly different words. A belief
// that repeats one already stored no longer makes a new file: its sessions
// strengthen the stored one (storeAssistantHistoryBeliefs). The one-time
// cleanup below does the same for what was learned before that: each repeat
// gets `learned_duplicate_of: <id>` in its frontmatter and its sessions are
// added to the belief it repeats. Like `activity_origin: agent`, the readers
// every page goes through skip a marked file; the file stays on disk, so
// removing the line brings the belief back, and Forget and Privacy still
// reach it.

const (
	learnedDuplicateKey = "learned_duplicate_of"
	// memoryDuplicateCleanupKey is the UI state key that records the cleanup.
	memoryDuplicateCleanupKey = "memory_duplicate_cleanup_v1"
	// historySessionStampCap bounds how many session times one belief keeps.
	historySessionStampCap = 24
)

// learnedDuplicate reports a memory file marked as a repeat of another. Like
// learnedFromAgents, it reads the frontmatter lines only.
func learnedDuplicate(content string) bool {
	trimmed := strings.TrimPrefix(content, "\ufeff")
	if !strings.HasPrefix(trimmed, "---") {
		return false
	}
	lines := strings.Split(trimmed, "\n")
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "---" {
			return false
		}
		if strings.HasPrefix(line, learnedDuplicateKey+":") {
			return true
		}
	}
	return false
}

// learnedHidden reports a learned file the pages skip: learned only from
// agent activity, or a repeat of another belief.
func learnedHidden(content string) bool {
	return learnedFromAgents(content) || learnedDuplicate(content)
}

// markLearnedDuplicate adds the duplicate line under the opening frontmatter
// fence and changes nothing else. ok is false for a file without frontmatter
// or one already hidden.
func markLearnedDuplicate(content, survivorID string) (string, bool) {
	if learnedHidden(content) || strings.TrimSpace(survivorID) == "" {
		return content, false
	}
	bom := ""
	body := content
	if strings.HasPrefix(body, "\ufeff") {
		bom, body = "\ufeff", strings.TrimPrefix(body, "\ufeff")
	}
	first, rest, found := strings.Cut(body, "\n")
	if !found || strings.TrimSpace(first) != "---" || !strings.Contains(rest, "\n---") && !strings.HasPrefix(rest, "---") {
		return content, false
	}
	return bom + first + "\n" + learnedDuplicateKey + ": " + survivorID + "\n" + rest, true
}

// historyBeliefStamps are the session times a history belief cites.
func historyBeliefStamps(frontmatter map[string]any) []string {
	stamps := okfStringSlice(frontmatter, "history_sessions_at")
	if first := okfString(frontmatter, "history_session_at"); first != "" {
		stamps = append(stamps, first)
	}
	return stamps
}

// mergeHistorySessionStamps adds session times to a history belief, so a
// belief seen again counts each session it was seen in. The first session
// time stays as it was. ok is false when nothing new was added.
func mergeHistorySessionStamps(content string, stamps []string) (string, bool) {
	doc, ok := parseOKFDocument(content)
	if !ok {
		return content, false
	}
	byAt := map[int64]string{}
	for _, stamp := range historyBeliefStamps(doc.Frontmatter) {
		if at := parseOKFTimestampMs(stamp); at != 0 {
			byAt[at] = stamp
		}
	}
	before := len(byAt)
	for _, stamp := range stamps {
		if at := parseOKFTimestampMs(stamp); at != 0 {
			if _, seen := byAt[at]; !seen {
				byAt[at] = stamp
			}
		}
	}
	if len(byAt) == before || len(byAt) < 2 {
		return content, false
	}
	ats := make([]int64, 0, len(byAt))
	for at := range byAt {
		ats = append(ats, at)
	}
	sort.Slice(ats, func(i, j int) bool { return ats[i] > ats[j] })
	if len(ats) > historySessionStampCap {
		ats = ats[:historySessionStampCap]
	}
	sort.Slice(ats, func(i, j int) bool { return ats[i] < ats[j] })
	merged := make([]string, 0, len(ats))
	for _, at := range ats {
		merged = append(merged, byAt[at])
	}
	doc.Frontmatter["history_sessions_at"] = merged
	return renderOKFDocument(doc.Frontmatter, doc.Body), true
}

// runMemoryDuplicateCleanup runs the one-time cleanup once per data folder.
func (a *App) runMemoryDuplicateCleanup(ctx context.Context) {
	if ctx == nil || a == nil || a.store == nil {
		return
	}
	if done, err := a.store.GetUIState(ctx, memoryDuplicateCleanupKey); err != nil || done == "done" {
		return
	}
	// A repeat must not fold into a belief the agent cleanup will hide.
	if done, err := a.store.GetUIState(ctx, agentActivityCleanupKey); err != nil || done != "done" {
		return
	}
	dir, err := memoryDir()
	if err != nil {
		return
	}
	// History writes hold writeMu; take it so a pass 2 never races a mark.
	a.assistantHistory.writeMu.Lock()
	hidden, strengthened, err := hideDuplicateHistoryBeliefs(ctx, dir)
	a.assistantHistory.writeMu.Unlock()
	if err != nil {
		log.Printf("[memory-duplicates] cleanup not finished: %v", err)
		return
	}
	if hidden+strengthened > 0 {
		a.markMemoryDirty("memory-duplicates-hidden")
	}
	log.Printf("[memory-duplicates] cleanup hid=%d strengthened=%d", hidden, strengthened)
	if err := a.store.SetUIState(ctx, memoryDuplicateCleanupKey, "done"); err != nil {
		log.Printf("[memory-duplicates] cleanup marker not saved: %v", err)
	}
}

// duplicateCleanupBelief is one visible history belief the cleanup compares.
type duplicateCleanupBelief struct {
	id, path, title, group string
	stamps                 []string
	firstAt                int64
	shape                  beliefShape
	// kept is a belief the person answered or wrote: it may absorb a
	// repeat but is never hidden itself.
	kept bool
	// reinforce is false for the person's own words, which stay as written.
	reinforce bool
}

// hideDuplicateHistoryBeliefs marks every visible history belief that nearly
// repeats an earlier one of the same project and assistant, and adds its
// sessions to the earlier one. A belief the person confirmed, marked Not
// quite or edited is never hidden, and is the one kept when it has a repeat.
func hideDuplicateHistoryBeliefs(ctx context.Context, dir string) (hidden, strengthened int, err error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	groups := map[string][]*duplicateCleanupBelief{}
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".md") || isOKFReservedMarkdownFile(file.Name()) {
			continue
		}
		path := filepath.Join(dir, file.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(data)
		if learnedHidden(content) {
			continue
		}
		doc, ok := parseOKFDocument(legacyMemoryWithoutConfirmation(content))
		if !ok || okfString(doc.Frontmatter, "source") != assistantHistorySource {
			continue
		}
		title := okfString(doc.Frontmatter, "title")
		if strings.TrimSpace(title) == "" {
			continue
		}
		edited := isUserEditedMemory(content)
		answered := memoryConfirmedAt(content) != 0 || memoryNotQuiteAt(content) != 0
		group := okfString(doc.Frontmatter, "history_project") + "\x00" + okfString(doc.Frontmatter, "history_assistant")
		groups[group] = append(groups[group], &duplicateCleanupBelief{
			id:        strings.TrimSuffix(file.Name(), ".md"),
			path:      path,
			title:     title,
			group:     group,
			shape:     newBeliefShape(title),
			stamps:    historyBeliefStamps(doc.Frontmatter),
			firstAt:   parseOKFTimestampMs(okfString(doc.Frontmatter, "timestamp")),
			kept:      edited || answered,
			reinforce: !edited && memoryNotQuiteAt(content) == 0,
		})
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if ctx.Err() != nil {
			return hidden, strengthened, ctx.Err()
		}
		beliefs := groups[key]
		// The person's answers come first, then the oldest: a repeat folds
		// into the belief the person already saw.
		sort.SliceStable(beliefs, func(i, j int) bool {
			if beliefs[i].kept != beliefs[j].kept {
				return beliefs[i].kept
			}
			if beliefs[i].firstAt != beliefs[j].firstAt {
				return beliefs[i].firstAt < beliefs[j].firstAt
			}
			return beliefs[i].id < beliefs[j].id
		})
		var survivors []*duplicateCleanupBelief
		absorbed := map[*duplicateCleanupBelief][]string{}
		for _, belief := range beliefs {
			var into *duplicateCleanupBelief
			if !belief.kept {
				for _, survivor := range survivors {
					if nearDuplicateBeliefShapes(belief.shape, survivor.shape) {
						into = survivor
						break
					}
				}
			}
			if into == nil {
				survivors = append(survivors, belief)
				continue
			}
			if hideDuplicateHistoryBelief(belief, into.id) {
				hidden++
				absorbed[into] = append(absorbed[into], belief.stamps...)
			}
		}
		for _, survivor := range survivors {
			if stamps := absorbed[survivor]; len(stamps) > 0 && survivor.reinforce && strengthenHistoryBelief(survivor.id, survivor.path, stamps) {
				strengthened++
			}
		}
	}
	return hidden, strengthened, nil
}

// hideDuplicateHistoryBelief marks one file under its writer lock.
func hideDuplicateHistoryBelief(belief *duplicateCleanupBelief, survivorID string) bool {
	unlock := memoryFileLocks.Acquire(belief.id)
	defer unlock()
	data, err := os.ReadFile(belief.path)
	if err != nil {
		return false
	}
	content := string(data)
	if isUserEditedMemory(content) || memoryConfirmedAt(content) != 0 || memoryNotQuiteAt(content) != 0 {
		return false
	}
	marked, ok := markLearnedDuplicate(content, survivorID)
	if !ok {
		return false
	}
	if err := writeKnowHowAtomic(belief.path, []byte(marked)); err != nil {
		log.Printf("[memory-duplicates] belief not marked: %v", err)
		return false
	}
	return true
}

// strengthenHistoryBelief adds session times to one file under its writer lock.
func strengthenHistoryBelief(id, path string, stamps []string) bool {
	unlock := memoryFileLocks.Acquire(id)
	defer unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := string(data)
	if learnedHidden(content) || isUserEditedMemory(content) {
		return false
	}
	merged, ok := mergeHistorySessionStamps(content, stamps)
	if !ok {
		return false
	}
	if err := writeKnowHowAtomic(path, []byte(merged)); err != nil {
		log.Printf("[memory-duplicates] belief not strengthened: %v", err)
		return false
	}
	return true
}
