package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"yansu-agent/internal/client"
)

// errReplaced is returned when the backend sends a "replaced" message, indicating
// another daemon has taken over this identity.
var errReplaced = errors.New("replaced by another daemon")

// RelayAdapter connects to the backend via WebSocket and receives events
// routed by the centralized DaemonHub. It implements the Adapter interface.
type RelayAdapter struct {
	backendURL  string // base URL, e.g. "https://dashboard.yansu.ai"
	token       string // yansu login token for WS auth
	teamID      string // Slack workspace ID
	slackUserID string // Slack user ID
	conn        *websocket.Conn
	sendMu      sync.Mutex
	apiClient   *client.Client // reused across FetchContext calls
}

// NewRelayAdapter creates a new relay adapter.
func NewRelayAdapter(backendURL, token, teamID, slackUserID string) *RelayAdapter {
	return &RelayAdapter{
		backendURL:  backendURL,
		token:       token,
		teamID:      teamID,
		slackUserID: slackUserID,
		apiClient:   client.New(backendURL, token),
	}
}

// Listen connects to the backend relay WS and calls handler for each incoming event.
// Reconnects with exponential backoff on disconnect until ctx is cancelled.
// On "replaced" (another daemon took over), enters dormant backoff (30s → 10min).
func (a *RelayAdapter) Listen(ctx context.Context, handler func(Event)) error {
	backoff := 2 * time.Second
	dormant := false

	for {
		if dormant {
			// After being replaced, use longer dormant backoff
			backoff = 30 * time.Second
			dormant = false
		}

		err := a.connectAndListen(ctx, handler)
		if ctx.Err() != nil {
			return nil // clean shutdown
		}

		if errors.Is(err, errReplaced) {
			fmt.Printf("[%s] Relay: Another daemon took over this identity. Entering dormant state.\n",
				time.Now().Format("2006-01-02 15:04:05"))
			dormant = true
			// Use dormant backoff for "replaced" events
			select {
			case <-time.After(30 * time.Second):
			case <-ctx.Done():
				return nil
			}
			continue
		}

		fmt.Printf("[%s] Relay disconnected: %v. Reconnecting in %v...\n",
			time.Now().Format("2006-01-02 15:04:05"), err, backoff)
		select {
		case <-time.After(backoff):
			if backoff*2 < 5*time.Minute {
				backoff *= 2
			} else {
				backoff = 5 * time.Minute
			}
		case <-ctx.Done():
			return nil
		}
	}
}

//isoform:destination yansu_backend
func (a *RelayAdapter) connectAndListen(ctx context.Context, handler func(Event)) error {
	// Build WS URL from backend base URL
	wsURL := a.backendURL
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL = strings.TrimRight(wsURL, "/") + "/api/daemon/ws"

	header := http.Header{}
	header.Set("Authorization", "Bearer "+a.token)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		return fmt.Errorf("dial backend: %w", err)
	}
	a.conn = conn
	defer func() {
		conn.Close()
		a.conn = nil
	}()

	// Send register message
	regMsg, _ := json.Marshal(map[string]string{
		"type":          "register",
		"team_id":       a.teamID,
		"slack_user_id": a.slackUserID,
		"client_type":   "cli",
	})
	if err := conn.WriteMessage(websocket.TextMessage, regMsg); err != nil {
		return fmt.Errorf("send register: %w", err)
	}

	// Read registration ack
	_, ackRaw, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read register ack: %w", err)
	}
	var ack struct {
		Type   string `json:"type"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(ackRaw, &ack); err != nil {
		return fmt.Errorf("parse register ack: %w", err)
	}
	if ack.Status != "ok" {
		return fmt.Errorf("registration rejected: %s", ack.Error)
	}

	fmt.Printf("[%s] Connected to backend relay as %s:%s\n",
		time.Now().Format("2006-01-02 15:04:05"), a.teamID, a.slackUserID)

	// Set up ping handler to refresh the read deadline when the backend sends
	// WebSocket pings. Without this, idle connections time out every ~90s because
	// gorilla/websocket handles pings internally without resetting the deadline.
	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		a.sendMu.Lock()
		defer a.sendMu.Unlock()
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
	})

	// Close the connection when ctx is cancelled to unblock ReadMessage immediately.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stopWatch:
		}
	}()

	// Start heartbeat goroutine
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	go a.heartbeat(heartbeatCtx)

	// Read loop
	conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))

		var base struct {
			Type    string          `json:"type"`
			RelayID string          `json:"relay_id"`
			Event   json.RawMessage `json:"event"`
		}
		if err := json.Unmarshal(msg, &base); err != nil {
			continue
		}

		switch base.Type {
		case "event":
			var evt struct {
				UserID    string `json:"user_id"`
				ChannelID string `json:"channel_id"`
				ThreadTS  string `json:"thread_ts"`
				Text      string `json:"text"`
				EventTS   string `json:"event_ts"`
				TeamID    string `json:"team_id"`
				Platform  string `json:"platform"`
				EventType string `json:"event_type"`
				ChatType  string `json:"chat_type"`
				Files     []struct {
					ID          string `json:"id"`
					Name        string `json:"name"`
					MimeType    string `json:"mimetype"`
					URL         string `json:"url_private"`
					DownloadURL string `json:"url_private_download"`
					Size        int    `json:"size"`
					Thumb       string `json:"thumb_360"`
				} `json:"files"`
			}
			if err := json.Unmarshal(base.Event, &evt); err != nil {
				continue
			}

			// Drop passively-monitored channel messages before they reach the
			// router: the CLI daemon must only act on DMs and @mentions, never
			// on ambient channel conversation between other people. Skip the
			// event_ack (there is no ack timer for passive fanout) and send
			// stream_end so the backend cleans up any relay context.
			if IsPassiveChannelEvent(Event{Platform: evt.Platform, EventType: evt.EventType, ChatType: evt.ChatType}) {
				a.sendJSON(map[string]string{
					"type":     "stream_end",
					"relay_id": base.RelayID,
				})
				continue
			}

			// Send ack immediately
			a.sendJSON(map[string]string{
				"type":     "event_ack",
				"relay_id": base.RelayID,
			})

			// Convert files to EventFile slice
			var files []EventFile
			for _, f := range evt.Files {
				dlURL := f.DownloadURL
				if dlURL == "" {
					dlURL = f.URL
				}
				files = append(files, EventFile{
					ID:       f.ID,
					Name:     f.Name,
					MimeType: f.MimeType,
					URL:      dlURL,
					Size:     f.Size,
					Thumb:    f.Thumb,
				})
			}

			event := Event{
				UserID:    evt.UserID,
				ChannelID: evt.ChannelID,
				ThreadTS:  evt.ThreadTS,
				Text:      evt.Text,
				EventTS:   evt.EventTS,
				TeamID:    strings.TrimSpace(firstNonEmpty(evt.TeamID, a.teamID)),
				Platform:  evt.Platform,
				EventType: evt.EventType,
				ChatType:  evt.ChatType,
				RelayID:   base.RelayID,
				Files:     files,
			}
			handler(event)

		case "replaced":
			return errReplaced
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// heartbeat sends periodic heartbeat messages to keep the route key alive.
func (a *RelayAdapter) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.sendJSON(map[string]string{"type": "heartbeat"})
		case <-ctx.Done():
			return
		}
	}
}

// Reply sends a reply message through the relay.
func (a *RelayAdapter) Reply(event Event, text string) {
	if event.RelayID == "" {
		return
	}
	a.sendJSON(map[string]string{
		"type":     "reply",
		"relay_id": event.RelayID,
		"text":     text,
	})
}

// PostAndStream sends stream messages through the relay.
func (a *RelayAdapter) PostAndStream(event Event, ch <-chan string) {
	if event.RelayID == "" {
		for range ch {
		}
		return
	}

	a.sendJSON(map[string]string{
		"type":     "stream_start",
		"relay_id": event.RelayID,
	})

	for line := range ch {
		a.sendJSON(map[string]string{
			"type":     "stream_chunk",
			"relay_id": event.RelayID,
			"text":     line,
		})
	}

	a.sendJSON(map[string]string{
		"type":     "stream_end",
		"relay_id": event.RelayID,
	})
}

// sendJSON marshals and writes a JSON message to the WebSocket, mutex-protected.
func (a *RelayAdapter) sendJSON(v any) {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	if a.conn == nil {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	a.conn.WriteMessage(websocket.TextMessage, data)
}

// FetchContext retrieves conversation context via the backend REST API.
// Returns nil, nil when no relay ID is present (nothing to fetch).
func (a *RelayAdapter) FetchContext(ctx context.Context, event Event) ([]ContextMessage, error) {
	if event.RelayID == "" {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	resp, err := a.apiClient.DaemonFetchContext(ctx, event.RelayID)
	if err != nil {
		return nil, fmt.Errorf("DaemonFetchContext: %w", err)
	}

	msgs := make([]ContextMessage, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		msgs = append(msgs, ContextMessage{
			UserID:    m.UserID,
			Text:      m.Text,
			Timestamp: m.Timestamp,
		})
	}
	return msgs, nil
}
