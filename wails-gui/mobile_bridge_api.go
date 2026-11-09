package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Release 1 routes (PROTOCOL.md section 7). Chat, snapshot, permission, purpose,
// self and memory routes return in later releases.
const (
	mobileBriefListDefault = 14
	mobileBriefListMax     = 60
	mobileBriefIDMaxLength = 64
)

// mobileIDMaxLength bounds the ids a route accepts from the phone.
const mobileIDMaxLength = 128

var (
	errMobileDeviceNotFound = errors.New("device not found")
	// errMobileStoreUnavailable answers 503 while the Mac's stores are not ready.
	errMobileStoreUnavailable = errors.New("the computer is not ready yet")
	errMobileNotFound         = errors.New("not found")
)

type mobileStatusView struct {
	Topics    bool `json:"topics,omitempty"`
	BrainDump bool `json:"brainDump,omitempty"`
	FirstRead bool `json:"firstRead,omitempty"`
	Holds     bool `json:"holds,omitempty"`
	// Sees is true when this Mac serves What Isoform sees (20.7e).
	Sees bool `json:"sees,omitempty"`
	// TaskThreads is true when this Mac serves task threads (20.7b): thread
	// fields on tasks, task replies and actions, and an origin on new work.
	TaskThreads      bool `json:"taskThreads,omitempty"`
	TodayMeetingPrep bool `json:"todayMeetingPrep,omitempty"`
	// ChatMainSend is true when POST /chats/main/messages sends to the main chat even before the phone lists one (15.5a).
	ChatMainSend           bool                `json:"chatMainSend,omitempty"`
	BriefRelevanceFeedback bool                `json:"briefRelevanceFeedback,omitempty"`
	BriefAsk               bool                `json:"briefAsk,omitempty"`
	BriefWeekDecline       bool                `json:"briefWeekDecline,omitempty"`
	NotificationTest       bool                `json:"notificationTest,omitempty"`
	HubName                string              `json:"hubName"`
	AppVersion             string              `json:"appVersion"`
	ProtocolVersion        int                 `json:"protocolVersion"`
	NarrativeAvailable     bool                `json:"narrativeAvailable"`
	Schedule               MobileBriefSchedule `json:"schedule"`
	LastBriefAt            string              `json:"lastBriefAt"`
	// KeepAwake is the saved "Keep this Mac awake" switch.
	KeepAwake bool `json:"keepAwake"`
	// InstantPush is true when this Mac's relay can deliver notices at once.
	InstantPush bool `json:"instantPush"`
	// BriefSchedule lists the scheduled brief slots due in the next 24 hours, at
	// their slot times. The Mac starts each build 10 minutes earlier.
	BriefSchedule []mobileBriefSlotTime `json:"briefSchedule"`
}

// mobileBriefSlotTime is one scheduled brief: its slot and the time it is stated for.
type mobileBriefSlotTime struct {
	Slot string `json:"slot"`
	At   string `json:"at"`
}

// briefSource returns the running brief service, or nil while it is not ready.
func (bridge *mobileBridge) briefSource() mobileBriefSource {
	if bridge.app == nil {
		return nil
	}
	return bridge.app.mobileBriefSource()
}

// handleAuthorized routes one request from an authenticated device. A route that
// needs a scope the device lacks answers 403.
func (bridge *mobileBridge) handleAuthorized(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	path := strings.TrimPrefix(r.URL.Path, mobileAPIPrefix)
	switch r.Method + " " + path {
	case "GET /status":
		bridge.serveStatus(w)
	case "GET /holds/propose":
		if !mobileRequireScope(w, device, mobileScopeHandoff) {
			return
		}
		bridge.serveHoldProposal(w, r, device, calendarAccessGranted())
	case "GET /holds/status":
		if !mobileRequireScope(w, device, mobileScopeHandoff) {
			return
		}
		bridge.serveHoldStatus(w, r, device)
	case "POST /holds/actions":
		if !mobileRequireScope(w, device, mobileScopeHandoff) {
			return
		}
		bridge.serveHoldAction(w, r, device, calendarAccessGranted())
	case "POST /first-read/answer":
		if !mobileRequireScope(w, device, mobileScopeChatWrite) || !mobileRequireScope(w, device, mobileScopeMemoryFix) {
			return
		}
		bridge.serveFirstReadAnswer(w, r, device)
	case "GET /today":
		if !mobileRequireScope(w, device, mobileScopeBriefRead) {
			return
		}
		bridge.serveToday(w, r, device)
	case "GET /today/meeting-prep":
		if !mobileRequireScope(w, device, mobileScopeBriefRead) {
			return
		}
		bridge.serveTodayMeetingPrep(w, r)
	case "GET /calendar/free-time":
		if !mobileRequireScope(w, device, mobileScopeBriefRead) {
			return
		}
		if bridge.app == nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, "calendar unavailable")
			return
		}
		ranges, err := bridge.app.GetFindTime()
		if err != nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, "calendar unavailable")
			return
		}
		mobileJSON(w, http.StatusOK, ranges, "")
	case "GET /calendar/free-time-details":
		if !mobileRequireScope(w, device, mobileScopeBriefRead) {
			return
		}
		if bridge.app == nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, "calendar unavailable")
			return
		}
		details, err := bridge.app.GetFindTimeDetails()
		if err != nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, "calendar unavailable")
			return
		}
		mobileJSON(w, http.StatusOK, details, "")
	case "GET /topics":
		if !mobileRequireScope(w, device, mobileScopeBriefRead) {
			return
		}
		bridge.serveTopicList(w)
	case "GET /brief/latest":
		if !mobileRequireScope(w, device, mobileScopeBriefRead) {
			return
		}
		bridge.serveLatestBrief(w)
	case "GET /briefs":
		if !mobileRequireScope(w, device, mobileScopeBriefRead) {
			return
		}
		bridge.serveBriefList(w, r.URL.Query().Get("limit"))
	case "POST /brief/refresh":
		if !mobileRequireScope(w, device, mobileScopeBriefRefresh) {
			return
		}
		bridge.serveBriefRefresh(w, r, device)
	case "GET /devices":
		bridge.serveDevices(w)
	case "GET /chats":
		if !mobileRequireScope(w, device, mobileScopeChatRead) {
			return
		}
		bridge.serveChatList(w, r.URL.Query().Get("limit"))
	case "POST /chats":
		if !mobileRequireScope(w, device, mobileScopeChatWrite) {
			return
		}
		bridge.serveChatCreate(w, r, device)
	case "POST /handoffs":
		if !mobileRequireScope(w, device, mobileScopeHandoff) {
			return
		}
		bridge.serveHandoffCreate(w, r, device)
	case "GET /notifications":
		if !mobileRequireScope(w, device, mobileScopePush) {
			return
		}
		bridge.serveNotificationSettings(w)
	case "POST /notifications/test":
		if !mobileRequireScope(w, device, mobileScopePush) {
			return
		}
		bridge.serveNotificationTest(w, r, device)
	case "POST /notifications/preferences":
		if !mobileRequireScope(w, device, mobileScopePush) {
			return
		}
		bridge.serveNotificationPreferences(w, r, device)
	case "GET /settings/keep-awake":
		if !mobileRequireScope(w, device, mobileScopePush) {
			return
		}
		bridge.serveKeepAwake(w)
	case "POST /settings/keep-awake":
		if !mobileRequireScope(w, device, mobileScopePush) {
			return
		}
		bridge.serveKeepAwakeSet(w, r)
	case "GET /goals":
		if !mobileRequireScope(w, device, mobileScopeGoalsRead) {
			return
		}
		bridge.serveGoalList(w, r)
	case "GET /your-agent":
		if !mobileRequireScope(w, device, mobileScopeBriefRead) {
			return
		}
		bridge.serveYourAgent(w)
	case "GET /work-profile/summary":
		if !mobileRequireScope(w, device, mobileScopeBriefRead) {
			return
		}
		bridge.serveWorkProfileSummary(w)
	case "GET /agent-owner/status", "GET /agent-owner/jobs", "GET /agent-owner/job", "GET /agent-owner/balance":
		if !mobileRequireScope(w, device, mobileScopeBriefRead) {
			return
		}
		bridge.serveAgentOwnerRead(w, r, strings.TrimPrefix(path, "/agent-owner/"))
	case "POST /agent-owner/decision", "POST /agent-owner/action":
		if !mobileRequireScope(w, device, mobileScopeBriefRead) || !mobileRequireScope(w, device, mobileScopeChatWrite) {
			return
		}
		bridge.serveAgentOwnerWrite(w, r, path == "/agent-owner/decision")
	case "GET /tasks":
		if !mobileRequireScope(w, device, mobileScopeBriefRead) {
			return
		}
		bridge.serveTaskListForDevice(w, device)
	case "POST /tasks/parse":
		if !mobileRequireScope(w, device, mobileScopeHandoff) {
			return
		}
		bridge.serveBrainDumpParse(w, r, device)
	case "POST /tasks/add":
		if !mobileRequireScope(w, device, mobileScopeHandoff) {
			return
		}
		bridge.serveBrainDumpAdd(w, r, device)
	case "GET /privacy":
		if !mobileRequireScope(w, device, mobileScopeBriefRead) {
			return
		}
		bridge.serveSees(w)
	case "POST /privacy/show":
		if !mobileRequireScope(w, device, mobileScopeBriefRead) {
			return
		}
		bridge.serveSeesShow(w, r, device)
	case "POST /offers/find-more":
		if !mobileRequireScope(w, device, mobileScopeOffersRead) {
			return
		}
		bridge.serveOfferFindMore(w, r)
	case "GET /offers":
		if !mobileRequireScope(w, device, mobileScopeOffersRead) {
			return
		}
		bridge.serveOfferList(w, r, device)
	case "POST /goals":
		if !mobileRequireScope(w, device, mobileScopeGoalsWrite) {
			return
		}
		bridge.serveGoalCreate(w, r, device)
	default:
		switch mobileRouteFamily(r.Method, path) {
		case "GET /topics/*":
			if !mobileRequireScope(w, device, mobileScopeChatRead) {
				return
			}
			bridge.serveTopicDetail(w, r, mobilePathSegments(path)[1])
		case "POST /topics/*/messages":
			if !mobileRequireScope(w, device, mobileScopeChatWrite) {
				return
			}
			bridge.serveTopicMessage(w, r, device, mobilePathSegments(path)[1])
		case "GET /chats/*":
			if !mobileRequireScope(w, device, mobileScopeChatRead) {
				return
			}
			bridge.serveChatDetail(w, r, device, mobilePathSegments(path)[1])
		case "POST /chats/*/proactive/*/receipts":
			if !mobileRequireScope(w, device, mobileScopeChatRead) {
				return
			}
			segments := mobilePathSegments(path)
			bridge.serveProactiveReceipt(w, r, device, segments[1], segments[3])
		case "POST /chats/*/proactive/*/actions":
			if !mobileRequireScope(w, device, mobileScopeChatWrite) {
				return
			}
			segments := mobilePathSegments(path)
			bridge.serveProactiveAction(w, r, device, segments[1], segments[3])
		case "POST /chats/*/messages", "POST /chats/*/offer-messages":
			if !mobileRequireScope(w, device, mobileScopeChatWrite) {
				return
			}
			bridge.serveChatMessage(w, r, device, mobilePathSegments(path)[1])
		case "POST /chats/*/brief-messages":
			if !mobileRequireScope(w, device, mobileScopeChatWrite) || !mobileRequireScope(w, device, mobileScopeBriefRead) {
				return
			}
			bridge.serveBriefChatMessage(w, r, device, mobilePathSegments(path)[1])
		case "POST /briefs/*/week-intent":
			if !mobileRequireScope(w, device, mobileScopeChatWrite) || !mobileRequireScope(w, device, mobileScopeBriefRead) {
				return
			}
			bridge.serveMorningBriefWeekIntent(w, r, device, mobilePathSegments(path)[1])
		case "GET /briefs/*/week-decline":
			if !mobileRequireScope(w, device, mobileScopeBriefRead) {
				return
			}
			bridge.serveWeekDecline(w, r, device, mobilePathSegments(path)[1])
		case "POST /briefs/*/week-decline":
			if !mobileRequireScope(w, device, mobileScopeBriefRead) || !mobileRequireScope(w, device, mobileScopeChatWrite) {
				return
			}
			bridge.serveWeekDecline(w, r, device, mobilePathSegments(path)[1])
		case "POST /chats/*/brief-opener":
			if !mobileRequireScope(w, device, mobileScopeChatWrite) || !mobileRequireScope(w, device, mobileScopeBriefRead) {
				return
			}
			bridge.serveBriefOpener(w, r, device, mobilePathSegments(path)[1])
		case "POST /chats/*/brief-paragraph":
			if !mobileRequireScope(w, device, mobileScopeChatWrite) || !mobileRequireScope(w, device, mobileScopeBriefRead) {
				return
			}
			bridge.serveBriefParagraph(w, r, device, mobilePathSegments(path)[1])
		case "POST /chats/*/cancel":
			if !mobileRequireScope(w, device, mobileScopeChatWrite) {
				return
			}
			bridge.serveChatCancel(w, r, device, mobilePathSegments(path)[1])
		case "POST /chats/*/permissions/*":
			if !mobileRequireScope(w, device, mobileScopeChatWrite) {
				return
			}
			segments := mobilePathSegments(path)
			bridge.servePermissionAnswer(w, r, device, segments[1], segments[3])
		case "POST /chats/*/proactive/actions":
			if !mobileRequireScope(w, device, mobileScopeChatWrite) {
				return
			}
			bridge.serveProactiveCardAction(w, r, device, mobilePathSegments(path)[1])
		case "POST /memory/*/reject-attribution":
			if !mobileRequireScope(w, device, mobileScopeMemoryFix) {
				return
			}
			bridge.serveMemoryReject(w, r, device, mobilePathSegments(path)[1])
		case "POST /self/directives/*/retract":
			if !mobileRequireScope(w, device, mobileScopeMemoryFix) {
				return
			}
			bridge.serveDirectiveRetract(w, r, device, mobilePathSegments(path)[2])
		case "GET /briefs/*/audio":
			if !mobileRequireScope(w, device, mobileScopeBriefRead) {
				return
			}
			bridge.serveBriefAudio(w, r, mobilePathSegments(path)[1])
		case "GET /briefs/*/ask-answer":
			if !mobileRequireScope(w, device, mobileScopeBriefRead) || !mobileRequireScope(w, device, mobileScopeChatRead) {
				return
			}
			bridge.serveBriefAskAnswer(w, r, mobilePathSegments(path)[1])
		case "GET /briefs/*":
			if !mobileRequireScope(w, device, mobileScopeBriefRead) {
				return
			}
			bridge.serveBrief(w, r, strings.TrimPrefix(path, "/briefs/"))
		case "POST /briefs/*/received":
			if !mobileRequireScope(w, device, mobileScopeBriefRead) {
				return
			}
			bridge.serveBriefReceived(w, r, device, mobilePathSegments(path)[1])
		case "POST /briefs/*/conversation":
			if !mobileRequireScope(w, device, mobileScopeChatWrite) || !mobileRequireScope(w, device, mobileScopeBriefRead) {
				return
			}
			bridge.serveBriefConversation(w, r, device, mobilePathSegments(path)[1])
		case "GET /goals/*":
			if !mobileRequireScope(w, device, mobileScopeGoalsRead) {
				return
			}
			bridge.serveGoalDetail(w, mobilePathSegments(path)[1])
		case "GET /tasks/*":
			if !mobileRequireScope(w, device, mobileScopeBriefRead) {
				return
			}
			bridge.serveTaskDetail(w, mobilePathSegments(path)[1])
		case "POST /handoffs/*/start":
			if !mobileRequireScope(w, device, mobileScopeHandoff) {
				return
			}
			bridge.serveHandoffStart(w, r, device, mobilePathSegments(path)[1])
		case "POST /tasks/*/messages":
			if !mobileRequireScope(w, device, mobileScopeChatWrite) {
				return
			}
			bridge.serveTaskMessage(w, r, device, mobilePathSegments(path)[1])
		case "POST /tasks/*/actions":
			if !mobileRequireScope(w, device, mobileScopeHandoff) {
				return
			}
			bridge.serveTaskAction(w, r, device, mobilePathSegments(path)[1])
		case "POST /tasks/*/stop":
			if !mobileRequireScope(w, device, mobileScopeHandoff) {
				return
			}
			bridge.serveTaskStop(w, r, device, mobilePathSegments(path)[1])
		case "POST /goals/*/events":
			if !mobileRequireScope(w, device, mobileScopeGoalsWrite) {
				return
			}
			bridge.serveGoalEvent(w, r, device, mobilePathSegments(path)[1])
		case "POST /offers/*/decline":
			if !mobileRequireScope(w, device, mobileScopeOffersDecide) {
				return
			}
			bridge.serveOfferDecline(w, r, device, mobilePathSegments(path)[1])
		case "POST /briefs/*/items/*/feedback":
			if !mobileRequireScope(w, device, mobileScopeBriefFeedback) {
				return
			}
			segments := mobilePathSegments(path)
			bridge.serveBriefFeedback(w, r, device, segments[1], segments[3])
		case "DELETE /devices/*":
			bridge.serveSelfRevoke(w, device, strings.TrimPrefix(path, "/devices/"))
		case "POST /notifications/*":
			if !mobileRequireScope(w, device, mobileScopePush) {
				return
			}
			bridge.serveNotificationKind(w, r, device, mobilePathSegments(path)[1])
		default:
			mobileJSON(w, http.StatusNotFound, nil, "mobile route not found")
		}
	}
}

// mobileRouteFamily names parameterized routes so the default branch can switch
// on a fixed template instead of on request data.
func mobileRouteFamily(method, path string) string {
	if strings.HasPrefix(path, "/briefs/") && !strings.Contains(strings.TrimPrefix(path, "/briefs/"), "/") && method == http.MethodGet {
		return "GET /briefs/*"
	}
	if strings.HasPrefix(path, "/devices/") && !strings.Contains(strings.TrimPrefix(path, "/devices/"), "/") && method == http.MethodDelete {
		return "DELETE /devices/*"
	}
	parts := mobilePathSegments(path)
	switch true {
	case len(parts) == 2 && parts[0] == "topics" && method == http.MethodGet:
		return "GET /topics/*"
	case len(parts) == 3 && parts[0] == "topics" && parts[2] == "messages" && method == http.MethodPost:
		return "POST /topics/*/messages"
	case len(parts) == 3 && parts[0] == "briefs" && parts[2] == "audio" && method == http.MethodGet:
		return "GET /briefs/*/audio"
	case len(parts) == 3 && parts[0] == "briefs" && parts[2] == "received" && method == http.MethodPost:
		return "POST /briefs/*/received"
	case len(parts) == 3 && parts[0] == "briefs" && parts[2] == "conversation" && method == http.MethodPost:
		return "POST /briefs/*/conversation"
	case len(parts) == 3 && parts[0] == "briefs" && parts[2] == "week-intent" && method == http.MethodPost:
		return "POST /briefs/*/week-intent"
	case len(parts) == 3 && parts[0] == "briefs" && parts[2] == "week-decline" && method == http.MethodGet:
		return "GET /briefs/*/week-decline"
	case len(parts) == 3 && parts[0] == "briefs" && parts[2] == "week-decline" && method == http.MethodPost:
		return "POST /briefs/*/week-decline"
	case len(parts) == 3 && parts[0] == "briefs" && parts[2] == "ask-answer" && method == http.MethodGet:
		return "GET /briefs/*/ask-answer"
	case len(parts) == 2 && parts[0] == "chats" && method == http.MethodGet:
		return "GET /chats/*"
	case len(parts) == 5 && parts[0] == "chats" && parts[2] == "proactive" && parts[4] == "receipts" && method == http.MethodPost:
		return "POST /chats/*/proactive/*/receipts"
	case len(parts) == 5 && parts[0] == "chats" && parts[2] == "proactive" && parts[4] == "actions" && method == http.MethodPost:
		return "POST /chats/*/proactive/*/actions"
	case len(parts) == 3 && parts[0] == "chats" && parts[2] == "messages" && method == http.MethodPost:
		return "POST /chats/*/messages"
	case len(parts) == 3 && parts[0] == "chats" && parts[2] == "offer-messages" && method == http.MethodPost:
		return "POST /chats/*/offer-messages"
	case len(parts) == 3 && parts[0] == "chats" && parts[2] == "brief-messages" && method == http.MethodPost:
		return "POST /chats/*/brief-messages"
	case len(parts) == 3 && parts[0] == "chats" && parts[2] == "brief-opener" && method == http.MethodPost:
		return "POST /chats/*/brief-opener"
	case len(parts) == 3 && parts[0] == "chats" && parts[2] == "brief-paragraph" && method == http.MethodPost:
		return "POST /chats/*/brief-paragraph"
	case len(parts) == 3 && parts[0] == "chats" && parts[2] == "cancel" && method == http.MethodPost:
		return "POST /chats/*/cancel"
	case len(parts) == 4 && parts[0] == "chats" && parts[2] == "permissions" && method == http.MethodPost:
		return "POST /chats/*/permissions/*"
	case len(parts) == 4 && parts[0] == "chats" && parts[2] == "proactive" && parts[3] == "actions" && method == http.MethodPost:
		return "POST /chats/*/proactive/actions"
	case len(parts) == 3 && parts[0] == "memory" && parts[2] == "reject-attribution" && method == http.MethodPost:
		return "POST /memory/*/reject-attribution"
	case len(parts) == 4 && parts[0] == "self" && parts[1] == "directives" && parts[3] == "retract" && method == http.MethodPost:
		return "POST /self/directives/*/retract"
	case len(parts) == 2 && parts[0] == "notifications" && method == http.MethodPost:
		return "POST /notifications/*"
	case len(parts) == 5 && parts[0] == "briefs" && parts[2] == "items" && parts[4] == "feedback" && method == http.MethodPost:
		return "POST /briefs/*/items/*/feedback"
	case len(parts) == 2 && parts[0] == "goals" && method == http.MethodGet:
		return "GET /goals/*"
	case len(parts) == 2 && parts[0] == "tasks" && method == http.MethodGet:
		return "GET /tasks/*"
	case len(parts) == 3 && parts[0] == "handoffs" && parts[2] == "start" && method == http.MethodPost:
		return "POST /handoffs/*/start"
	case len(parts) == 3 && parts[0] == "tasks" && parts[2] == "stop" && method == http.MethodPost:
		return "POST /tasks/*/stop"
	case len(parts) == 3 && parts[0] == "tasks" && parts[2] == "messages" && method == http.MethodPost:
		return "POST /tasks/*/messages"
	case len(parts) == 3 && parts[0] == "tasks" && parts[2] == "actions" && method == http.MethodPost:
		return "POST /tasks/*/actions"
	case len(parts) == 3 && parts[0] == "goals" && parts[2] == "events" && method == http.MethodPost:
		return "POST /goals/*/events"
	case len(parts) == 3 && parts[0] == "offers" && parts[2] == "decline" && method == http.MethodPost:
		return "POST /offers/*/decline"
	}
	return ""
}

// mobileValidID bounds the identifiers a route passes to the Mac's stores.
func mobileValidID(id string) bool {
	if id == "" || len(id) > mobileIDMaxLength {
		return false
	}
	for _, r := range id {
		if r <= ' ' || r > '~' || r == '/' {
			return false
		}
	}
	return true
}

// mobileStoreReady reports whether the activity store the phone's write routes
// use is open.
func (a *App) mobileStoreReady() bool {
	return a != nil && a.activity != nil && a.activity.store != nil
}

// mobileMutationResult maps a write route's error to a phone-safe answer.
func mobileMutationResult(w http.ResponseWriter, data any, err error) {
	switch {
	case err == nil:
		mobileJSON(w, http.StatusOK, data, "")
	case errors.Is(err, errMobileStoreUnavailable):
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
	case errors.Is(err, errMobileNotFound):
		mobileJSON(w, http.StatusNotFound, nil, "this item is no longer on your computer")
	default:
		log.Printf("[mobile] action failed: %v", err)
		mobileJSON(w, http.StatusConflict, nil, "the computer could not complete this action; refresh and try again")
	}
}

func mobileRequireScope(w http.ResponseWriter, device mobileDeviceRecord, scope string) bool {
	if mobileHasScope(device.Scopes, scope) {
		return true
	}
	mobileJSON(w, http.StatusForbidden, nil, "this phone does not have the "+scope+" permission; pair it again to grant it")
	return false
}

func (bridge *mobileBridge) serveStatus(w http.ResponseWriter) {
	view := mobileStatusView{
		HubName: mobileHubName(), AppVersion: Version, ProtocolVersion: 1, Topics: true, TaskThreads: true, BrainDump: true, Sees: true, FirstRead: true, TodayMeetingPrep: true, ChatMainSend: true, NotificationTest: true, BriefRelevanceFeedback: true, BriefAsk: true, BriefWeekDecline: true,
		KeepAwake: bridge.keepAwakeSetting(), InstantPush: mobileInstantPushConfigured(),
		BriefSchedule: []mobileBriefSlotTime{},
	}
	view.Holds = bridge.mobileHoldsAvailable(calendarAccessGranted())
	if svc := bridge.briefSource(); svc != nil {
		if schedule, err := svc.Schedule(); err == nil {
			view.Schedule = schedule
			view.NarrativeAvailable = schedule.NarrativeAvailable
			view.BriefSchedule = mobileBriefSlotTimes(time.Now(), time.Local, schedule)
		}
		if brief, ok := currentMobileBrief(svc); ok {
			view.LastBriefAt = brief.AsOf
		}
	}
	mobileJSON(w, http.StatusOK, view, "")
}

// mobileBriefSlotTimes lists the enabled brief slots that fall in the next 24
// hours, at their slot times, earliest first.
func mobileBriefSlotTimes(now time.Time, loc *time.Location, schedule MobileBriefSchedule) []mobileBriefSlotTime {
	out := []mobileBriefSlotTime{}
	if !schedule.Enabled {
		return out
	}
	if loc == nil {
		loc = time.Local
	}
	end := now.Add(24 * time.Hour)
	local := now.In(loc)
	clocks := []struct{ slot, value string }{
		{mobileBriefSlotMorning, schedule.Morning},
		{mobileBriefSlotEvening, schedule.Evening},
	}
	for day := 0; day <= 1; day++ {
		for _, clock := range clocks {
			hour, minute, ok := parseMobileBriefClock(clock.value)
			if !ok {
				continue
			}
			at := time.Date(local.Year(), local.Month(), local.Day()+day, hour, minute, 0, 0, loc)
			if at.Before(now) || at.After(end) {
				continue
			}
			out = append(out, mobileBriefSlotTime{Slot: clock.slot, At: at.Format(time.RFC3339)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// serveKeepAwake answers GET /settings/keep-awake.
func (bridge *mobileBridge) serveKeepAwake(w http.ResponseWriter) {
	if bridge.keepAwake == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	mobileJSON(w, http.StatusOK, bridge.keepAwake.Status(), "")
}

// serveKeepAwakeSet answers POST /settings/keep-awake with {"on": bool}.
func (bridge *mobileBridge) serveKeepAwakeSet(w http.ResponseWriter, r *http.Request) {
	var input struct {
		On *bool `json:"on"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	if input.On == nil {
		mobileJSON(w, http.StatusBadRequest, nil, "on is required")
		return
	}
	if bridge.keepAwake == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	status, err := bridge.keepAwake.SetOn(*input.On)
	if err != nil {
		mobileJSON(w, http.StatusInternalServerError, nil, "the setting could not be saved")
		return
	}
	bridge.notify()
	bridge.noteTodayChanged()
	mobileJSON(w, http.StatusOK, status, "")
}

// serveLatestBrief answers with the current brief's package.
func (bridge *mobileBridge) serveLatestBrief(w http.ResponseWriter) {
	svc := bridge.briefSource()
	if svc == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileBriefUnavailable.Error())
		return
	}
	// The Today read first: it saves any withdrawal over the stored brief
	// before the current one is chosen.
	svc.LatestMobileBrief()
	brief, ok := currentMobileBrief(svc)
	if !ok {
		mobileJSON(w, http.StatusNotFound, nil, "no brief is available yet")
		return
	}
	pkg, ok := bridge.briefPackage(brief.BriefID)
	if !ok {
		mobileJSON(w, http.StatusNotFound, nil, "no brief is available yet")
		return
	}
	bridge.servePackageObject(w, pkg)
}

// currentMobileBrief is the one current brief (PROTOCOL.md section 8.1), the
// choice made in mobile_brief_current.go: the newest stored brief with written
// text, else the newest stored brief. Every phone route uses this choice.
func currentMobileBrief(svc mobileBriefSource) (MobileBriefV1, bool) {
	if current, ok := svc.(mobileBriefCurrenter); ok {
		return current.CurrentMobileBrief()
	}
	// A source without the in-memory choice (test fakes) applies the same
	// rule to its listing, newest first.
	var briefs []MobileBriefV1
	for _, row := range svc.ListMobileBriefs(mobileBriefStoreListMax) {
		if brief, ok := svc.GetMobileBrief(row.BriefID); ok {
			briefs = append(briefs, brief)
		}
	}
	return mobileBriefChooseCurrent(briefs)
}

func (bridge *mobileBridge) serveBriefList(w http.ResponseWriter, rawLimit string) {
	limit := mobileBriefListDefault
	if rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > mobileBriefListMax {
			mobileJSON(w, http.StatusBadRequest, nil, "limit must be between 1 and 60")
			return
		}
		limit = parsed
	}
	svc := bridge.briefSource()
	if svc == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileBriefUnavailable.Error())
		return
	}
	mobileJSON(w, http.StatusOK, mobileBriefSummariesWithLeads(svc, limit), "")
}

// Derive leads only from the delivery-gated brief, never directly from the
// stored document, so withdrawn narrative cannot leak through the index.
func mobileBriefSummariesWithLeads(svc mobileBriefSource, limit int) []MobileBriefSummary {
	rows := mobileBounded(svc.ListMobileBriefs(limit), limit)
	for i := range rows {
		if brief, ok := svc.GetMobileBrief(rows[i].BriefID); ok && brief.NarrativeStatus == "ok" {
			rows[i].Lead = mobileBriefLead(brief.Narrative)
			if rows[i].Lead == "" {
				rows[i].Lead = mobileBriefLead(brief.WeeklyRecap)
			}
			if rows[i].Lead == "" && brief.YourWeek != nil {
				rows[i].Lead = mobileBriefLead([]MobileBriefPara{{Text: brief.YourWeek.Headline}})
			}
		}
	}
	return rows
}

func mobileBriefLead(paragraphs []MobileBriefPara) string {
	greeting := ""
	for _, paragraph := range paragraphs {
		text := strings.TrimSpace(paragraph.Text)
		for text != "" {
			sentence, rest := mobileBriefFirstSentence(text)
			// The lead says something about the day: a greeting is skipped
			// while anything follows it.
			if mobileBriefIsGreeting(sentence) {
				if greeting == "" {
					greeting = sentence
				}
				text = rest
				continue
			}
			return mobileChatBound(sentence, 384)
		}
	}
	return mobileChatBound(greeting, 384)
}

// mobileBriefFirstSentence splits text after its first sentence.
func mobileBriefFirstSentence(text string) (string, string) {
	runes := []rune(text)
	for i, char := range runes {
		switch char {
		case '。', '！', '？':
			return string(runes[:i+1]), strings.TrimSpace(string(runes[i+1:]))
		case '.', '!', '?':
			if i+1 == len(runes) || runes[i+1] == ' ' || runes[i+1] == '\n' {
				return string(runes[:i+1]), strings.TrimSpace(string(runes[i+1:]))
			}
		}
	}
	return text, ""
}

// serveBrief answers GET /briefs/{id} from the stored package, byte for byte
// (PROTOCOL.md section 8.1): the package object while it fits, its parts
// otherwise, and always parts with ?package=1.
func (bridge *mobileBridge) serveBrief(w http.ResponseWriter, r *http.Request, briefID string) {
	if !mobileValidBriefID(briefID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the brief id is not valid")
		return
	}
	if bridge.briefSource() == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileBriefUnavailable.Error())
		return
	}
	pkg, ok := bridge.briefPackage(briefID)
	if !ok {
		mobileJSON(w, http.StatusNotFound, nil, "brief not found")
		return
	}
	query := r.URL.Query()
	if rawPart := query.Get("part"); rawPart != "" {
		part, err := strconv.Atoi(rawPart)
		version := query.Get("version")
		if err != nil || part < 0 || version == "" || len(version) > 64 {
			mobileJSON(w, http.StatusBadRequest, nil, "the brief part is invalid")
			return
		}
		if version != pkg.Version {
			mobileJSON(w, http.StatusConflict, map[string]string{"reason": errMobileBriefPackageChanged.Error()}, errMobileBriefPackageChanged.Error())
			return
		}
		parts := mobileBriefPackageParts(pkg.Bytes)
		if part >= len(parts) {
			mobileJSON(w, http.StatusBadRequest, nil, "the brief part is invalid")
			return
		}
		mobileJSON(w, http.StatusOK, map[string]any{"version": pkg.Version, "part": part, "data": parts[part]}, "")
		return
	}
	if query.Get("package") == "1" {
		parts := mobileBriefPackageParts(pkg.Bytes)
		mobileJSON(w, http.StatusOK, map[string]any{"briefId": pkg.BriefID, "version": pkg.Version, "parts": len(parts), "data": parts[0]}, "")
		return
	}
	bridge.servePackageObject(w, pkg)
}

// servePackageObject answers with the package object itself while it fits,
// for phones that read a brief as one object.
func (bridge *mobileBridge) servePackageObject(w http.ResponseWriter, pkg mobileBriefPackage) {
	if len(pkg.Bytes) > mobileBriefPackageInlineMax {
		mobileJSON(w, http.StatusOK, map[string]any{"chunked": true, "version": pkg.Version, "parts": len(mobileBriefPackageParts(pkg.Bytes))}, "")
		return
	}
	mobileJSON(w, http.StatusOK, json.RawMessage(pkg.Bytes), "")
}

// serveBriefReceived records that a phone saved a brief's package, so the
// outbox stops sending it.
func (bridge *mobileBridge) serveBriefReceived(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, briefID string) {
	if !mobileValidBriefID(briefID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the brief id is not valid")
		return
	}
	var input struct {
		Version string `json:"version"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	if input.Version == "" || len(input.Version) > 64 {
		mobileJSON(w, http.StatusBadRequest, nil, "version is required")
		return
	}
	asOf := ""
	if svc := bridge.briefSource(); svc != nil {
		if brief, ok := svc.GetMobileBrief(briefID); ok {
			asOf = brief.AsOf
		}
	}
	bridge.briefOutbox.acknowledge(device.DeviceID, briefID, asOf)
	mobileJSON(w, http.StatusOK, map[string]bool{"received": true}, "")
}

// serveBriefRefresh asks the brief service for a fresh brief. The service enforces
// the cooldown and reports it in the result.
func (bridge *mobileBridge) serveBriefRefresh(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	svc := bridge.briefSource()
	if svc == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileBriefUnavailable.Error())
		return
	}
	request := func(w http.ResponseWriter) {
		result, err := svc.RequestMobileBrief("phone")
		if err != nil {
			mobileJSON(w, http.StatusInternalServerError, nil, "the brief could not be started")
			return
		}
		mobileJSON(w, http.StatusOK, result, "")
	}
	if r.Body == http.NoBody || r.ContentLength == 0 {
		request(w)
		return
	}
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	if input.IdempotencyKey == "" {
		request(w)
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, request)
}

func (bridge *mobileBridge) serveDevices(w http.ResponseWriter) {
	now := time.Now()
	bridge.mu.Lock()
	devices := make([]MobileDeviceView, 0, len(bridge.devices))
	for _, record := range bridge.devices {
		if expires, ok := mobileParseTimestamp(record.ExpiresAt); ok && now.Before(expires) {
			devices = append(devices, record.publicView())
		}
	}
	bridge.mu.Unlock()
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].CreatedAt != devices[j].CreatedAt {
			return devices[i].CreatedAt < devices[j].CreatedAt
		}
		return devices[i].DeviceID < devices[j].DeviceID
	})
	mobileJSON(w, http.StatusOK, devices, "")
}

// serveSelfRevoke lets a device remove only itself over the envelope route.
func (bridge *mobileBridge) serveSelfRevoke(w http.ResponseWriter, device mobileDeviceRecord, deviceID string) {
	if deviceID != device.DeviceID {
		mobileJSON(w, http.StatusForbidden, nil, "a paired phone may only remove itself")
		return
	}
	if err := bridge.revokeDevice(deviceID); err != nil {
		if errors.Is(err, errMobileDeviceNotFound) {
			mobileJSON(w, http.StatusNotFound, nil, "device not found")
			return
		}
		mobileJSON(w, http.StatusInternalServerError, nil, "device removal could not be saved")
		return
	}
	mobileJSON(w, http.StatusOK, map[string]bool{"revoked": true}, "")
}

// mobileValidBriefID accepts the briefId forms of PROTOCOL.md section 8 only.
func mobileValidBriefID(id string) bool {
	if id == "" || len(id) > mobileBriefIDMaxLength {
		return false
	}
	for index := 0; index < len(id); index++ {
		char := id[index]
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		case char == '-', char == '_':
		default:
			return false
		}
	}
	return true
}

// mobilePathSegments splits a hub path into its non-empty segments.
func mobilePathSegments(path string) []string {
	segments := make([]string, 0, 4)
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		if segment != "" {
			segments = append(segments, segment)
		}
	}
	return segments
}
