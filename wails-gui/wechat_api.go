package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"wails-gui/internal/destinations"
)

const (
	wechatDefaultBaseURL    = "https://ilinkai.weixin.qq.com"
	wechatDefaultCDNBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"
	wechatPollTimeout       = 40 // seconds
)

// wechatAPIClient communicates with the iLink Bot API.
//
// Two HTTP clients because the long-poll and media-download paths have
// incompatible timeout requirements:
//   - client: no overall Timeout (getupdates is a server-controlled long-poll).
//   - mediaClient: 60s overall cap + redirect allowlist so a slow-trickle or
//     SSRF-via-30x CDN response can't pin the per-sender thread lock.
type wechatAPIClient struct {
	baseURL     string
	botToken    string
	client      *http.Client
	mediaClient *http.Client
}

// wechatMediaDownloadTimeout bounds a single CDN media download. Picked well
// above a normal 20MB image over mobile and well below the 10-minute per-turn
// task timeout, so a stuck download fails fast without dragging the thread lock.
const wechatMediaDownloadTimeout = 60 * time.Second

//isoform:destination chat_bridges
func newWeChatAPIClient(baseURL, botToken string) *wechatAPIClient {
	if baseURL == "" {
		baseURL = wechatDefaultBaseURL
	}
	// No overall client timeout — getupdates is a long-poll the server
	// controls. But a bare http.Client{} also leaves transport-level
	// timeouts unset, so a stalled TLS handshake or header read can pin
	// the goroutine + FD indefinitely and keep the network interface awake.
	// Clone DefaultTransport so we inherit sane dial/keepalive/idle settings
	// and add explicit handshake + response-header deadlines.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = time.Duration(wechatPollTimeout+15) * time.Second

	// Separate transport so media downloads get a tighter response-header
	// deadline without affecting long-poll behaviour.
	mediaTransport := http.DefaultTransport.(*http.Transport).Clone()
	mediaTransport.TLSHandshakeTimeout = 10 * time.Second
	mediaTransport.ResponseHeaderTimeout = 20 * time.Second

	return &wechatAPIClient{
		baseURL:  baseURL,
		botToken: botToken,
		client:   &http.Client{Transport: transport},
		mediaClient: &http.Client{
			Transport:     mediaTransport,
			Timeout:       wechatMediaDownloadTimeout,
			CheckRedirect: checkWeChatCDNRedirect,
		},
	}
}

// checkWeChatCDNRedirect re-applies the scheme + host allowlist on every
// redirect hop. Go's default follows up to 10 redirects transparently, which
// would let a CDN 30x point `mediaClient` at an internal/non-HTTPS address
// and bypass the entry-URL check.
func checkWeChatCDNRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return fmt.Errorf("too many redirects")
	}
	if req.URL.Scheme != "https" || !isAllowedWeChatCDNHost(req.URL.Hostname()) {
		return fmt.Errorf("redirect to disallowed host: %s", req.URL.Host)
	}
	return nil
}

// randomWechatUIN generates a random X-WECHAT-UIN header value.
// The iLink API requires this on every request.
func randomWechatUIN() string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	n := binary.LittleEndian.Uint32(buf)
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", n)))
}

//isoform:destination chat_bridges
func (c *wechatAPIClient) doRequest(method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	reqURL := c.baseURL + path
	req, err := http.NewRequest(method, reqURL, reader)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.botToken)
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("X-WECHAT-UIN", randomWechatUIN())
	req.Header.Set("Content-Type", "application/json")

	// Suppress per-request logging for the high-frequency getupdates poll.
	verbose := path != "/ilink/bot/getupdates"
	if verbose {
		log.Printf("[wechat-api] %s %s", method, path)
	}
	if path == "/ilink/bot/sendmessage" {
		req = destinations.CountRequest(req, destinations.ChatBridges)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		log.Printf("[wechat-api] %s %s error: %v", method, path, err)
		return nil, fmt.Errorf("do request %s: %w", path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if verbose {
		log.Printf("[wechat-api] %s %s -> HTTP %d (%d bytes)", method, path, resp.StatusCode, len(respBody))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// GetQRCode fetches a new QR code for WeChat login.
func (c *wechatAPIClient) GetQRCode() (*wechatQRCodeResponse, error) {
	data, err := c.doRequest("GET", "/ilink/bot/get_bot_qrcode?bot_type=3", nil)
	if err != nil {
		return nil, err
	}
	var resp wechatQRCodeResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse qrcode response: %w", err)
	}
	if resp.Ret != 0 {
		return nil, fmt.Errorf("qrcode error ret=%d", resp.Ret)
	}
	return &resp, nil
}

// GetQRCodeStatus polls the QR code scan status.
func (c *wechatAPIClient) GetQRCodeStatus(qrcode string) (*wechatQRCodeStatusResponse, error) {
	path := "/ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)
	data, err := c.doRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	var resp wechatQRCodeStatusResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse qrcode status: %w", err)
	}
	return &resp, nil
}

// GetUpdates long-polls for new messages.
func (c *wechatAPIClient) GetUpdates(cursor string) (*wechatGetUpdatesResponse, error) {
	body := wechatGetUpdatesRequest{
		GetUpdatesBuf: cursor,
		BaseInfo:      defaultBaseInfo,
	}
	data, err := c.doRequest("POST", "/ilink/bot/getupdates", body)
	if err != nil {
		return nil, err
	}
	var resp wechatGetUpdatesResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse getupdates: %w", err)
	}
	// Log non-trivial responses (skip -14 timeout noise). The body holds the
	// messages themselves, so only its size is logged.
	if resp.Errcode != -14 {
		log.Printf("[wechat-api] getUpdates: errcode=%d msgs=%d bytes=%d",
			resp.Errcode, len(resp.Msgs), len(data))
	}
	return &resp, nil
}

// SendMessage sends a message to a WeChat user.
func (c *wechatAPIClient) SendMessage(req *wechatSendMessageRequest) error {
	// Log file sends by size only; the body holds the message itself.
	if reqJSON, err := json.Marshal(req); err == nil {
		hasFile := false
		for _, item := range req.Msg.ItemList {
			if item.FileItem != nil {
				hasFile = true
				break
			}
		}
		if hasFile {
			log.Printf("[wechat-api] sendmessage (file): items=%d bytes=%d", len(req.Msg.ItemList), len(reqJSON))
		}
	}

	data, err := c.doRequest("POST", "/ilink/bot/sendmessage", req)
	if err != nil {
		return err
	}
	var resp wechatSendMessageResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("parse sendmessage: %w", err)
	}
	if resp.Errcode != 0 {
		return fmt.Errorf("sendmessage error %d: %s", resp.Errcode, resp.Errmsg)
	}
	return nil
}

// GetUploadURL requests CDN upload parameters for outbound media.
func (c *wechatAPIClient) GetUploadURL(req *wechatGetUploadURLRequest) (*wechatGetUploadURLResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("getuploadurl request is nil")
	}
	req.BaseInfo = defaultBaseInfo

	data, err := c.doRequest("POST", "/ilink/bot/getuploadurl", req)
	if err != nil {
		return nil, err
	}
	var resp wechatGetUploadURLResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse getuploadurl: %w", err)
	}
	if resp.Ret != 0 {
		return nil, fmt.Errorf("getuploadurl ret=%d: %s", resp.Ret, resp.Errmsg)
	}
	return &resp, nil
}

// UploadToCDN uploads AES-128-ECB encrypted bytes to the CDN URL and returns
// the encrypt_query_param from the response header x-encrypted-param.
//
// The scheme + host allowlist mirrors DownloadAndDecryptMedia: without it, a
// hostile getuploadurl response (or tampered BaseURL) could turn this into an
// SSRF-style POST primitive. mediaClient.CheckRedirect re-applies the same
// check on every 30x hop.
//
//isoform:destination chat_bridges
func (c *wechatAPIClient) UploadToCDN(uploadURL string, encryptedData []byte) (string, error) {
	parsed, err := url.Parse(uploadURL)
	if err != nil {
		return "", fmt.Errorf("parse upload URL: %w", err)
	}
	if parsed.Scheme != "https" || !isAllowedWeChatCDNHost(parsed.Hostname()) {
		return "", fmt.Errorf("upload URL host not allowed: %s", parsed.Host)
	}

	req, err := http.NewRequest(http.MethodPost, uploadURL, bytes.NewReader(encryptedData))
	if err != nil {
		return "", fmt.Errorf("build CDN upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	req = destinations.CountRequest(req, destinations.ChatBridges)
	resp, err := c.mediaClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("CDN upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("CDN upload status %d: %s", resp.StatusCode, string(body))
	}

	encryptQueryParam := resp.Header.Get("X-Encrypted-Param")
	if encryptQueryParam == "" {
		return "", fmt.Errorf("CDN upload: missing x-encrypted-param in response")
	}
	return encryptQueryParam, nil
}

// GetConfig fetches bot config (typing ticket) for a specific user conversation.
func (c *wechatAPIClient) GetConfig(userID, contextToken string) (*wechatGetConfigResponse, error) {
	body := wechatGetConfigRequest{
		IlinkUserID:  userID,
		ContextToken: contextToken,
		BaseInfo:     defaultBaseInfo,
	}
	data, err := c.doRequest("POST", "/ilink/bot/getconfig", body)
	if err != nil {
		return nil, err
	}
	var resp wechatGetConfigResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse getconfig: %w", err)
	}
	if resp.Ret != 0 {
		return nil, fmt.Errorf("getconfig ret=%d: %s", resp.Ret, resp.Errmsg)
	}
	return &resp, nil
}

// SendTyping sends a typing indicator to a WeChat user.
func (c *wechatAPIClient) SendTyping(userID, typingTicket string) error {
	body := wechatSendTypingRequest{
		IlinkUserID:  userID,
		TypingTicket: typingTicket,
		Status:       1, // 1 = typing
		BaseInfo:     defaultBaseInfo,
	}
	_, err := c.doRequest("POST", "/ilink/bot/sendtyping", body)
	return err
}

// --- Static QR code login client (no token needed) ---

// wechatLoginClient is used for the QR code login flow before we have a bot token.
type wechatLoginClient struct {
	client *http.Client
}

//isoform:destination chat_bridges
func newWeChatLoginClient() *wechatLoginClient {
	return &wechatLoginClient{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

//isoform:destination chat_bridges
func (c *wechatLoginClient) doGet(rawURL string, extraHeaders map[string]string) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-WECHAT-UIN", randomWechatUIN())
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// GetQRCode fetches a QR code for login (no auth needed).
func (c *wechatLoginClient) GetQRCode() (*wechatQRCodeResponse, error) {
	data, err := c.doGet(wechatDefaultBaseURL+"/ilink/bot/get_bot_qrcode?bot_type=3", nil)
	if err != nil {
		return nil, err
	}
	var resp wechatQRCodeResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse qrcode: %w", err)
	}
	if resp.Ret != 0 {
		return nil, fmt.Errorf("qrcode error ret=%d", resp.Ret)
	}
	return &resp, nil
}

// GetQRCodeStatus polls for scan result (no auth needed).
func (c *wechatLoginClient) GetQRCodeStatus(qrcode string) (*wechatQRCodeStatusResponse, error) {
	rawURL := wechatDefaultBaseURL + "/ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)
	data, err := c.doGet(rawURL, map[string]string{"iLink-App-ClientVersion": "1"})
	if err != nil {
		return nil, err
	}
	var resp wechatQRCodeStatusResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse status: %w", err)
	}
	return &resp, nil
}
