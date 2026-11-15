package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/mail"
	"strings"
	"time"

	"wails-gui/storage"
)

// todayNewMessageAddressWindow is how far back a Mail header can prove an
// address: a message sent to or received from that person.
const todayNewMessageAddressWindow = 90 * 24 * time.Hour

// TodayBriefDraftRoute is the recorded route of a new message to someone with
// no Mail thread: the card's person id, an address a record proves, where it
// came from, and the subject Isoform writes. A name the model wrote is never
// a lookup key, an address or permission to send.
type TodayBriefDraftRoute struct {
	PersonID string `json:"personId"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	// Source is "mail" (a header in the last 90 days) or "contacts" (the
	// person's Contacts card, linked through Who you know).
	Source   string `json:"source"`
	SourceAt int64  `json:"sourceAt,omitempty"`
	Subject  string `json:"subject"`
}

// TodayBriefDraft carries a saved, source-checked draft, its proven route and
// any prior send receipt.
type TodayBriefDraft struct {
	Answer      TodayPanelAnswer     `json:"answer"`
	State       string               `json:"state"`
	Route       TodayBriefDraftRoute `json:"route"`
	Subject     string               `json:"subject"`
	Destination string               `json:"destination,omitempty"`
}

func todayBriefDraftSendID(answerID string) string { return "today-brief-draft:" + answerID }

func validTodayDraftAddress(candidate string) bool {
	parsed, err := mail.ParseAddress(candidate)
	return err == nil && parsed.Address == candidate && !strings.ContainsAny(candidate, "\r\n") && len(candidate) <= 200
}

// todayProvenAddress returns the one address a record proves for a person id:
// the newest Mail header to or from them in the last 90 days, else the single
// email on their linked Contacts card. Without either there is no address.
func (a *App) todayProvenAddress(ctx context.Context, principalID string, now time.Time) (address, source string, at int64, ok bool) {
	if a == nil || a.store == nil || principalID == "" || a.IsClosedEyesModeEnabled() || !a.proactiveVoicePermission(ctx, mailPermissionKey) {
		return "", "", 0, false
	}
	rows, err := a.store.ListMailMessages(ctx, now.Add(-todayNewMessageAddressWindow).UnixMilli())
	if err != nil {
		return "", "", 0, false
	}
	for _, row := range rows {
		if row.PrincipalID != principalID || row.At > now.UnixMilli() || row.At <= at {
			continue
		}
		candidate := ""
		switch row.Mailbox {
		case storage.MailboxInbox:
			candidate = strings.TrimSpace(row.SenderEmail)
		case storage.MailboxSent:
			if lines := strings.Fields(row.To); len(lines) == 1 {
				candidate = lines[0]
			}
		}
		if validTodayDraftAddress(candidate) {
			address, at = candidate, row.At
		}
	}
	if address != "" {
		return address, "mail", at, true
	}
	// The person record in Who you know must be a Contacts card: imported from
	// it, or linked to it.
	person, err := a.store.GetPrincipal(ctx, principalID)
	if err != nil || person.ID != principalID {
		return "", "", 0, false
	}
	if person.Source != "system_contacts" || person.ExternalRef == "" {
		ref, err := a.store.LinkedContactRef(ctx, principalID)
		if err != nil || ref == "" {
			return "", "", 0, false
		}
	}
	handles, err := a.store.ListAppHandlesByPrincipal(ctx, principalID)
	if err != nil {
		return "", "", 0, false
	}
	for _, handle := range handles {
		candidate := strings.ToLower(strings.TrimSpace(handle.DisplayHandle))
		if handle.App != "email" || !validTodayDraftAddress(candidate) {
			continue
		}
		if address != "" && address != candidate {
			return "", "", 0, false
		}
		address = candidate
	}
	if address == "" {
		return "", "", 0, false
	}
	return address, "contacts", 0, true
}

// todayBriefDraftRoute binds a brief card with no waiting-reply source to its
// one person id and that person's proven address.
func (a *App) todayBriefDraftRoute(ctx context.Context, ref TodayChatContextRef) (TodayBriefDraftRoute, error) {
	var none TodayBriefDraftRoute
	if a == nil || a.store == nil || !ref.BriefDraft || ref.SourceKind != todayChatBriefCardKind || a.IsClosedEyesModeEnabled() {
		return none, errTodayChatItemUnavailable
	}
	now := time.Now()
	sources := a.collectTodayChatSources(ctx, now)
	brief, ok := a.todayChatDeliveredBriefFrom(ctx, ref.BriefID, &sources)
	if !ok {
		return none, errTodayChatItemUnavailable
	}
	card, ok := a.todayChatCardNow(ctx, brief, ref.SourceID, sources)
	if !ok || card.Mode != mobileBriefCardSend || card.Kind == mobileBriefCardOpener {
		return none, errTodayChatItemUnavailable
	}
	evidence := mobileBriefEvidenceByRef(brief)
	personID := ""
	for _, id := range card.Refs {
		source := evidence[id]
		switch source.SourceKind {
		case mobileBriefWaitingReplySource:
			// A reply goes through its Mail thread, never a new message.
			return none, errTodayChatItemUnavailable
		case "principal":
			if personID != "" && personID != source.SourceID {
				return none, errTodayChatItemUnavailable
			}
			personID = source.SourceID
		}
	}
	if personID == "" {
		return none, errTodayChatItemUnavailable
	}
	person, err := a.store.GetPrincipal(ctx, personID)
	name := strings.TrimSpace(person.DisplayName)
	if err != nil || person.ID != personID || person.Kind != storage.PrincipalKindContact || name == "" || len([]rune(name)) > 200 {
		return none, errTodayChatItemUnavailable
	}
	subject, err := a.todayChatCardSubject(ctx, brief, card, sources, now)
	if err != nil {
		return none, errTodayChatItemUnavailable
	}
	title := strings.TrimSpace(todayChatItemLabel(subject))
	if title == "" || len(title) > 998 || strings.ContainsAny(title, "\r\n") {
		return none, errTodayChatItemUnavailable
	}
	address, source, at, ok := a.todayProvenAddress(ctx, personID, now)
	if !ok {
		return none, errTodayChatItemUnavailable
	}
	return TodayBriefDraftRoute{PersonID: personID, Name: name, Address: address, Source: source, SourceAt: at, Subject: title}, nil
}

// GetTodayBriefDraftRoute tells Today whether a new-message draft chip may be
// offered: only when a record proves the person's address.
func (a *App) GetTodayBriefDraftRoute(ref TodayChatContextRef) (TodayBriefDraftRoute, error) {
	return a.todayBriefDraftRoute(context.Background(), ref)
}

func (a *App) GetTodayBriefDraft(ref TodayChatContextRef) (*TodayBriefDraft, error) {
	route, err := a.todayBriefDraftRoute(context.Background(), ref)
	if err != nil {
		return nil, err
	}
	answer, err := a.GetTodayPanelAnswer(ref)
	if err != nil || answer == nil {
		return nil, err
	}
	prior, err := a.store.LoadSingleOutboundSend(context.Background(), todayBriefDraftSendID(answer.AnswerItemID))
	if err != nil {
		return nil, err
	}
	out := &TodayBriefDraft{Answer: *answer, State: "ready", Route: route, Subject: route.Subject}
	if prior != nil {
		out.Answer.Answer = prior.Text
		out.Subject = prior.Subject
		out.Destination = prior.DraftTo
		switch prior.State {
		case storage.OutboundSendAccepted, storage.OutboundSendFinalized:
			out.State = "sent"
		default:
			out.State = "unknown"
		}
	}
	return out, nil
}

// SendTodayBriefDraft acts only on a saved source-checked draft and the route
// a record proves now. The person may edit the subject and text; the address
// is never taken from the caller. Send is the only permission to send.
func (a *App) SendTodayBriefDraft(ref TodayChatContextRef, answerID, subject, text string) error {
	err := a.sendTodayBriefDraft(ref, answerID, subject, text)
	if err == nil {
		noteBriefActed()
	}
	return err
}

func (a *App) sendTodayBriefDraft(ref TodayChatContextRef, answerID, subject, text string) error {
	if a == nil || a.store == nil {
		return errTodayChatItemUnavailable
	}
	mainChatProactiveActionMu.Lock()
	defer mainChatProactiveActionMu.Unlock()
	draft, err := a.GetTodayBriefDraft(ref)
	if err != nil || draft == nil || draft.Answer.AnswerItemID != answerID || draft.State != "ready" {
		return errTodayChatItemUnavailable
	}
	ctx := context.Background()
	subject, text = strings.TrimSpace(subject), strings.TrimSpace(text)
	if subject == "" {
		subject = draft.Route.Subject
	}
	if len(text) > mainChatEditedDraftMax {
		return errTodayChatItemUnavailable
	}
	if !a.proactiveVoicePermission(ctx, mailPermissionKey) {
		return errMailNotAllowed
	}
	route := draft.Route
	content := mainChatProactiveDraft{To: route.Address, Subject: subject, Text: text, Channel: "mail", Recipient: route.Name, PrincipalID: route.PersonID}
	content.Revision = mainChatDraftRevision(content)
	if !mainChatDraftSendable(&content) {
		return errTodayChatItemUnavailable
	}
	current, err := a.todayBriefDraftRoute(ctx, ref)
	if err != nil || current != route {
		return errTodayChatItemUnavailable
	}
	row := storage.OutboundSendRow{MomentID: todayBriefDraftSendID(answerID), Revision: content.Revision, Channel: "mail", PrincipalID: route.PersonID, Recipient: route.Name, DraftTo: route.Address, Subject: subject, Text: text, SourceKey: "brief:" + ref.BriefID + ":" + ref.SourceID}
	return a.sendTodayDraft(ctx, row)
}

func (a *App) sendTodayDraft(ctx context.Context, row storage.OutboundSendRow) error {
	claimed, ok, err := a.store.ClaimSingleOutboundSend(ctx, row, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if !ok {
		return errMainChatSendUnknown
	}
	if err := a.transportForMainChatSend().Send(ctx, claimed); err != nil {
		unknown := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
		var transportErr *proactiveSendTransportError
		if errors.As(err, &transportErr) {
			unknown = transportErr.unknown
		}
		var netErr net.Error
		unknown = unknown || errors.As(err, &netErr)
		if markErr := a.store.MarkOutboundSendFailed(ctx, claimed.MomentID, claimed.Revision, unknown); markErr != nil || unknown {
			return errMainChatSendUnknown
		}
		return err
	}
	if err := a.store.MarkOutboundSendAccepted(ctx, claimed.MomentID, claimed.Revision, time.Now().UnixMilli(), 0); err != nil {
		return errMainChatSendFinalize
	}
	return nil
}
