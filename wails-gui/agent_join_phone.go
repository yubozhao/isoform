package main

// Agents joining from the phone, and agents disconnected lately
// (phone-agent v1.2.0 rules 9 and 11, and §6's Connect again; agents-join
// v1.0.0 §2; PROTOCOL.md 27.4). The phone makes the join link and checks the
// agent's code on its own sheet; the computer holds the one open join and
// shows it too, and the first answer wins. When the agent answers, the phone
// that made the link is told once, within the link's ten minutes: it
// finishes the person's own action and isn't a kind of its own.

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/agentjoin"
)

const mobileNoticeKindAgentJoin = "agent_join"

var (
	errPhoneJoinUnknown  = errors.New("that isn't the open join")
	errPhoneJoinExpired  = errors.New("the join link ran out")
	errPhoneJoinNotAsked = errors.New("no agent is asking to join yet")
)

// phoneAgentJoin is the join a phone made: one at a time, like the join.
type phoneAgentJoin struct {
	JoinID    string
	DeviceID  string
	ExpiresAt int64
	Reconnect string
	Notified  bool
}

// mobileAgentJoinView is a join as the phone reads it.
type mobileAgentJoinView struct {
	JoinID    string `json:"joinId"`
	State     string `json:"state"`
	ExpiresAt int64  `json:"expiresAt"`
	AgentName string `json:"agentName,omitempty"`
	From      string `json:"from,omitempty"`
	ToOpen    string `json:"toOpen,omitempty"`
	Code      string `json:"code,omitempty"`
	AgentID   string `json:"agentId,omitempty"`
}

// startPhoneAgentJoin makes a join link from a phone.
func (a *App) startPhoneAgentJoin(deviceID, reconnect string) (AgentJoinStart, string, error) {
	start, err := a.startExistingAgentJoin(true)
	if err != nil {
		return AgentJoinStart{}, "", err
	}
	joinID := uuid.NewString()
	a.agentJoin.phoneMu.Lock()
	a.agentJoin.phone = &phoneAgentJoin{JoinID: joinID, DeviceID: deviceID, ExpiresAt: start.ExpiresAt, Reconnect: reconnect}
	a.agentJoin.phoneMu.Unlock()
	return start, joinID, nil
}

// phoneJoinNow is the phone's join and the service's open one, when they
// are the same join.
func (a *App) phoneJoinNow(joinID string) (phoneAgentJoin, *agentjoin.Join, bool) {
	a.agentJoin.phoneMu.Lock()
	p := a.agentJoin.phone
	var copied phoneAgentJoin
	if p != nil {
		copied = *p
	}
	a.agentJoin.phoneMu.Unlock()
	if p == nil || copied.JoinID != joinID {
		return phoneAgentJoin{}, nil, false
	}
	var current *agentjoin.Join
	if svc := a.agentJoin.svc; svc != nil {
		current = svc.Current()
	}
	if current != nil && current.ExpiresAt != copied.ExpiresAt {
		// A newer join, made on the computer, closed this one.
		current = nil
	}
	return copied, current, true
}

// phoneAgentJoinView is the join for the phone's code sheet.
func (a *App) phoneAgentJoinView(joinID string) (mobileAgentJoinView, bool) {
	p, j, ok := a.phoneJoinNow(joinID)
	if !ok {
		return mobileAgentJoinView{}, false
	}
	view := mobileAgentJoinView{JoinID: p.JoinID, State: agentjoin.StateExpired, ExpiresAt: p.ExpiresAt}
	if j == nil {
		return view, true
	}
	view.State = j.State
	switch j.State {
	case agentjoin.StateRequested:
		view.AgentName, view.From, view.ToOpen, view.Code = j.Name, j.Device, j.MayOpen, j.Check
	case agentjoin.StateAllowed:
		view.AgentName, view.From, view.ToOpen, view.AgentID = j.Name, j.Device, j.MayOpen, j.AgentID
	}
	return view, true
}

// answerPhoneAgentJoin is the phone's Yes, it shows the code, or No.
func (a *App) answerPhoneAgentJoin(joinID string, yes bool) (mobileAgentJoinView, error) {
	view, ok := a.phoneAgentJoinView(joinID)
	switch {
	case !ok:
		return view, errPhoneJoinUnknown
	case view.State == agentjoin.StateExpired:
		return view, errPhoneJoinExpired
	case view.State != agentjoin.StateRequested:
		return view, errPhoneJoinNotAsked
	}
	if err := a.AnswerAgentJoin(yes); err != nil {
		if errors.Is(err, agentjoin.ErrNoJoin) {
			after, _ := a.phoneAgentJoinView(joinID)
			return after, errPhoneJoinNotAsked
		}
		return view, err
	}
	after, _ := a.phoneAgentJoinView(joinID)
	return after, nil
}

// notePhoneAgentJoinChanged tells the phone that made the join, once, that
// an agent is asking, while the link still works.
func (a *App) notePhoneAgentJoinChanged() {
	svc := a.agentJoin.svc
	if svc == nil {
		return
	}
	j := svc.Current()
	now := time.Now()
	a.agentJoin.phoneMu.Lock()
	p := a.agentJoin.phone
	if p == nil || p.Notified || j == nil || j.ExpiresAt != p.ExpiresAt || j.State != agentjoin.StateRequested || now.UnixMilli() >= p.ExpiresAt {
		a.agentJoin.phoneMu.Unlock()
		return
	}
	p.Notified = true
	copied := *p
	a.agentJoin.phoneMu.Unlock()
	if err := a.publishAgentJoinNotice(copied, j.Name, now); err != nil {
		log.Printf("[agents] the phone wasn't told an agent is asking: %v", err)
	}
}

func (a *App) publishAgentJoinNotice(p phoneAgentJoin, agentName string, now time.Time) error {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return errMobileRelayOffline
	}
	var devices []mobileDeviceRecord
	for _, device := range bridge.devicesWithScope(mobileScopePush) {
		if device.DeviceID == p.DeviceID {
			devices = append(devices, device)
		}
	}
	if len(devices) == 0 {
		return errMobileNoticeNoRecipient
	}
	name := strings.TrimSpace(agentName)
	if name == "" {
		name = "An agent"
	}
	title := name + " is asking to join"
	if len(title) > mobileNoticeMaxTitle {
		title = title[:mobileNoticeMaxTitle]
	}
	return bridge.publishMobileNotice(mobileNotice{
		ID: uuid.NewString(), Kind: mobileNoticeKindAgentJoin, Title: title, Body: "Check the code in Isoform.",
		Category: "YANSU_MOMENT", DestinationKind: mobileNoticeKindAgentJoin, DestinationID: p.JoinID,
		Level: "time-sensitive", CreatedAt: now, ExpiresAt: time.UnixMilli(p.ExpiresAt),
	}, devices)
}

// ── Disconnected agents (agents_disconnected.go keeps the list) ──

// rewriteDisconnectedAgents changes the list under the file lock.
func (a *App) rewriteDisconnectedAgents(change func([]disconnectedAgent) []disconnectedAgent) {
	a.agentJoin.fileMu.Lock()
	defer a.agentJoin.fileMu.Unlock()
	path, err := a.disconnectedAgentsPath()
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	if err == nil {
		err = writeFrontJSON(path, change(a.disconnectedAgentsLocked()))
	}
	if err != nil {
		log.Printf("[agents] the disconnected list wasn't saved: %v", err)
	}
}

// clearDisconnectedFor drops the entries an agent that joined replaces: the
// one it reconnects, the same known agent, or the same name.
func (a *App) clearDisconnectedFor(name, reconnect string) {
	catalog := catalogIDForName(name)
	a.rewriteDisconnectedAgents(func(list []disconnectedAgent) []disconnectedAgent {
		kept := list[:0]
		for _, d := range list {
			if d.ID == reconnect || (catalog != "" && d.CatalogID == catalog) || strings.EqualFold(strings.TrimSpace(d.Name), strings.TrimSpace(name)) {
				continue
			}
			kept = append(kept, d)
		}
		return kept
	})
}

// phoneJoinReconnect is the agent the open phone join reconnects, if any.
func (a *App) phoneJoinReconnect() string {
	a.agentJoin.phoneMu.Lock()
	defer a.agentJoin.phoneMu.Unlock()
	if p := a.agentJoin.phone; p != nil {
		if svc := a.agentJoin.svc; svc != nil {
			if j := svc.Current(); j != nil && j.ExpiresAt == p.ExpiresAt {
				return p.Reconnect
			}
		}
	}
	return ""
}

// ── The phone (PROTOCOL.md 27.4) ──

func (bridge *mobileBridge) serveAgentJoinStart(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Reconnect      string `json:"reconnect,omitempty"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	reconnect := strings.TrimSpace(input.Reconnect)
	if reconnect != "" && !bridge.app.agentReconnectable(reconnect) {
		mobileJSON(w, http.StatusBadRequest, nil, "that agent isn't one you disconnected lately")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		start, joinID, err := bridge.app.startPhoneAgentJoin(device.DeviceID, reconnect)
		switch {
		case errors.Is(err, errTaskLinksClosedEyes), errors.Is(err, errWorkOnItClosedEyes):
			mobileJSON(w, http.StatusConflict, map[string]string{"code": "not_taking_work"}, "your computer isn't taking agent work right now")
		case errors.Is(err, errAgentJoinNoOrigin):
			mobileJSON(w, http.StatusConflict, map[string]string{"code": "no_address"}, err.Error())
		case err != nil:
			log.Printf("[agents] phone join failed: %v", err)
			mobileJSON(w, http.StatusInternalServerError, nil, "the join link could not be made")
		default:
			mobileJSON(w, http.StatusCreated, map[string]any{"joinId": joinID, "message": start.Message, "link": start.Link,
				"expiresAt": start.ExpiresAt, "local": start.Local}, "")
		}
	})
}

func (bridge *mobileBridge) serveAgentJoin(w http.ResponseWriter, joinID string) {
	view, ok := bridge.app.phoneAgentJoinView(joinID)
	if !ok {
		mobileJSON(w, http.StatusNotFound, nil, errPhoneJoinUnknown.Error())
		return
	}
	mobileJSON(w, http.StatusOK, view, "")
}

func (bridge *mobileBridge) serveAgentJoinAnswer(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, joinID string) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Answer         string `json:"answer"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	if input.Answer != "yes" && input.Answer != "no" {
		mobileJSON(w, http.StatusBadRequest, nil, "answer must be yes or no")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		view, err := bridge.app.answerPhoneAgentJoin(joinID, input.Answer == "yes")
		switch {
		case err == nil:
			mobileJSON(w, http.StatusOK, view, "")
		case errors.Is(err, errPhoneJoinUnknown):
			mobileJSON(w, http.StatusNotFound, nil, err.Error())
		case errors.Is(err, errPhoneJoinExpired):
			mobileJSON(w, http.StatusGone, view, err.Error())
		case errors.Is(err, errPhoneJoinNotAsked):
			mobileJSON(w, http.StatusConflict, view, err.Error())
		case errors.Is(err, errTaskLinksClosedEyes), errors.Is(err, errWorkOnItClosedEyes):
			mobileJSON(w, http.StatusConflict, map[string]string{"code": "not_taking_work"}, "your computer isn't taking agent work right now")
		default:
			log.Printf("[agents] phone join answer failed: %v", err)
			mobileJSON(w, http.StatusInternalServerError, nil, "that didn't go through")
		}
	})
}

// agentReconnectable says whether an id is one Connect again may name.
func (a *App) agentReconnectable(id string) bool {
	for _, d := range a.disconnectedAgents() {
		if d.ID == id || d.CatalogID == id {
			return true
		}
	}
	return false
}
