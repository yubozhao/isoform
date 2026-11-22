package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
)

const webFetchMaxBytes = 2 * 1024 * 1024

// WebFetchTool fetches a URL and returns the text content. Refuses
// internal / private IPs to avoid being a SSRF helper.
type WebFetchTool struct{}

// NewWebFetchTool returns a WebFetch tool.
func NewWebFetchTool() *WebFetchTool { return &WebFetchTool{} }

func (*WebFetchTool) Name() string { return "WebFetch" }

func (*WebFetchTool) Description() string {
	return "Fetch a URL over HTTPS and return the text content. Refuses internal/private IPs. " +
		"Caps response at 2 MB. " +
		"Allowed URL sources: (a) URLs the user explicitly provided in the conversation, " +
		"(b) URLs that appeared verbatim in earlier tool output (search results, sitemaps, link tags in a previously-fetched page), " +
		"(c) known search/aggregator endpoints with a properly URL-encoded query parameter (e.g. https://news.google.com/search?q=...). " +
		"Do NOT fabricate article URLs by combining a known content site with a slug guessed from an article title or training data — " +
		"site-specific slug rules (mlb.com, espn.com, nytimes.com, etc.) are unpredictable and almost always 404. " +
		"If the response comes back with `[HTTP 4xx ...]` or `[HTTP 5xx ...]`, treat the URL as dead and do not retry it; pick a different allowed URL or stop fetching."
}

func (*WebFetchTool) JSONSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"url"},
		"properties": map[string]any{
			"url": map[string]any{"type": "string"},
		},
	}
}

func (*WebFetchTool) NeedsPermission(input map[string]any) (bool, PermissionPrompt) {
	return false, PermissionPrompt{}
}

//isoform:destination assistant_search
func (t *WebFetchTool) Execute(ctx context.Context, call ToolCall) (ToolResult, error) {
	raw, _ := call.Input["url"].(string)
	if raw == "" {
		return ToolResult{}, fmt.Errorf("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ToolResult{}, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "https" {
		return ToolResult{}, fmt.Errorf("only https:// URLs are allowed")
	}
	if err := refusePrivateHost(u.Hostname()); err != nil {
		return ToolResult{}, err
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := refusePrivateHost(req.URL.Hostname()); err != nil {
				return err
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ToolResult{}, err
	}
	req.Header.Set("user-agent", "Yansu-Agent/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return ToolResult{}, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, webFetchMaxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return ToolResult{}, fmt.Errorf("read body: %w", err)
	}
	truncated := len(body) > webFetchMaxBytes
	if truncated {
		body = body[:webFetchMaxBytes]
	}
	ct := resp.Header.Get("content-type")
	text := extractContent(body, u, ct)
	if truncated {
		text += "\n(response truncated at 2 MB)"
	}

	// Surface HTTP errors to the model. Without this the model sees a
	// 404 page body as a normal successful fetch (HTML is HTML), reads
	// the friendly "page not found" copy, and retries the exact same
	// dead URL turn after turn — observed in the wild as a 50-turn loop
	// re-fetching three hallucinated MLB.com slugs 42 times each.
	// Prefixing the status line + flipping IsError gives the model a
	// machine-readable signal that "retrying this URL won't help".
	isErr := resp.StatusCode >= 400
	if isErr {
		text = httpErrorPrefix(resp.StatusCode, u.String()) + text
	}

	return ToolResult{
		Content:  text,
		IsError:  isErr,
		Metadata: map[string]any{"status": resp.StatusCode, "content_type": ct, "truncated": truncated},
	}, nil
}

// httpErrorPrefix builds the human + model-readable banner prepended to a
// non-2xx WebFetch body. Pulled out so the wording is testable without
// driving a full HTTPS request through the tool.
func httpErrorPrefix(status int, requestedURL string) string {
	return fmt.Sprintf("[HTTP %d %s] WebFetch failed for %s — the upstream server reported an error. Do not retry the same URL; pick a different source or stop and report what you found so far.\n\n",
		status, http.StatusText(status), requestedURL)
}

// extractContent uses Mozilla Readability to extract article content
// from HTML, producing much smaller output than naive tag-stripping.
// Falls back to regex-based extractText for non-HTML or when
// readability fails (e.g. pages without article structure like API
// docs, dashboards, error pages).
func extractContent(body []byte, pageURL *url.URL, contentType string) string {
	s := string(body)
	if !strings.Contains(contentType, "html") && !strings.Contains(s, "<") {
		return s
	}
	article, err := readability.FromReader(bytes.NewReader(body), pageURL)
	if err == nil && article.Node != nil {
		var buf bytes.Buffer
		if renderErr := article.RenderText(&buf); renderErr == nil {
			text := strings.TrimSpace(buf.String())
			if len(text) > 200 {
				var header string
				if t := article.Title(); t != "" {
					header = "# " + t + "\n\n"
				}
				if b := article.Byline(); b != "" {
					header += b + "\n\n"
				}
				log.Printf("[WebFetch] readability extracted %d bytes (raw HTML was %d bytes)", len(header)+len(text), len(body))
				return header + text
			}
		}
	}
	return extractTextFallback(s)
}

var tagRe = regexp.MustCompile(`(?is)<[^>]*>`)
var scriptRe = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
var styleRe = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
var whitespaceRe = regexp.MustCompile(`[ \t]+`)
var newlineRe = regexp.MustCompile(`\n{3,}`)

// extractTextFallback strips HTML tags via regex. Used when readability
// cannot extract article content (non-article pages, API responses, etc).
func extractTextFallback(html string) string {
	if !strings.Contains(html, "<") {
		return html
	}
	html = scriptRe.ReplaceAllString(html, "")
	html = styleRe.ReplaceAllString(html, "")
	html = tagRe.ReplaceAllString(html, " ")
	html = whitespaceRe.ReplaceAllString(html, " ")
	html = newlineRe.ReplaceAllString(html, "\n\n")
	return strings.TrimSpace(html)
}

// refusePrivateHost rejects hostnames that resolve to loopback, link-local,
// private, or cloud-metadata address ranges. SSRF guard per doc §4.4.8.
func refusePrivateHost(host string) error {
	if host == "" {
		return fmt.Errorf("missing host in URL")
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("localhost URLs are refused")
	}
	// If it's an IP literal, check directly. Otherwise resolve.
	if ip := net.ParseIP(host); ip != nil {
		return checkIP(ip)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("dns lookup: %w", err)
	}
	for _, ip := range ips {
		if err := checkIP(ip); err != nil {
			return err
		}
	}
	return nil
}

func checkIP(ip net.IP) error {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("URL resolves to a restricted address (%s)", ip.String())
	}
	// Cloud metadata addresses.
	if ip.Equal(net.IPv4(169, 254, 169, 254)) {
		return fmt.Errorf("URL resolves to the cloud metadata endpoint")
	}
	return nil
}

// WebSearchTool is a lightweight stub: this build does not ship a hosted
// search backend (no API key configured), so calling WebSearch always
// fails. The failure message redirects the model to opencli via Bash,
// which is the supported search path for the Yansu desktop app.
type WebSearchTool struct{}

// NewWebSearchTool returns a stub search tool.
func NewWebSearchTool() *WebSearchTool { return &WebSearchTool{} }

func (*WebSearchTool) Name() string { return "WebSearch" }

func (*WebSearchTool) Description() string {
	return "DEPRECATED in this build — no hosted search backend / API key is configured. " +
		"Do NOT call this tool. For any web search, research, or look-up, run `opencli` via the Bash tool instead. " +
		"opencli is pre-installed and on PATH; the smart-search skill (listed under <user-installed-skills>) has the full routing rules. " +
		"Typical flow: `opencli list -f yaml` -> pick an AI source (grok / gemini / doubao) or a specialized adapter -> `opencli <site> -h` -> `opencli <site> <command> ...`."
}

func (*WebSearchTool) JSONSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"query"},
		"properties": map[string]any{
			"query": map[string]any{"type": "string"},
		},
	}
}

func (*WebSearchTool) NeedsPermission(input map[string]any) (bool, PermissionPrompt) {
	return false, PermissionPrompt{}
}

func (t *WebSearchTool) Execute(_ context.Context, call ToolCall) (ToolResult, error) {
	q, _ := call.Input["query"].(string)
	if q == "" {
		return ToolResult{}, fmt.Errorf("query is required")
	}
	// IsError=true so the model treats this as a real failure (counts
	// toward the toolErrorStreak guard in native/loop.go) instead of
	// reading the message as a successful WebSearch result. The previous
	// wording recommended a Google News aggregator + WebFetch, which the
	// model interpreted as "guess URLs and fetch them" — a 50-turn cron
	// failure where 49 fabricated URLs 404'd. This build instead points
	// the model at opencli, which is the supported search backend.
	return ToolResult{
		Content: "WebSearch is not configured in this build (no hosted search API key). " +
			"Use opencli via the Bash tool for this query: \"" + q + "\". " +
			"opencli is already installed and on PATH. Recommended workflow:\n" +
			"  1. `opencli list -f yaml` — discover available adapters.\n" +
			"  2. Read the smart-search SKILL.md listed in <user-installed-skills> for source-selection rules (AI sources: grok / gemini / doubao; specialized sources for tech / social / shopping / etc.).\n" +
			"  3. `opencli <site> -h` to see subcommands for the chosen adapter, then `opencli <site> <command> --query \"...\"` (or the adapter's argument shape from -h) to actually run the search.\n" +
			"Do NOT fabricate article URLs and pass them to WebFetch — site-specific slugs (mlb.com, espn.com, nytimes.com, etc.) are unpredictable and will return 404 or off-topic landing pages.",
		IsError:  true,
		Metadata: map[string]any{"query": q, "configured": false, "preferred_backend": "opencli"},
	}, nil
}
