package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/draw"
	"image/jpeg"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/storage"

	"yansu-agent/agent"
)

// WeChat exposes almost nothing through accessibility or OCR (validated: its chat
// bubbles never reached the text pipeline, so real conversations were missed and
// the extractor was misled by the left-side group list). For WeChat observations
// we therefore read the chat from the SCREENSHOT with a multimodal model — the
// approach AirJelly uses for every app — and use the result as the observation
// Body. Scoped to WeChat for now to keep vision cost bounded.

// wechatSidebarFraction is the left fraction of the WeChat window taken up by the
// conversation list, cropped away so the VLM sees only the active chat area.
const wechatSidebarFraction = 0.28

const wechatVisionTimeout = 60 * time.Second

// wechatBundleID is WeChat's macOS bundle identifier, used to check the
// protected-apps list before running the vision pass.
const wechatBundleID = "com.tencent.xinWeChat"

func isWeChatObservation(o Observation) bool {
	switch o.RealApp {
	case "WeChat", "tencent_chat":
		return true
	}
	return false
}

// Recheck persisted provenance after queueing and again before using a crop or
// cached vision result. Protection can change while earlier frames are read.
// It also holds the WeChat pictures switch and waits for the privacy model
// (activity_vision_switch.go).
func (c *ActivityTriager) visionSessionAllowsModelRead(sessionID string) bool {
	if c == nil || c.app == nil || c.app.IsClosedEyesModeEnabled() || !c.app.visionPicturesAllowed() {
		return false
	}
	store := c.activityStore()
	if store == nil {
		return false
	}
	session, err := store.GetSessionByID(sessionID)
	return err == nil && session != nil && c.app.activitySessionAllowsModelRead(store, *session)
}

// enrichWeChatObservations replaces the Body of WeChat observations with chat
// content read from their screenshot by a VLM. No-op without a runner / usable
// frame; a failure or "no real conversation" leaves the original body untouched.
func (c *ActivityTriager) enrichWeChatObservations(ctx context.Context, seq *ObservationSequence) {
	// When WeChat itself is on the protected-apps list its screenshots are
	// captured fully blurred, so a VLM read cannot succeed — but worse, the
	// model HALLUCINATES plausible chats from the mosaic (observed: senders
	// "Alice"/"Bob"/"Person 1" with fabricated messages), polluting the
	// Who you know page with fake contacts. Skip the entire pass.
	if _, isSensitive := pii.IsSensitiveApp(wechatBundleID); isSensitive ||
		pii.IsSensitiveAppName("微信") || pii.IsSensitiveAppName("WeChat") {
		return
	}
	store := c.activityStore()
	if store == nil {
		return
	}
	snapshotsDir := store.SnapshotsDir()
	snapCache := map[string][]ActivitySnapshotRow{}
	getSnaps := func(sid string) []ActivitySnapshotRow {
		if !c.visionSessionAllowsModelRead(sid) {
			return nil
		}
		if s, ok := snapCache[sid]; ok {
			return s
		}
		s, err := store.GetSessionSnapshots(sid)
		if err != nil {
			s = nil
		}
		snapCache[sid] = s
		return s
	}
	// One labeler for the whole pass: it resolves each VLM-read sender to a
	// principal (auto-bucketing unknowns) and records a People sighting, the
	// same machinery the AX-based parsers feed for Discord/Slack. WeChat is
	// the only chat app with no AX/OCR sender signal, so this VLM read is its
	// sole path onto the Who you know page. Uses the principal store (c.app.store),
	// not the activity store, mirroring buildGroupSummary's labeler wiring.
	var principalStore *storage.Store
	if c.app != nil {
		principalStore = c.app.store
	}
	labeler := newSenderLabeler(principalStore)
	wechatObs, vlmCalls, recorded := 0, 0, 0
	for i := range seq.Observations {
		o := &seq.Observations[i]
		if !isWeChatObservation(*o) {
			continue
		}
		wechatObs++
		sn, ok := representativeWeChatSnapshot(o, getSnaps(o.SessionID))
		if !ok {
			log.Printf("[wechat-people] obs %d (session %s): no keyboard-triggered frame with a focus rect — skipping VLM", i, o.SessionID)
			continue
		}
		img := cropFocusedWindowArea(sn, snapshotsDir, wechatSidebarFraction)
		if img == nil {
			log.Printf("[wechat-people] obs %d (session %s, snap %d): crop failed (missing file or focus rect) — skipping", i, o.SessionID, sn.ID)
			continue
		}
		res := c.describeWeChatScreenshot(ctx, img, o.SessionID)
		vlmCalls++
		if t := strings.TrimSpace(res.Substance); t != "" && !strings.EqualFold(t, "NONE") {
			o.Body = t
		}
		n := recordWeChatSenderSightings(labeler, res.Messages, o.SessionID, sn.Timestamp)
		recorded += n
		log.Printf("[wechat-people] obs %d (session %s, snap %d): VLM read %d message(s), attributed %d non-self sender(s)", i, o.SessionID, sn.ID, len(res.Messages), n)
	}
	if wechatObs > 0 {
		log.Printf("[wechat-people] pass done: %d WeChat observation(s), %d VLM call(s), %d sender(s) attributed", wechatObs, vlmCalls, recorded)
	}
}

// visionCacheMaxEntries bounds the in-process VLM read cache. FIFO eviction:
// entries are a few hundred bytes each and the goal is only to absorb repeats
// within a work session, not to be a durable cache.
const visionCacheMaxEntries = 256

// visionResultCache dedupes VLM screenshot reads by exact image hash. The same
// static window re-captured across observations or sessions produces
// byte-identical crops; without this every semantic pass re-sends the same JPEG
// to the model. Exact-match only — a changed frame (new message, cursor moved)
// hashes differently and is read fresh.
type visionResultCache struct {
	mu      sync.Mutex
	wechat  map[string]wechatVisionResult
	generic map[string]string
	order   []string // "w:"/"g:"-prefixed keys in insertion order, for FIFO eviction
}

// visionImageKey returns the cache key for a cropped screenshot attachment.
func visionImageKey(img *agent.ImageAttachment) string {
	sum := sha256.Sum256([]byte(img.DataBase64))
	return hex.EncodeToString(sum[:])
}

func (vc *visionResultCache) evictLocked() {
	for len(vc.order) > visionCacheMaxEntries {
		oldest := vc.order[0]
		vc.order = vc.order[1:]
		delete(vc.wechat, strings.TrimPrefix(oldest, "w:"))
		delete(vc.generic, strings.TrimPrefix(oldest, "g:"))
	}
}

func (vc *visionResultCache) getWeChat(key string) (wechatVisionResult, bool) {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	res, ok := vc.wechat[key]
	return res, ok
}

func (vc *visionResultCache) putWeChat(key string, res wechatVisionResult) {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if vc.wechat == nil {
		vc.wechat = make(map[string]wechatVisionResult)
	}
	if _, exists := vc.wechat[key]; !exists {
		vc.order = append(vc.order, "w:"+key)
	}
	vc.wechat[key] = res
	vc.evictLocked()
}

func (vc *visionResultCache) getGeneric(key string) (string, bool) {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	text, ok := vc.generic[key]
	return text, ok
}

func (vc *visionResultCache) putGeneric(key, text string) {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if vc.generic == nil {
		vc.generic = make(map[string]string)
	}
	if _, exists := vc.generic[key]; !exists {
		vc.order = append(vc.order, "g:"+key)
	}
	vc.generic[key] = text
	vc.evictLocked()
}

// wechatSelfSenderMarkers are the labels the VLM is told to use for the
// user's own (right-aligned) bubbles. They must never become a Contact, so
// they're skipped before reaching the resolver.
var wechatSelfSenderMarkers = map[string]bool{
	"me": true, "myself": true, "我": true, "自己": true,
}

// recordWeChatSenderSightings attributes each VLM-read message to its sender
// via the labeler, which resolves the sender to a principal and records a
// sighting (so the person surfaces on the Who you know page). The user's own
// messages are skipped; the PlausibleAutoContactName guard inside the
// resolver discards any sender string that is message body rather than a
// name. Each message gets a distinct millisecond stamp (baseTs+index) so a
// sender's per-message count accumulates instead of colliding on a shared
// snapshot timestamp.
func recordWeChatSenderSightings(labeler *senderLabeler, msgs []wechatVisionMessage, sessionID string, baseTs int64) int {
	if labeler == nil {
		return 0
	}
	recorded := 0
	for n, m := range msgs {
		sender := strings.TrimSpace(m.Sender)
		if sender == "" || wechatSelfSenderMarkers[strings.ToLower(sender)] {
			continue
		}
		labeler.Label(context.Background(), "wechat", sender, m.Text, sessionID, baseTs+int64(n))
		recorded++
	}
	return recorded
}

// wechatInputTrigger marks a snapshot captured at a keyboard-input moment.
const wechatInputTrigger = "keyboard"

// representativeWeChatSnapshot picks the frame to read the active WeChat
// conversation from. It PREFERS the last keyboard-triggered frame (captured
// the moment the user typed/sent, so the conversation is on screen and the
// latest message is fresh), but falls back to the last frame with a usable
// image + focus rect regardless of trigger.
//
// The keyboard-only gate this replaced missed almost everything: WeChat
// snapshots are sparse and overwhelmingly arrive on click / visual_change /
// app_switch triggers, not keyboard, so requiring keyboard meant the VLM ran
// essentially never and no WeChat person ever reached the Who you know page. A
// usable focus rect (to crop to the window) and a readable file are still
// required — without them the crop would feed the VLM the wrong screen.
func representativeWeChatSnapshot(o *Observation, snaps []ActivitySnapshotRow) (ActivitySnapshotRow, bool) {
	if len(o.SnapshotIDs) == 0 || len(snaps) == 0 {
		return ActivitySnapshotRow{}, false
	}
	byID := make(map[int64]ActivitySnapshotRow, len(snaps))
	for _, s := range snaps {
		byID[s.ID] = s
	}
	var kb, any ActivitySnapshotRow
	var haveKB, haveAny bool
	for _, id := range o.SnapshotIDs {
		s, ok := byID[id]
		if !ok || !s.HasImage() {
			continue
		}
		// Require a usable focus rect: without it the crop falls back to the
		// full screen and the VLM reads whatever else is on screen (e.g. a
		// browser), misidentifying the WeChat conversation.
		if s.FocusRectW <= 0 || s.FocusRectH <= 0 {
			continue
		}
		any, haveAny = s, true
		if s.Trigger == wechatInputTrigger {
			kb, haveKB = s, true
		}
	}
	if haveKB {
		return kb, true
	}
	return any, haveAny
}

// cropFocusedWindowArea decodes the snapshot JPEG, crops to the focused window (via
// the persisted normalized focus rect), optionally drops the left sidebarFraction
// (WeChat's conversation list; pass 0 for terminals, which have no sidebar), and
// returns the result as a base64 image. Returns nil when there is no focus rect
// (cropping would otherwise hand the VLM the whole screen — other apps included —
// and misidentify the window) or on any decode failure.
func cropFocusedWindowArea(sn ActivitySnapshotRow, snapshotsDir string, sidebarFraction float64) *agent.ImageAttachment {
	if sn.PrivacyHeld || strings.TrimSpace(sn.FilePath) == "" {
		return nil
	}
	// No focus rect → can't isolate the window; skip rather than feed the full
	// screen to the VLM (the misidentified-as-other-app bug).
	if sn.FocusRectW <= 0 || sn.FocusRectH <= 0 {
		return nil
	}
	// file_path is stored relative to the snapshots dir (same as OCR / video read).
	path := sn.FilePath
	if snapshotsDir != "" && !filepath.IsAbs(path) {
		path = filepath.Join(snapshotsDir, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	src, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	b := src.Bounds()
	W, H := b.Dx(), b.Dy()
	x0, y0, x1, y1 := b.Min.X, b.Min.Y, b.Max.X, b.Max.Y
	if sn.FocusRectW > 0 && sn.FocusRectH > 0 {
		x0 = b.Min.X + int(sn.FocusRectX*float64(W))
		y0 = b.Min.Y + int(sn.FocusRectY*float64(H))
		x1 = b.Min.X + int((sn.FocusRectX+sn.FocusRectW)*float64(W))
		y1 = b.Min.Y + int((sn.FocusRectY+sn.FocusRectH)*float64(H))
	}
	// Drop the left sidebar (e.g. WeChat conversation list) to leave the main area.
	x0 += int(float64(x1-x0) * sidebarFraction)
	rect := image.Rect(x0, y0, x1, y1).Intersect(b)
	if rect.Dx() < 40 || rect.Dy() < 40 {
		return nil
	}
	dst := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(dst, dst.Bounds(), src, rect.Min, draw.Src)
	// Upscale a small crop so the title-bar contact name (small CJK glyphs in a
	// tiny right-bottom WeChat window) stays legible to the VLM — otherwise the
	// name pixelates and the model substitutes visually similar characters.
	var out image.Image = dst
	if rect.Dx() < 900 {
		out = upscaleImage2x(dst)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 85}); err != nil {
		return nil
	}
	return &agent.ImageAttachment{
		MediaType:  "image/jpeg",
		DataBase64: base64.StdEncoding.EncodeToString(buf.Bytes()),
	}
}

const wechatVisionPrompt = `This image is the MAIN CHAT AREA of WeChat (the left conversation list has been cropped out). WeChat exposes almost nothing to accessibility/OCR, so the screenshot is the only source — read the actual conversation from it.

Read, in the conversation's own language, and return a SINGLE JSON object:

{
  "substance": "1-3 plain sentences of what is actually being discussed: concrete topics, decisions, plans, questions. Empty string if no real conversation is visible.",
  "messages": [
    { "sender": "<who sent this bubble>", "text": "<the message text>" }
  ]
}

How to fill "sender" for each bubble, top to bottom:
- GROUP chat: every incoming message renders the sender's nickname ABOVE its bubble — copy that nickname VERBATIM. Read it character by character; Chinese nicknames often use UNCOMMON characters, so do NOT substitute a visually similar common character. If a nickname is genuinely unreadable, OMIT that message rather than guessing.
- 1:1 chat: there are no names above bubbles. Left/incoming bubbles are from the contact named in the TITLE BAR at the very top — use that exact title-bar name (same character-by-character rule).
- The USER'S OWN messages are right-aligned (bubble hugging the right edge). For every right-aligned bubble, set "sender" to exactly "me".
- WeChat clears the input box after sending, so the latest sent message is the bottom-most bubble, not the (possibly empty) input field.

Only include bubbles whose text and sender you can actually read. Include at most the 20 most recent messages. If no real conversation is visible (only a contact list, an empty chat, or app chrome), return {"substance":"","messages":[]}.

Output ONLY the JSON object, no preamble, no code fence.`

// wechatVisionResult is the structured read of a WeChat chat screenshot:
// a free-text substance summary (used as the observation Body, as before)
// plus per-bubble (sender, text) pairs used to attribute People sightings.
type wechatVisionResult struct {
	Substance string                `json:"substance"`
	Messages  []wechatVisionMessage `json:"messages"`
}

type wechatVisionMessage struct {
	Sender string `json:"sender"`
	Text   string `json:"text"`
}

// wechatVisionMaxMessages bounds how many bubbles we attribute per frame so a
// noisy read can't fan out into dozens of sightings from one screenshot.
const wechatVisionMaxMessages = 20

// describeWeChatScreenshot asks the multimodal light model to read a cropped
// WeChat chat screenshot and return its substance plus per-bubble senders.
// On a non-JSON response it degrades gracefully: the whole reply becomes the
// substance (the pre-structured-output behavior) and Messages stays empty.
func (c *ActivityTriager) describeWeChatScreenshot(ctx context.Context, img *agent.ImageAttachment, sessionID string) wechatVisionResult {
	if img == nil || !c.visionSessionAllowsModelRead(sessionID) {
		return wechatVisionResult{}
	}
	c.app.mu.Lock()
	runner := c.app.runner
	c.app.mu.Unlock()
	if runner == nil {
		return wechatVisionResult{}
	}
	cacheKey := visionImageKey(img)
	if res, ok := c.visionCache.getWeChat(cacheKey); ok {
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, wechatVisionTimeout)
	defer cancel()
	// A WeChat window goes as a picture: See them counts it.
	assistantCallScopeFrom(ctx).add("wechat", "", 1)

	var out strings.Builder
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:          agent.PriorityBackground,
		AutoApproveTools:  true,
		Prompt:            wechatVisionPrompt,
		Images:            []agent.ImageAttachment{*img},
		Model:             c.app.lightTaskModel(),
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		SuppressTelemetry: true,
	}, func(e *agent.StreamEvent) error {
		if e.Type == "assistant" {
			out.WriteString(e.Content)
		}
		return nil
	})
	if err != nil {
		return wechatVisionResult{}
	}
	raw := strings.TrimSpace(stripCodeFences(out.String()))
	if raw == "" {
		return wechatVisionResult{}
	}
	var res wechatVisionResult
	if err := unmarshalJSONBestEffort(raw, &res); err != nil {
		// Model ignored the JSON instruction and returned prose — keep the
		// old substance-only behavior so the Body enrichment still works.
		prose := wechatVisionResult{Substance: raw}
		c.visionCache.putWeChat(cacheKey, prose)
		return prose
	}
	if len(res.Messages) > wechatVisionMaxMessages {
		res.Messages = res.Messages[:wechatVisionMaxMessages]
	}
	c.visionCache.putWeChat(cacheKey, res)
	return res
}

// ── Generic vision fallback: apps whose text didn't come through accessibility ──
// Some apps expose almost nothing usable to accessibility/parsers, and OCR mangles
// their content (terminals: monospace → garbage; custom-rendered apps). For those
// the screenshot is the only reliable source — read it with a VLM, the approach
// AirJelly uses for every app. We trigger by BODY QUALITY (structured text too
// weak), NOT an app whitelist: apps with good ax (Slack/Gmail/...) are skipped
// automatically and any ax-poor app is covered without maintaining a list. WeChat
// keeps its specialized path (sidebar crop + title-bar name reading).

const genericVisionTimeout = 60 * time.Second

// maxWeakVisionPerGroup caps VLM calls for weak-body observations in one extraction
// group, so long continuous ax-poor work (e.g. terminals) can't blow up vision
// cost. AirJelly likewise caps keyframe count. Paired with collapseScrolling
// (already dedupes similar frames into observations) + even sampling across the
// group, this bounds cost while staying representative.
const maxWeakVisionPerGroup = 6

// minStructuredBodyRunes is the structured-text (parser body, else flat ax) length
// below which an observation is treated as too weak to extract from — its content
// didn't come through accessibility, so it needs a VLM read. Apps with real ax
// content (Slack/Gmail/...) clear this; terminals/custom apps (only a window title)
// fall under it.
const minStructuredBodyRunes = 40

// structuredBodyWeak reports whether the snapshot's structured text (parser body,
// else flat ax text) is too short to extract from. Deliberately IGNORES OCR: OCR is
// the unreliable fallback we're replacing (terminal OCR is garbage), so "structured
// text is thin" is exactly when a VLM read helps. Apps with rich ax (Slack/Gmail)
// have a long structured body and are skipped.
func structuredBodyWeak(sn ActivitySnapshotRow) bool {
	parsed := parseSnapshotContent(sn)
	body := ""
	if parsed != nil {
		body = strings.TrimSpace(parsed.Body)
	}
	if body == "" {
		body = strings.TrimSpace(sn.AXText)
	}
	return len([]rune(body)) < minStructuredBodyRunes
}

// enrichWeakObservations replaces the Body of observations whose structured text is
// too weak with content read from their screenshot by a VLM. No-op without a runner
// / usable frame; a failure or "no real content" leaves the body untouched. WeChat
// is handled separately by enrichWeChatObservations (specialized prompt + crop).
func (c *ActivityTriager) enrichWeakObservations(ctx context.Context, seq *ObservationSequence) {
	store := c.activityStore()
	if store == nil {
		return
	}
	snapshotsDir := store.SnapshotsDir()
	// Cache the per-session ID→snapshot map (built once per session) instead of
	// rebuilding it inside representativeVisionSnapshot for every observation.
	byIDCache := map[string]map[int64]ActivitySnapshotRow{}
	getSnapsByID := func(sid string) map[int64]ActivitySnapshotRow {
		if !c.visionSessionAllowsModelRead(sid) {
			return nil
		}
		if m, ok := byIDCache[sid]; ok {
			return m
		}
		snaps, err := store.GetSessionSnapshots(sid)
		if err != nil {
			snaps = nil
		}
		m := make(map[int64]ActivitySnapshotRow, len(snaps))
		for _, s := range snaps {
			m[s.ID] = s
		}
		byIDCache[sid] = m
		return m
	}
	// Collect observations whose structured text is too weak to extract from (skip
	// WeChat — specialized path — and ax-rich apps like Slack/Gmail). Then cap +
	// EVENLY sample to bound vision cost across long ax-poor stretches (e.g. a long
	// terminal session) while staying representative across the whole group.
	type weakCand struct {
		idx int
		sn  ActivitySnapshotRow
	}
	var cands []weakCand
	for i := range seq.Observations {
		o := &seq.Observations[i]
		if isWeChatObservation(*o) {
			continue // specialized path (sidebar crop + title-bar name reading)
		}
		sn, ok := representativeVisionSnapshot(o, getSnapsByID(o.SessionID))
		if !ok {
			continue
		}
		if !structuredBodyWeak(sn) {
			continue // ax/parser produced enough — no VLM needed (Slack/Gmail/...)
		}
		cands = append(cands, weakCand{i, sn})
	}
	candIdx := make([]int, len(cands))
	for i := range cands {
		candIdx[i] = i
	}
	for _, ci := range evenlySampleIndices(candIdx, maxWeakVisionPerGroup) {
		o := &seq.Observations[cands[ci].idx]
		if !c.visionSessionAllowsModelRead(o.SessionID) {
			continue
		}
		// Crop to the focused window (no sidebar fraction — generic apps have none).
		img := cropFocusedWindowArea(cands[ci].sn, snapshotsDir, 0)
		if img == nil {
			continue
		}
		text := c.describeScreenshotForVision(ctx, img, o.SessionID)
		if t := strings.TrimSpace(text); t != "" && !strings.EqualFold(t, "NONE") {
			o.Body = t
		}
	}
}

// evenlySampleIndices returns at most n entries from idx, spread evenly across the
// slice (so we sample moments across the whole group instead of just the first n).
func evenlySampleIndices(idx []int, n int) []int {
	if n <= 0 {
		return nil
	}
	if len(idx) <= n {
		return idx
	}
	out := make([]int, 0, n)
	step := float64(len(idx)) / float64(n)
	for i := range n {
		out = append(out, idx[int(float64(i)*step)])
	}
	return out
}

// representativeVisionSnapshot picks the LAST frame in the observation with a usable
// image + focus rect — the most recent state. Returns false when no frame has a
// focus rect (cropping would otherwise feed the whole screen to the VLM and
// misidentify the window).
func representativeVisionSnapshot(o *Observation, byID map[int64]ActivitySnapshotRow) (ActivitySnapshotRow, bool) {
	if len(o.SnapshotIDs) == 0 || len(byID) == 0 {
		return ActivitySnapshotRow{}, false
	}
	var pick ActivitySnapshotRow
	found := false
	for _, id := range o.SnapshotIDs {
		s, ok := byID[id]
		if !ok || !s.HasImage() {
			continue
		}
		if s.FocusRectW <= 0 || s.FocusRectH <= 0 {
			continue
		}
		pick, found = s, true
	}
	return pick, found
}

const genericVisionPrompt = `This image is the focused window of an app whose text content did NOT come through accessibility — the screenshot is the only reliable source (e.g. a terminal, or a custom-rendered app). Read what the user is actually doing or reading.

Report, in the content's own language:
- WHAT is happening: the work, commands, content, or conversation on screen, and the apparent goal or topic. For a terminal: commands run, files edited, tools used (git, build/compile, tests, ssh, an AI coding agent), errors.
- Concrete substance: names, commands, files, decisions, results or errors — not just "using an app".

Output 1-3 plain sentences of substance, no preamble. If nothing meaningful is visible (an idle/empty screen, only app chrome or a banner), output exactly: NONE`

// describeScreenshotForVision asks the multimodal light model to read a screenshot
// of an ax-poor app and return the substance of what's on screen.
func (c *ActivityTriager) describeScreenshotForVision(ctx context.Context, img *agent.ImageAttachment, sessionID string) string {
	if img == nil || !c.visionSessionAllowsModelRead(sessionID) {
		return ""
	}
	c.app.mu.Lock()
	runner := c.app.runner
	c.app.mu.Unlock()
	if runner == nil {
		return ""
	}
	cacheKey := visionImageKey(img)
	if text, ok := c.visionCache.getGeneric(cacheKey); ok {
		return text
	}
	ctx, cancel := context.WithTimeout(ctx, genericVisionTimeout)
	defer cancel()

	var out strings.Builder
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:          agent.PriorityBackground,
		AutoApproveTools:  true,
		Prompt:            genericVisionPrompt,
		Images:            []agent.ImageAttachment{*img},
		Model:             c.app.lightTaskModel(),
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		SuppressTelemetry: true,
	}, func(e *agent.StreamEvent) error {
		if e.Type == "assistant" {
			out.WriteString(e.Content)
		}
		return nil
	})
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(stripCodeFences(out.String()))
	// Cache only real answers (including the stable "NONE" verdict); an empty
	// response is indistinguishable from a transient failure.
	if text != "" {
		c.visionCache.putGeneric(cacheKey, text)
	}
	return text
}
