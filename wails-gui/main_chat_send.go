package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	netmail "net/mail"
	"regexp"
	"strings"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"

	"github.com/google/uuid"
	"github.com/slack-go/slack"
)

const (
	mainChatSendAction        = "send"
	mainChatSendFollowupAfter = 24 * time.Hour
)

var (
	errMainChatSendUnknown  = errors.New("send outcome is unknown; the message will not be sent again automatically")
	errMainChatSendFinalize = errors.New("send accepted; receipt pending")
	slackUserDestination    = regexp.MustCompile(`^[UW][A-Z0-9]{2,}$`)
	slackRoomDestination    = regexp.MustCompile(`^[CGD][A-Z0-9]{2,}$`)
)

type proactiveSendTransport interface {
	Send(context.Context, storage.OutboundSendRow) error
}

type systemProactiveSendTransport struct {
	app *App
}

type proactiveSendTransportError struct {
	err     error
	unknown bool
}

func (e *proactiveSendTransportError) Error() string { return e.err.Error() }
func (e *proactiveSendTransportError) Unwrap() error { return e.err }

func proactiveSendUnknown(err error) error {
	if err == nil {
		err = errMainChatSendUnknown
	}
	return &proactiveSendTransportError{err: err, unknown: true}
}

func mainChatDraftRevision(draft mainChatProactiveDraft) string {
	raw, _ := json.Marshal(struct {
		To, Subject, Text, Channel, PrincipalID string
	}{draft.To, draft.Subject, draft.Text, draft.Channel, draft.PrincipalID})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validMainChatDraftRecipient(draft mainChatProactiveDraft) bool {
	to := strings.TrimSpace(draft.To)
	if to == "" || to != draft.To || strings.ContainsAny(to, "\r\n") {
		return false
	}
	switch draft.Channel {
	case "mail":
		address, err := netmail.ParseAddress(to)
		return err == nil && address.Address == to
	case "slack":
		return slackUserDestination.MatchString(to) || slackRoomDestination.MatchString(to)
	default:
		return false
	}
}

func mainChatDraftSendable(draft *mainChatProactiveDraft) bool {
	if draft == nil || strings.TrimSpace(draft.Text) == "" || strings.TrimSpace(draft.Recipient) == "" ||
		len([]rune(draft.Recipient)) > 200 || strings.TrimSpace(draft.PrincipalID) == "" ||
		draft.Revision == "" || draft.Revision != mainChatDraftRevision(*draft) || !validMainChatDraftRecipient(*draft) {
		return false
	}
	switch draft.Channel {
	case "mail":
		return strings.TrimSpace(draft.Subject) != "" && !strings.ContainsAny(draft.Subject, "\r\n") && len(draft.Subject) <= 998 && len(draft.Text) <= 256*1024
	case "slack":
		return len(draft.Text) <= 40*1024
	default:
		return false
	}
}

func setMainChatSendAction(item *mainChatProactive, enabled bool) {
	ids := item.ActionIDs[:0]
	for _, id := range item.ActionIDs {
		if id != mainChatSendAction {
			ids = append(ids, id)
		}
	}
	item.ActionIDs = ids
	delete(item.ActionLabels, mainChatSendAction)
	for lang, copy := range item.Localized {
		delete(copy.ActionLabels, mainChatSendAction)
		item.Localized[lang] = copy
	}
	if !enabled {
		return
	}
	insert := len(item.ActionIDs)
	for i, id := range item.ActionIDs {
		if id == "open" {
			insert = i
			break
		}
	}
	item.ActionIDs = append(item.ActionIDs, "")
	copy(item.ActionIDs[insert+1:], item.ActionIDs[insert:])
	item.ActionIDs[insert] = mainChatSendAction
	for lang, copy := range item.Localized {
		catalog, _ := engagement.Copy(engagement.Kind(item.Kind), lang)
		if copy.ActionLabels == nil {
			copy.ActionLabels = map[string]string{}
		}
		copy.ActionLabels[mainChatSendAction] = catalog.Actions[mainChatSendAction]
		item.Localized[lang] = copy
	}
	item.ActionLabels = item.Localized["en"].ActionLabels
	if localized, ok := item.Localized["zh-CN"]; ok && item.Headline != "" {
		catalog, _ := engagement.Copy(engagement.Kind(item.Kind), "zh-CN")
		if item.Headline == catalog.PushBody {
			item.ActionLabels = localized.ActionLabels
		}
	}
}

func populateMainChatSendDraft(item *mainChatProactive, moment engagement.Moment, source proactiveVoiceSource) {
	if item.DraftContent == nil || moment.Kind != engagement.KindWaitingOnReply {
		setMainChatSendAction(item, false)
		return
	}
	item.DraftContent.Channel = "mail"
	item.DraftContent.PrincipalID = moment.SubjectID
	item.DraftContent.Recipient = ""
	for _, person := range source.People {
		if person.Ref == "p1" && person.Name != nil {
			item.DraftContent.Recipient = strings.TrimSpace(*person.Name)
			break
		}
	}
	item.DraftContent.Revision = mainChatDraftRevision(*item.DraftContent)
	setMainChatSendAction(item, mainChatDraftSendable(item.DraftContent))
}

func (a *App) mainChatSendSource(ctx context.Context, card *mainChatProactive) (string, error) {
	momentID, err := a.mainChatReceiptMomentID(ctx, card.MomentID)
	if err != nil {
		return "", err
	}
	moment, err := a.store.GetEngagementMoment(ctx, momentID)
	if err != nil || moment == nil {
		return "", errors.New("the original message source is unavailable")
	}
	if moment.Kind != card.Kind || moment.SubjectID != card.DraftContent.PrincipalID {
		return "", errors.New("the original message source does not match this draft")
	}
	return moment.DedupeKey, nil
}

func (a *App) validateMainChatSend(ctx context.Context, card *mainChatProactive) error {
	if card == nil || !card.Draft || !mainChatDraftSendable(card.DraftContent) || card.VoiceReceipt == "" {
		return errors.New("this draft cannot be sent")
	}
	if !a.proactiveVoiceReceiptAllowsRead(ctx, card.VoiceReceipt, card.Kind, proactiveVoicePresentationHash(*card), false) {
		return errors.New("the draft source is no longer available")
	}
	return nil
}

func outboundRowForMainChatDraft(card *mainChatProactive, sourceKey string) storage.OutboundSendRow {
	draft := card.DraftContent
	return storage.OutboundSendRow{
		MomentID: card.MomentID, Revision: draft.Revision, Channel: draft.Channel,
		PrincipalID: draft.PrincipalID, Recipient: draft.Recipient, DraftTo: draft.To,
		Subject: draft.Subject, Text: draft.Text, SourceKey: sourceKey,
	}
}

func (a *App) transportForMainChatSend() proactiveSendTransport {
	if a.proactiveSendTransport != nil {
		return a.proactiveSendTransport
	}
	return systemProactiveSendTransport{app: a}
}

// mainChatEditedDraftMax bounds the words a person may send in place of a draft.
const mainChatEditedDraftMax = 16 * 1024

// resolveMainChatSendText sends the draft, or the person's own edit of it
// (Phone Threads §4, Edit on the draft card). An edit is the person's words and
// needs no source provenance, but the draft they edited must still be sendable:
// its source readable and its card current. The edit becomes its own revision,
// so it can never replay or be confused with the generated draft's send.
func (a *App) resolveMainChatSendText(ctx context.Context, row *storage.StreamItemRow, root *mainChatProactive, card *mainChatProactive, text string) (string, error) {
	draft := card.DraftContent
	if draft == nil || draft.Revision == "" {
		return "", errors.New("this draft cannot be sent")
	}
	text = strings.TrimSpace(text)
	validated := false
	if text != "" && text != strings.TrimSpace(draft.Text) {
		if len(text) > mainChatEditedDraftMax {
			return "", errors.New("the reply is too long to send")
		}
		switch card.State {
		case "acted", "dismissed", "deferred", "expired", "muted":
			return "", errors.New("proactive message was already handled")
		}
		if err := a.validateMainChatSend(ctx, card); err != nil {
			return "", err
		}
		validated = true
		revised := *draft
		sum := sha256.Sum256([]byte(text))
		revised.Text = text
		revised.Revision = draft.Revision + "-edit-" + hex.EncodeToString(sum[:6])
		card.DraftContent = &revised
		draft = card.DraftContent
	}
	stored, err := a.store.LoadOutboundSend(ctx, card.MomentID, draft.Revision)
	if err != nil {
		return "", err
	}
	if stored != nil {
		switch stored.State {
		case storage.OutboundSendFinalized:
			return storage.EngagementReceiptActed, nil
		case storage.OutboundSendAccepted:
			return a.mainChatSendTaken(ctx, row, root, card, *stored, true), nil
		case storage.OutboundSendSending, storage.OutboundSendUnknown:
			return "", errMainChatSendUnknown
		}
	}
	switch card.State {
	case "acted", "dismissed", "deferred", "expired", "muted":
		return "", errors.New("proactive message was already handled")
	}
	if !validated {
		if err := a.validateMainChatSend(ctx, card); err != nil {
			return "", err
		}
	}
	sourceKey, err := a.mainChatSendSource(ctx, card)
	if err != nil {
		return "", err
	}
	attempt := outboundRowForMainChatDraft(card, sourceKey)
	claimed, claim, err := a.store.ClaimOutboundSend(ctx, attempt, time.Now().UnixMilli())
	if err != nil {
		return "", err
	}
	if !claim {
		switch claimed.State {
		case storage.OutboundSendAccepted:
			return a.mainChatSendTaken(ctx, row, root, card, claimed, true), nil
		case storage.OutboundSendFinalized:
			return storage.EngagementReceiptActed, nil
		default:
			return "", errMainChatSendUnknown
		}
	}
	if err := a.transportForMainChatSend().Send(ctx, claimed); err != nil {
		unknown := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
		var transportErr *proactiveSendTransportError
		if errors.As(err, &transportErr) {
			unknown = transportErr.unknown
		}
		var netErr net.Error
		unknown = unknown || errors.As(err, &netErr)
		if markErr := a.store.MarkOutboundSendFailed(ctx, claimed.MomentID, claimed.Revision, unknown); markErr != nil {
			return "", fmt.Errorf("send failed and its outcome could not be recorded: %w", markErr)
		}
		if unknown {
			return "", errMainChatSendUnknown
		}
		return "", err
	}
	sentAt := time.Now()
	followupAt := a.mainChatFollowupTime(ctx, claimed, sentAt)
	accepted := claimed
	accepted.State = storage.OutboundSendAccepted
	accepted.SentAt = sentAt.UnixMilli()
	accepted.FollowupAt = followupAt.UnixMilli()
	recorded := a.store.MarkOutboundSendAccepted(ctx, claimed.MomentID, claimed.Revision, accepted.SentAt, accepted.FollowupAt) == nil
	return a.mainChatSendTaken(ctx, row, root, card, accepted, recorded), nil
}

// mainChatSendReceiptRetryDelays space the quiet background saves of a sent
// message's receipt.
var mainChatSendReceiptRetryDelays = []time.Duration{2 * time.Second, 15 * time.Second, time.Minute, 5 * time.Minute}

const errMainChatSendReceiptUnsaved codeError = "a sent message's receipt could not be saved"

// mainChatSendTaken is the result once Mail or Slack has taken the message:
// it was sent. The receipt is saved apart from the send (Chat review §2), so a
// receipt that can't be saved never reads as a failed send and never invites a
// second email. It saves quietly in the background and is logged if it still
// can't. recorded is false when the ledger itself didn't take the acceptance.
func (a *App) mainChatSendTaken(ctx context.Context, row *storage.StreamItemRow, root *mainChatProactive, card *mainChatProactive, send storage.OutboundSendRow, recorded bool) string {
	if recorded {
		if _, err := a.finalizeMainChatSend(ctx, row, root, card, send); err == nil {
			return storage.EngagementReceiptActed
		}
	}
	a.retryMainChatSendReceipt(send)
	return storage.EngagementReceiptActed
}

func (a *App) retryMainChatSendReceipt(send storage.OutboundSendRow) {
	delays := append([]time.Duration(nil), mainChatSendReceiptRetryDelays...)
	var done <-chan struct{}
	if a.ctx != nil {
		done = a.ctx.Done()
	}
	go func() {
		var err error = errMainChatSendReceiptUnsaved
		for _, delay := range delays {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-done:
				timer.Stop()
				return
			}
			if err = a.saveMainChatSendReceipt(send); err == nil {
				return
			}
		}
		log.Printf("[main-chat] %s: %s", errMainChatSendReceiptUnsaved, safeLogError(err))
	}()
}

// saveMainChatSendReceipt records an accepted send's receipt from the ledger
// row and the stored card; it never calls the transport again.
func (a *App) saveMainChatSendReceipt(send storage.OutboundSendRow) error {
	if a == nil || a.store == nil {
		return errMainChatSendReceiptUnsaved
	}
	mainChatProactiveActionMu.Lock()
	defer mainChatProactiveActionMu.Unlock()
	ctx := context.Background()
	stored, err := a.store.LoadOutboundSend(ctx, send.MomentID, send.Revision)
	if err != nil {
		return withLogStage("load send", err)
	}
	if stored == nil {
		return withLogStage("load send", sql.ErrNoRows)
	}
	switch stored.State {
	case storage.OutboundSendFinalized:
		return nil
	case storage.OutboundSendSending:
		if err := a.store.MarkOutboundSendAccepted(ctx, send.MomentID, send.Revision, send.SentAt, send.FollowupAt); err != nil {
			return withLogStage("mark accepted", err)
		}
		stored.State, stored.SentAt, stored.FollowupAt = storage.OutboundSendAccepted, send.SentAt, send.FollowupAt
	case storage.OutboundSendAccepted:
	default:
		return withLogStage("load send", errMainChatSendUnknown)
	}
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return withLogStage("main chat", err)
	}
	row, err := a.store.FindProactiveStreamItem(ctx, main.LocalID, send.MomentID)
	if err != nil {
		return withLogStage("load card", err)
	}
	if row == nil {
		return withLogStage("load card", sql.ErrNoRows)
	}
	var root mainChatProactive
	if err := json.Unmarshal([]byte(row.ToolInput), &root); err != nil {
		return withLogStage("decode card", errMainChatSendReceiptUnsaved)
	}
	normalizeMainChatProactiveActions(&root)
	card := &root
	if card.MomentID != send.MomentID {
		card = nil
		for i := range root.Items {
			if root.Items[i].MomentID == send.MomentID {
				card = &root.Items[i]
				break
			}
		}
	}
	if card == nil || card.DraftContent == nil {
		return withLogStage("load card", sql.ErrNoRows)
	}
	if card.DraftContent.Revision != send.Revision {
		// The person's edit went: the card keeps the words that were sent.
		revised := *card.DraftContent
		revised.Text, revised.Revision = stored.Text, stored.Revision
		card.DraftContent = &revised
	}
	_, err = a.finalizeMainChatSend(ctx, row, &root, card, *stored)
	return withLogStage("finalize", err)
}

func mainChatSendReceiptID(momentID, revision string) string {
	sum := sha256.Sum256([]byte("isoform:message-send\x00" + momentID + "\x00" + revision))
	return "message-send:" + hex.EncodeToString(sum[:16])
}

func mainChatSendChannelLabel(channel string) string {
	if channel == "slack" {
		return "Slack"
	}
	return "Mail"
}

func (a *App) postMainChatSendReceipt(ctx context.Context, send storage.OutboundSendRow) (string, error) {
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return "", err
	}
	id := mainChatSendReceiptID(send.MomentID, send.Revision)
	mainChatPostMu.Lock()
	defer mainChatPostMu.Unlock()
	existing, err := a.store.LoadStreamItem(ctx, id)
	if err != nil {
		return "", err
	}
	if existing != nil {
		return existing.ID, nil
	}
	sentAt := time.UnixMilli(send.SentAt)
	report := mainChatReport{
		Source: mainChatSourceMessageSend, Key: mainChatReportKey(mainChatSourceMessageSend, send.MomentID, send.Revision),
		Target: mainChatTarget{Kind: mainChatTargetNone}, Channel: send.Channel, SentAt: send.SentAt, PrincipalID: send.PrincipalID,
		Evidence: []mainChatEvidenceRef{{Kind: "principal", ID: send.PrincipalID, Target: mainChatTarget{Kind: "person", ID: send.PrincipalID}}},
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	content := fmt.Sprintf("Sent via %s, %s", mainChatSendChannelLabel(send.Channel), sentAt.Local().Format("3:04 PM"))
	storedID := a.appendMainChatItem(ctx, main.LocalID, storage.StreamItemRow{
		ID: id, Kind: "system", Content: content, ToolName: mainChatReportTool, ToolInput: string(payload), CreatedAt: send.SentAt,
	}, mainChatSourceMessageSend)
	if storedID == "" {
		return "", errors.New("the sent receipt could not be stored")
	}
	return storedID, nil
}

func mainChatFollowupDedupeKey(send storage.OutboundSendRow) string {
	return engagement.DedupeKey(engagement.KindReminder, "outbound", mainChatSendReceiptID(send.MomentID, send.Revision))
}

func (a *App) scheduleMainChatFollowup(ctx context.Context, send storage.OutboundSendRow) error {
	if a.engagement == nil || send.FollowupAt <= 0 {
		return nil
	}
	dedupeKey := mainChatFollowupDedupeKey(send)
	seen, err := a.store.EngagementMomentKeyExists(ctx, dedupeKey)
	if err != nil || seen {
		return err
	}
	due := time.UnixMilli(send.FollowupAt)
	title := "Follow up on your sent message"
	if a.currentUILanguage() == "zh-CN" {
		title = "跟进你发出的消息"
	}
	_, err = a.engagement.Submit(ctx, engagement.Moment{
		Kind: engagement.KindReminder, SubjectID: send.PrincipalID, DedupeKey: dedupeKey,
		CreatedAt: time.UnixMilli(send.SentAt), NotBefore: due, ExpiresAt: due.Add(7 * 24 * time.Hour), Stakes: 0.5,
		Params:   map[string]string{"title": title},
		Channels: []engagement.Channel{engagement.ChannelDesktop, engagement.ChannelPush},
	})
	return err
}

func (a *App) retireMainChatSendSource(ctx context.Context, card *mainChatProactive) error {
	momentID, err := a.mainChatReceiptMomentID(ctx, card.MomentID)
	if err != nil {
		return err
	}
	moment, err := a.store.GetEngagementMoment(ctx, momentID)
	if err != nil || moment == nil {
		return err
	}
	return a.store.SetEngagementMomentState(ctx, moment.ID, storage.EngagementStateSuppressed, "message_sent", storage.EngagementMomentRow{})
}

func (a *App) finalizeMainChatSend(ctx context.Context, row *storage.StreamItemRow, root *mainChatProactive, card *mainChatProactive, send storage.OutboundSendRow) (state string, err error) {
	defer func() {
		if err != nil && (send.State == storage.OutboundSendAccepted || send.State == storage.OutboundSendFinalized) {
			state = ""
			err = fmt.Errorf("%w: %v", errMainChatSendFinalize, err)
		}
	}()
	if send.State != storage.OutboundSendAccepted && send.State != storage.OutboundSendFinalized {
		return "", errMainChatSendUnknown
	}
	if err := a.scheduleMainChatFollowup(ctx, send); err != nil {
		return "", err
	}
	if _, err := a.postMainChatSendReceipt(ctx, send); err != nil {
		return "", err
	}
	if err := a.recordMainChatDelivery(*card, row.CreatedAt); err != nil {
		return "", err
	}
	if err := a.recordMainChatCardReceipt(ctx, card, storage.EngagementCompletionChannel, storage.EngagementReceiptActed, storage.EngagementCompletionAction); err != nil {
		return "", err
	}
	if err := a.retireMainChatSendSource(ctx, card); err != nil {
		return "", err
	}
	card.State = storage.EngagementReceiptActed
	card.SentAt = send.SentAt
	if root.Kind == "digest" && mainChatProactiveRowClass(*root, time.Now()) != MainChatRowWaiting {
		root.State = storage.EngagementReceiptActed
	}
	payload, err := json.Marshal(root)
	if err != nil {
		return "", err
	}
	if err := a.store.UpdateStreamItemToolInput(ctx, row.ID, string(payload)); err != nil {
		return "", err
	}
	if err := a.store.MarkOutboundSendFinalized(ctx, send.MomentID, send.Revision); err != nil {
		return "", err
	}
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return "", err
	}
	a.mainChatRowsChanged(main.LocalID)
	a.emitBrowserEvent(mainChatItemsAppendedEvent, map[string]any{"sessionId": main.LocalID, "itemId": row.ID, "kind": "system", "source": mainChatSourceEngagement})
	a.emitBrowserEvent("people:loops-changed")
	a.emitBrowserEvent("topics:updated")
	return storage.EngagementReceiptActed, nil
}

func (t systemProactiveSendTransport) Send(ctx context.Context, send storage.OutboundSendRow) error {
	switch send.Channel {
	case "mail":
		return t.app.sendMainChatMail(ctx, send)
	case "slack":
		return t.app.sendMainChatSlack(ctx, send)
	default:
		return errors.New("unsupported message channel")
	}
}

func (a *App) sendMainChatSlack(ctx context.Context, send storage.OutboundSendRow) error {
	if !validMainChatDraftRecipient(mainChatProactiveDraft{To: send.DraftTo, Channel: "slack"}) || strings.TrimSpace(send.Text) == "" {
		return errors.New("invalid Slack draft")
	}
	credentials := loadSlackLocalCredentials()
	if credentials == nil {
		return errors.New("local Slack is not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	api := slack.New(credentials.BotToken, slack.OptionAppLevelToken(credentials.AppToken))
	destination := send.DraftTo
	if slackUserDestination.MatchString(destination) {
		conversation, _, _, err := api.OpenConversationContext(ctx, &slack.OpenConversationParameters{Users: []string{destination}})
		if err != nil {
			return classifySlackSendError(ctx, err)
		}
		if conversation == nil || conversation.ID == "" {
			return errors.New("Slack did not open a conversation")
		}
		destination = conversation.ID
	}
	_, _, err := api.PostMessageContext(ctx, destination, slack.MsgOptionText(send.Text, false))
	return classifySlackSendError(ctx, err)
}

func classifySlackSendError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return proactiveSendUnknown(ctx.Err())
	}
	var statusErr interface{ HTTPStatusCode() int }
	if errors.As(err, &statusErr) {
		code := statusErr.HTTPStatusCode()
		if code == http.StatusTooManyRequests || code >= http.StatusInternalServerError {
			return proactiveSendUnknown(err)
		}
		return err
	}
	var rateLimited *slack.RateLimitedError
	if errors.As(err, &rateLimited) {
		return proactiveSendUnknown(err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return proactiveSendUnknown(err)
	}
	return err
}

// SendMainChat sends words to the existing main chat with no topic and no
// Today context attached, the way the main chat's own box does: the user row
// is stored first, then the run starts. It never creates or selects a
// session. Today's panel sends here while no topic is open.
func (a *App) SendMainChat(text string) (MainChatTopicSendResult, error) {
	return a.sendMainChat(text, promptOriginMac)
}

// AskMainChat answers a panel question without granting tool authority.
func (a *App) AskMainChat(text string) (MainChatTopicSendResult, error) {
	return a.sendMainChat(text, promptOriginPanelAsk)
}

// AskMainChatFromTodayChip sends an unbound first-day starter with bounded telemetry.
func (a *App) AskMainChatFromTodayChip(text string) (MainChatTopicSendResult, error) {
	return a.sendMainChat(text, promptOriginPanelAsk, "T15")
}

// SendMainChatFromTodayChip sends an unchanged panel answer chip.
func (a *App) SendMainChatFromTodayChip(text string) (MainChatTopicSendResult, error) {
	return a.sendMainChat(text, promptOriginMac, "T68")
}

// SendMainChatWithAttachments sends the Today panel's box with its attached
// files and images, as the Chat page's box does (chat-review v1.0.3 #4).
func (a *App) SendMainChatWithAttachments(text string, images []ImageData, files []FileData) (MainChatTopicSendResult, error) {
	return a.sendMainChatAttached(text, images, files, promptOriginMac)
}

func (a *App) sendMainChat(text string, origin promptOrigin, telemetryAction ...string) (MainChatTopicSendResult, error) {
	return a.sendMainChatAttached(text, nil, nil, origin, telemetryAction...)
}

func (a *App) sendMainChatAttached(text string, images []ImageData, files []FileData, origin promptOrigin, telemetryAction ...string) (MainChatTopicSendResult, error) {
	return a.sendMainChatAs(uuid.NewString(), text, images, files, origin, nil, telemetryAction...)
}

// sendMainChatAs is sendMainChatAttached with the user row's id chosen by the
// caller (Quick chat keys it by its request, so a retry stores it once) and
// an optional step run once the row is admitted, before the prompt is
// composed: no other turn on the main chat can run in between.
func (a *App) sendMainChatAs(id, text string, images []ImageData, files []FileData, origin promptOrigin, admitted func(), telemetryAction ...string) (MainChatTopicSendResult, error) {
	var result MainChatTopicSendResult
	if a == nil || a.store == nil || strings.TrimSpace(text) == "" || len(text) > 64<<10 {
		return result, errTodayChatItemUnavailable
	}
	ctx := context.Background()
	main, err := a.store.LoadMainSession(ctx)
	if err != nil {
		return result, err
	}
	if main == nil {
		return result, errTodayChatItemUnavailable
	}
	model := main.Model
	if model == "" {
		model = a.chatTaskModel()
	}
	chat, exists, err := a.store.MobileChat(ctx, main.LocalID)
	if err != nil {
		return result, err
	}
	if !exists {
		return result, errTodayChatItemUnavailable
	}
	workDir, err := mobileChatWorkDir(chat)
	if err != nil {
		return result, err
	}
	order := -1
	admit := func(turnCtx context.Context) error {
		if len(telemetryAction) > 0 {
			setTodayTurnSource(turnCtx, telemetryAction[0])
		}
		ctx, cancel := context.WithTimeout(turnCtx, 10*time.Second)
		defer cancel()
		revision, err := a.store.ReadRevision(ctx)
		if err != nil {
			return err
		}
		order, err = a.store.AppendUserStreamItemAtomicIfCurrent(ctx, main.LocalID, storage.StreamItemRow{ID: id, Kind: "user", Content: text}, revision, func() error { return nil })
		if err != nil {
			return err
		}
		a.mainChatRowsChanged(main.LocalID)
		a.scheduleOpenAnswerAskPromotion(main.LocalID, id, text)
		a.emitBrowserEvent(mainChatItemsAppendedEvent, map[string]any{"sessionId": main.LocalID, "itemId": id, "sortOrder": order, "kind": "user", "source": "today"})
		_ = a.observeCommunicationMessage(communicationMessage{Source: "isoform_chat", App: "Isoform", MessageID: id, AuthorID: "owner", OwnerID: "owner", Text: text, SentAt: time.Now(), BodyAvailable: true})
		if admitted != nil {
			admitted()
		}
		return nil
	}
	prompt := text
	if len(images) > 0 || len(files) > 0 {
		prompt = attachmentPrompt(text, images, files, workDir)
	}
	if err := a.executePromptOwned(prompt, text, workDir, model, main.SessionID, main.LocalID, images, main.PlanMode, main.ThinkingEnabled, "", nil, nil, "", "agentloop.user.send_clicked", origin, nil, admit); err != nil {
		return result, err
	}
	return MainChatTopicSendResult{SessionID: main.LocalID, UserItemID: id, SortOrder: order}, nil
}
