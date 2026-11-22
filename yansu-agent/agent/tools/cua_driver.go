package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"

	"yansu-agent/internal/config"
)

// cua_driver.go exposes the cua-driver Computer-Use Agent
// (https://github.com/trycua/cua) as a single LLM tool. Yansu's main
// app bundles cua-driver and runs its `serve` daemon on a Unix socket
// under ~/.yansu-agent/run/cua-driver.sock; this tool dials that socket
// and proxies tool calls verbatim.
//
// The LLM only learns one tool name (`cua_driver`) and selects an
// upstream tool via the `name` field. Doing it that way (instead of
// registering 25 separate native tools, one per cua-driver tool) keeps
// the model's tool-list token spend bounded and lets cua-driver evolve
// its surface without churning yansu-agent. The tradeoff is that the
// JSON schema for `args` is `object` (no per-call validation) — we
// rely on the SKILL.md the wails-gui ships to teach the model the
// per-tool argument shapes.
//
// macOS-only: cua-driver only ships an Apple Silicon build today, and
// its TCC requirements (Accessibility + Screen Recording) don't have
// equivalents on Linux/Windows. On other hosts the tool returns a
// friendly "not available" error rather than refusing to register.

// cuaDaemonSocketPath returns the Unix socket the wails-gui daemon
// listens on. Must stay in sync with wails-gui/cua_driver_daemon.go's
// cuaDaemonSocketPath.
func cuaDaemonSocketPath() string {
	root, err := config.ConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(root, "run", "cua-driver.sock")
}

// cuaDriverToolCallTimeout caps the per-call deadline. cua-driver tools
// are mostly fast; the slow outliers (`launch_app` against a
// cold-starting heavy app, `get_window_state` on a huge AX tree) still
// finish well inside 30 s. Anything longer indicates a stuck daemon
// and we'd rather surface a timeout than block the agent loop.
const cuaDriverToolCallTimeout = 30 * time.Second

// cuaDriverDialBudget is how long we keep retrying socket dials before
// giving up. The agent cannot spawn the daemon itself; the Wails app owns
// that lifecycle and may be replacing a stale daemon or rebinding the proxy
// during launch. Give that self-heal path enough room so normal app-start
// races do not leak as model-visible errors.
const cuaDriverDialBudget = 12 * time.Second
const cuaDriverDialRetryInterval = 100 * time.Millisecond

// CuaDriverTool implements tools.Tool, proxying invocations to the
// cua-driver daemon socket Yansu manages.
type CuaDriverTool struct {
	socketPath string
}

// NewCuaDriverTool builds the tool with the standard socket path. We
// resolve the path eagerly (at registration) but only dial it lazily
// (per Execute) so a missing daemon doesn't block the runner from
// starting.
func NewCuaDriverTool() *CuaDriverTool {
	return &CuaDriverTool{socketPath: cuaDaemonSocketPath()}
}

func (*CuaDriverTool) Name() string { return "cua_driver" }

func (*CuaDriverTool) Description() string {
	return "Read any installed macOS app's INTERNAL data (AX tree + screenshot) AND drive " +
		"apps in the background via the bundled cua-driver — without stealing focus from " +
		"the user's frontmost app. The app does NOT have to be currently running: call " +
		"name=\"launch_app\" with args={\"bundle\":\"<id>\"} to start it in the background " +
		"(activates=false, focus-safe, no Space switch), then name=\"get_window_state\" " +
		"to read state. " +
		"For READ-ONLY queries about anything an app maintains — chat history, document " +
		"or code content, mail, web pages, player state, calendar events, file listings, " +
		"whether or not the app is open right now — call name=\"get_window_state\" with " +
		"args={\"bundle\":\"<bundle_id>\"} (preceded by launch_app if not running; use " +
		"name=\"list_apps\" whenever you don't know the app's bundle id — launch_app and get_window_state require the bundle id (e.g. com.apple.Notes), not a plain app name). NOTE: the daemon expects " +
		"the JSON key \"bundle\" (not \"bundle_id\") — use the \"bundle\" field returned " +
		"by list_apps. Works for " +
		"any kind of macOS-visible surface: " +
		"native AppKit apps, Electron / CEF apps, browser tabs, IDEs, terminals, menu-bar " +
		"apps. The returned AX tree usually includes content scrolled out of the visible " +
		"viewport when the app keeps off-screen DOM nodes, so a single read often surfaces " +
		"more than a screenshot would. DEFAULT TO ACCESSIBILITY, NOT PIXELS: call " +
		"get_window_state WITHOUT a screenshot (the AX tree + ui_elements refs are enough " +
		"for the vast majority of reads and actions) and act by ref / element_index. Request " +
		"a screenshot (include_screenshot=true) ONLY when the AX tree came back empty or " +
		"incomplete (ax_status=\"empty_after_wakeup\" with no usable visual_index), the " +
		"target is a canvas / video / WebGL / game surface, or the task genuinely needs " +
		"visual inspection. Each screenshot is heavy (~1-3 MB on the wire); attaching one to " +
		"every step is the #1 cause of request-too-large failures on small-window upstreams " +
		"— so treat screenshots as the exception, not the default. " +
		"cua_driver and activity-cli are NOT substitutes — " +
		"they cover DIFFERENT KINDS OF DATA, both first-class. cua_driver = live read " +
		"of any installed app's INTERNAL data (the app does NOT have to be running — " +
		"call name=\"launch_app\" with args={\"bundle\":\"<id>\"} first to start it in the " +
		"background, then get_window_state to read; launch_app is focus-safe with " +
		"activates=false, no Space switch). Coverage: anything the app itself maintains " +
		"— chat threads, documents, mail, web pages, player state, calendar events, " +
		"files. activity-cli = the user's EXPERIENCE archive: things no single app " +
		"keeps, namely cross-app daily narrative, mic-routed meeting transcripts (who " +
		"said what, separated by source), OCR'd snapshots of content that has since " +
		"been deleted/recalled/edited from the source, the user's click/keystroke " +
		"record. Decision: \"could I get this answer by opening the app and looking?\" " +
		"If yes → cua_driver (launch_app if needed). If the answer needs the user's " +
		"subjective experience, a removed message, or a multi-app story → activity-cli. " +
		"The choice does NOT hinge on verb tense, temporal markers, or whether the app " +
		"is currently running — it hinges on data ownership. cua_driver already retries " +
		"AX wake-up internally for Electron/CEF apps (Slack, Discord, VS Code, Notion, " +
		"Feishu, DingTalk, WeChat desktop, …) and runs Vision OCR as a built-in third " +
		"layer when the AX tree is unreadable, exposing OCR'd text regions as v* refs " +
		"in the visual_index field of the same response — so partial or empty AX alone " +
		"is NOT a reason to fall back to activity-cli for an app-data question. The " +
		"one narrow cross-over (cua_driver falls back to activity-cli on an app-data " +
		"question) only fires when cua_driver returned ax_status=\"empty_after_wakeup\" " +
		"AND no usable visual_index — i.e. both the AX and the OCR layer failed, typical " +
		"for games, video players, DRM windows, canvas/Metal/WebGL surfaces. " +
		"For automation, use the canonical loop: launch_app → get_window_state → action. " +
		"Re-snapshot AFTER an action only when needed — to verify a high-risk/irreversible " +
		"step, to confirm an input known to silently no-op (type_text into WebKit/Electron, " +
		"press_key on a minimized window), or to resolve a fresh ref for the next " +
		"action. Do NOT re-snapshot after every action; the next action's element resolution " +
		"surfaces genuine drift. Three click-addressing modes by ax_status from the snapshot: " +
		"a ref (a string handle like e3, copied verbatim) from ui_elements (normal AX " +
		"apps), v* visual refs from visual_index " +
		"(CEF/Electron/Chinese apps with empty AX trees — macOS Vision OCR with zh-Hans), " +
		"or raw {pid, x, y} pixel coords (bgclick path, focus-safe). The `name` field " +
		"selects a tool (get_window_state, open_url, intent, launch_app, click, type_text, " +
		"hotkey, scroll, screenshot, list_apps, list_windows, set_value, press_key, drag, " +
		"check_permissions, etc. — run `list-tools` to enumerate). The `args` field is a JSON object matching " +
		"that tool's inputSchema. Call this tool directly — the canonical loop above is " +
		"enough to start; you do NOT need to Read any SKILL.md first. A `cua` skill doc " +
		"covers edge-case details (ax_status branching, the no-foreground contract, " +
		"web-rendered-app patterns); consult it only if you actually hit one. " +
		"EFFICIENCY — avoid wasted steps (the #1 cause of slow CUA runs and of blowing " +
		"the context window with accumulated screenshots): " +
		"(1) READ THE OPENING MAP: your first cua_driver result is prefixed with an " +
		"inventory of the windows already open right now. Read it before navigating or " +
		"launching anything — if the window holding what you need is listed, call " +
		"get_window_state on it directly instead of opening a new window or navigating " +
		"from scratch. (Need a fresh list mid-task? call name=\"list_apps\".) " +
		"(2) NAVIGATE THE BROWSER WITH open_url, NEVER WITH BASH: to reach ANY web page or " +
		"view — a search result, a settings page, a specific repo / issue / doc / profile, " +
		"or a deep-link that encodes a query or filter — make ONE call with name=\"open_url\", " +
		"args={\"url\":\"<full URL>\"} (focus-safe via LaunchServices, opens in the user's " +
		"browser, zero clicking; optional args[\"bundle\"] forces a specific browser). This " +
		"applies to BOTH read and operate tasks: to change a setting or click a button on a " +
		"page, FIRST open_url to that exact page, THEN drive it with get_window_state + click / " +
		"type_text. When filtering, put the site's own query syntax IN the URL — e.g. Gmail " +
		"\"https://mail.google.com/mail/u/0/#search/newer_than:1d\" (after:/before:/is:unread), " +
		"GitHub \"https://github.com/settings/appearance\" or \".../<owner>/<repo>/issues?q=is:open\". " +
		"NEVER reach a page any other way: do NOT shell out to Bash `open <url>`, `osascript " +
		"'tell <browser> to open location'`, `curl`, or any browser HTTP API — those steal " +
		"focus, are slower, and several cannot operate the page at all; and do NOT use " +
		"launch_app to open a URL (it only starts the app, there is no urls arg). Click the " +
		"in-app address bar / search box only as a last resort when no URL can express the target. " +
		"After open_url returns success the page IS opening — do NOT verify or re-drive it with " +
		"curl, a browser HTTP API, or lsof at ANY point; just call get_window_state to read it. " +
		"(2b) OPERATE THE PAGE THROUGH ACCESSIBILITY, NOT BLIND PIXELS: once a page is open, to " +
		"click a button / radio / toggle / link, call get_window_state and dispatch by its ref " +
		"from ui_elements — web custom controls (GitHub theme cards, settings toggles, …) are " +
		"almost always AX-addressable, so a single AX read finds the ref. A screenshot + pixel " +
		"{x,y} click is a LAST resort, only when the AX tree genuinely lacks the target. If a " +
		"click did not land, RE-READ the AX tree for the correct ref — do NOT re-screenshot and " +
		"re-click; and NEVER click the same pixel coordinate twice blind. The screenshot → " +
		"blind-pixel → screenshot loop is the #1 stuck pattern: 5 screenshots for one toggle " +
		"means you skipped the AX tree — go back and read it. " +
		"(3) NO-PROGRESS STOP: if an action does not change the window title / URL / AX " +
		"state after two consecutive attempts, STOP repeating that path — switch to a query " +
		"or keyboard approach, or explain the blocker to the user; never keep retrying the " +
		"same non-committing navigation (it both wastes turns and piles up screenshots)."
}

func (*CuaDriverTool) JSONSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"type": "string",
				"description": "Upstream cua-driver tool name (snake_case). " +
					"Examples: launch_app, get_window_state, click, type_text, hotkey, " +
					"scroll, screenshot, list_apps, list_windows, set_value, press_key, " +
					"drag, check_permissions.",
			},
			"args": map[string]any{
				"type":        "object",
				"description": "JSON object matching the upstream tool's inputSchema. Defaults to {} when omitted.",
			},
		},
	}
}

// NeedsPermission returns false. cua-driver itself gates dangerous
// actions via macOS TCC (Accessibility + Screen Recording), which the
// user grants once at first use. Putting another permission prompt
// layer in front would just be UI fatigue.
func (*CuaDriverTool) NeedsPermission(map[string]any) (bool, PermissionPrompt) {
	return false, PermissionPrompt{}
}

func (t *CuaDriverTool) Execute(ctx context.Context, call ToolCall) (ToolResult, error) {
	name, _ := call.Input["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return ToolResult{}, fmt.Errorf("name is required (e.g. \"launch_app\", \"get_window_state\")")
	}

	args := call.Input["args"]
	if args == nil {
		args = map[string]any{}
	}
	args, err := prepareCuaCall(call, name, args)
	if err != nil {
		return ToolResult{}, err
	}

	if t.socketPath == "" {
		return ToolResult{}, errors.New("cua-driver socket path unresolved (no $HOME?); cannot reach the daemon")
	}

	deadline := time.Now().Add(cuaDriverToolCallTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}

	resp, err := dialAndCallCuaDaemon(ctx, t.socketPath, name, args, deadline)
	if err != nil {
		return ToolResult{}, err
	}
	if !resp.OK {
		// Surface the upstream error verbatim — cua-driver's messages are
		// already model-readable ("Unknown tool: …", "No cached AX state",
		// etc.). Wrapping them would just make the model parse two
		// layers of error.
		return ToolResult{}, fmt.Errorf("cua-driver: %s", resp.ErrorMessage())
	}
	if gated, ok := gateUnattendedCuaResult(ctx, call, name, resp.Result); ok {
		return gated, nil
	}

	// Most cua-driver tools return a payload like
	//   {"kind":"call","payload":{"content":[{"type":"text","text":"…"}]}}
	// We unwrap to the text block when it's the only content, and fall
	// back to the raw JSON otherwise (e.g. screenshot tools that return
	// image blocks). The model can read either; raw JSON keeps every
	// field accessible.
	if text, ok := unwrapCuaTextPayload(resp.Result); ok {
		return ToolResult{Content: text}, nil
	}
	return ToolResult{Content: string(resp.Result)}, nil
}

// cuaDaemonResponse mirrors the wire format documented under
// `cua-driver help serve`: {"ok":true,"result":...} or
// {"ok":false,"error":"…","exitCode":N}.
//
// Error is RawMessage rather than a string because Yansu's policy
// proxy emits the legacy string shape, but the bare alma daemon (which
// we may hit when the proxy is down or being bypassed) returns
// {code,message,hint}. Decoding both into a string field would explode
// the call with "cannot unmarshal object into Go struct field" —
// silencing the upstream error entirely. RawMessage absorbs either
// shape; read it via ErrorMessage().
type cuaDaemonResponse struct {
	OK       bool            `json:"ok"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    json.RawMessage `json:"error,omitempty"`
	ExitCode int             `json:"exitCode,omitempty"`
}

// ErrorMessage flattens whatever shape Error carries into a string.
// Returns "" when the response carries no error. The string form lands
// as-is; the object form ({code, message, hint}) is joined as
// "message (hint) [code]" so all three signals survive.
func (r *cuaDaemonResponse) ErrorMessage() string {
	if r == nil || len(r.Error) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(r.Error, &s); err == nil {
		return s
	}
	var obj struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	}
	if err := json.Unmarshal(r.Error, &obj); err == nil {
		out := obj.Message
		if obj.Hint != "" {
			if out != "" {
				out += " (" + obj.Hint + ")"
			} else {
				out = obj.Hint
			}
		}
		if obj.Code != "" {
			if out != "" {
				out += " [" + obj.Code + "]"
			} else {
				out = obj.Code
			}
		}
		return out
	}
	return string(r.Error)
}

// dialCuaDaemonWithRetry repeatedly dials the canonical cua-driver
// socket with a small per-attempt timeout, falling through quickly on
// success and budgeting cuaDriverDialBudget total before giving up.
// Mirrors Alma's pattern of "try-connect, fall through if daemon is
// already there; otherwise poll while spawn finishes." Yansu's daemon
// is spawned by the wails-gui process at app launch, not by yansu-cli,
// so we can only wait — but the wait makes app-launch races and
// reaper-restart windows invisible to the model.
//
// Retries cover all dial errors (refused / no-such-file / EADDR-in-use
// during rebind), not just connection-refused. Any dial error before
// the budget runs out is treated as transient.
//
//isoform:destination local
func dialCuaDaemonWithRetry(ctx context.Context, sock string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	deadline := time.Now().Add(cuaDriverDialBudget)
	var lastErr error
	for {
		conn, err := dialer.DialContext(ctx, "unix", sock)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, fmt.Errorf("cua-driver dial cancelled: %w", ctx.Err())
		}
		if !time.Now().Before(deadline) {
			break
		}
		// Sleep but respect ctx — if the caller's deadline expires
		// while we're waiting, surface that instead of the dial error.
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("cua-driver dial cancelled: %w", ctx.Err())
		case <-time.After(cuaDriverDialRetryInterval):
		}
	}
	// IMPORTANT: this error message is LLM-facing. Earlier wording ("Yansu
	// may not be running, or the bundled cua-driver isn't installed on this
	// platform") was systematically misread by the model as "this tool is
	// permanently unavailable, switch to a different one" — agents would
	// immediately fall back to activity-cli (historical OCR snapshots) for
	// present-moment questions that genuinely needed cua_driver. The
	// rephrasing below frames the failure as transient (which it almost
	// always is — wails-gui spawns the daemon eagerly at launch and respawns
	// it on crash, so a missed dial is normally a sub-second warm-up race
	// or a brief proxy rebind) and gives the model an explicit recovery
	// path that does NOT involve switching to a different data source.
	return nil, fmt.Errorf("cua-driver daemon at %s did not respond within %s — most likely the daemon is still warming up (Yansu just launched) or briefly restarting after a crash. RETRY this same cua_driver call once or twice with a short pause before considering other approaches; the daemon almost always recovers within a few seconds. If retries still fail, SURFACE THIS ERROR TO THE USER (\"Yansu's cua-driver daemon isn't responding — please restart Yansu or check Accessibility permissions\") rather than falling back to activity-cli (activity-cli covers the user's experience archive — cross-app narrative, mic-routed meeting transcripts, OCR snapshots of deleted content, click/keystroke stream — not the app's internal data that the user asked about, so substituting it returns the wrong kind of data). Underlying dial error: %w", sock, cuaDriverDialBudget, lastErr)
}

func dialAndCallCuaDaemon(ctx context.Context, sock, name string, args any, deadline time.Time) (*cuaDaemonResponse, error) {
	conn, err := dialCuaDaemonWithRetry(ctx, sock)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)

	req := struct {
		Method string `json:"method"`
		Name   string `json:"name"`
		Args   any    `json:"args"`
	}{"call", name, args}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal cua-driver request: %w", err)
	}
	payload = append(payload, '\n')
	if _, err := conn.Write(payload); err != nil {
		return nil, fmt.Errorf("write cua-driver request: %w", err)
	}

	// 4 MB read buffer mirrors the wails-gui side — get_window_state
	// can return a base64-encoded screenshot that pushes a single
	// response above 1 MB.
	r := bufio.NewReaderSize(conn, 4<<20)
	line, err := r.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read cua-driver response: %w", err)
	}
	if len(line) == 0 {
		return nil, fmt.Errorf("cua-driver returned empty response")
	}

	var resp cuaDaemonResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("decode cua-driver response: %w (raw: %.200s)", err, string(line))
	}
	return &resp, nil
}

// unwrapCuaTextPayload pulls a single text block out of cua-driver's
// CallTool.Result envelope so the model sees plain text instead of a
// nested JSON wrapper. We're conservative: when the payload has any
// non-text content (e.g. images from screenshot tools) or multiple
// blocks, we fall back to the raw JSON so nothing gets lost.
func unwrapCuaTextPayload(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var envelope struct {
		Kind    string `json:"kind"`
		Payload struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", false
	}
	if len(envelope.Payload.Content) != 1 {
		return "", false
	}
	if envelope.Payload.Content[0].Type != "text" {
		return "", false
	}
	return envelope.Payload.Content[0].Text, true
}
