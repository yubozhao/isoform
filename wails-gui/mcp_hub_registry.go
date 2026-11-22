package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The MCP "registry" we surface in the hub combines two sources:
//
//  1. modelcontextprotocol/servers — the official reference-server list
//     maintained by the protocol authors. Each entry installs
//     deterministically as `npx -y @modelcontextprotocol/server-<slug>`,
//     so we can offer a one-click Install button.
//
//  2. punkpeye/awesome-mcp-servers — the de-facto community catalog
//     (hundreds of entries grouped by category). Install recipes vary
//     wildly across these repos, so we surface them as informational
//     rows pointing at the source repo. The user follows the link and
//     installs manually.
//
// Combining both gives one searchable registry for the user without
// pretending the "Install" button is safe for arbitrary community work.
const (
	mcpRegistrySourceModelcontextprotocol = "https://raw.githubusercontent.com/modelcontextprotocol/servers/main/README.md"
	mcpRegistrySourcePunkpeye             = "https://raw.githubusercontent.com/punkpeye/awesome-mcp-servers/main/README.md"

	mcpRegistryCacheFile = "registry.json"
	mcpRegistryTTL       = 24 * time.Hour
	mcpRegistryFetchTO   = 20 * time.Second
	mcpRegistryMaxBytes  = 4 << 20 // 4 MiB; punkpeye is ~750 KB today.

	mcpRegistryKindReference  = "reference"
	mcpRegistryKindThirdParty = "third-party"
)

var (
	// mcpRegistryEntryRegexp matches markdown bullets of the form
	//   - **[Name](link)** - description
	//   - [Name](link) - description
	//   - [Name](link) 📇 ☁️ - description       (punkpeye uses emoji tags)
	// Description capture is greedy to end-of-line and gets cleaned up
	// post-hoc (badges/emojis stripped) rather than baked into the regex.
	mcpRegistryEntryRegexp = regexp.MustCompile(
		`(?m)^\s*-\s+\*?\*?\[([^\]]+)\]\(([^)]+)\)\*?\*?\s*(.*)$`,
	)

	// mcpRegistryHeadingRegexp finds H2/H3 headings. Captures the level
	// (### or ##) and the heading text — we need the level to maintain a
	// proper H2/H3 stack rather than treating headings as a flat list.
	mcpRegistryHeadingRegexp = regexp.MustCompile(`(?m)^(#{2,3})\s+(.+?)\s*$`)

	// mcpRegistryRefSlugRegexp pulls the trailing src/<name> segment from
	// a modelcontextprotocol/servers Reference Servers link. Used to
	// derive the @modelcontextprotocol/server-<slug> npm package id.
	mcpRegistryRefSlugRegexp = regexp.MustCompile(`/(?:tree/[^/]+/)?src/([A-Za-z0-9_.-]+)/?$`)

	// mcpRegistryHTMLAnchorRegexp strips inline HTML anchors like
	// `<a name="aggregators"></a>` that punkpeye uses inside its H3
	// headings.
	mcpRegistryHTMLAnchorRegexp = regexp.MustCompile(`<a\s+name="[^"]*">\s*</a>`)

	// mcpRegistryImageRegexp strips the `[![alt](badge.png)](link)` image
	// badges punkpeye glues onto entry rows.
	mcpRegistryImageRegexp = regexp.MustCompile(`!\[[^\]]*\]\([^)]+\)`)

	mcpRegistryFetchMu sync.Mutex
)

// mcpRegistryThirdPartyHostAllowList constrains which URL hosts count
// as "real" third-party MCP server entries. punkpeye's README also
// references articles, twitter handles, glama.ai badges, and
// discussions URLs — we drop those because they're not installable.
var mcpRegistryThirdPartyHostAllowList = map[string]struct{}{
	"github.com":     {},
	"www.github.com": {},
	"gitlab.com":     {},
	"www.gitlab.com": {},
	"bitbucket.org":  {},
}

// mcpRegistryPunkpeyeSkipH2 is the set of H2 sections in punkpeye's
// README that don't list servers. Anything under these is dropped.
var mcpRegistryPunkpeyeSkipH2 = map[string]bool{
	"What is MCP?":    true,
	"Clients":         true,
	"Tutorials":       true,
	"Community":       true,
	"Legend":          true,
	"Frameworks":      true,
	"Tips and Tricks": true,
	"Tips & Tricks":   true,
	"Star History":    true,
	"Resources":       true,
}

// McpRegistryEntry is one catalog row. The view is small on purpose —
// the frontend renders a list of these, then the user clicks "Install"
// (reference) or follows the source link (third-party).
type McpRegistryEntry struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	SourceURL   string   `json:"source_url"`
	Section     string   `json:"section"` // human-readable display category (H3 → H2 fallback)
	Origin      string   `json:"origin"`  // which README we got this from
	Kind        string   `json:"kind"`    // mcpRegistryKind* constant
	NpmPackage  string   `json:"npm_package,omitempty"`
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	Installable bool     `json:"installable"`
}

type mcpRegistryCache struct {
	FetchedAt time.Time          `json:"fetched_at"`
	Sources   []string           `json:"sources"`
	Entries   []McpRegistryEntry `json:"entries"`
}

// SearchMcpRegistry returns the catalog filtered by the (optional) query.
// The first call within mcpRegistryTTL fetches and caches; subsequent
// calls are local-only.
func (a *App) SearchMcpRegistry(query string) ([]McpRegistryEntry, error) {
	cache, err := a.loadOrFetchMcpRegistry()
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return cache.Entries, nil
	}
	out := make([]McpRegistryEntry, 0, len(cache.Entries))
	for _, entry := range cache.Entries {
		hay := strings.ToLower(entry.Name + "\n" + entry.Slug + "\n" + entry.Description + "\n" + entry.Section)
		if strings.Contains(hay, q) {
			out = append(out, entry)
		}
	}
	return out, nil
}

// RefreshMcpRegistry forces a re-fetch even when the cache is still
// fresh. Useful when the registry list looks stale to the user.
func (a *App) RefreshMcpRegistry() ([]McpRegistryEntry, error) {
	cache, err := a.fetchMcpRegistry(context.Background())
	if err != nil {
		return nil, err
	}
	if err := writeMcpRegistryCache(cache); err != nil {
		return cache.Entries, fmt.Errorf("registry refreshed but cache write failed: %w", err)
	}
	return cache.Entries, nil
}

// InstallMcpFromRegistry resolves a registry slug to an McpServerConfig
// and persists it. Reference entries install deterministically; third-
// party entries are rejected with a helpful error pointing the user
// at their source repo.
//
// AutoStart=true (the common path: user just clicked "Install") spawns
// the subprocess immediately. Set false when programmatically
// pre-registering servers and you want to start them later (for tests
// that don't initialise the supervisor, or for "save without launch"
// flows once we add one).
func (a *App) InstallMcpFromRegistry(slug string, autoStart bool) (McpServerView, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return McpServerView{}, fmt.Errorf("registry slug is required")
	}
	cache, err := a.loadOrFetchMcpRegistry()
	if err != nil {
		return McpServerView{}, err
	}
	var entry *McpRegistryEntry
	for i := range cache.Entries {
		if cache.Entries[i].Slug == slug {
			entry = &cache.Entries[i]
			break
		}
	}
	if entry == nil {
		return McpServerView{}, fmt.Errorf("registry entry %q not found", slug)
	}
	if !entry.Installable {
		return McpServerView{}, fmt.Errorf("registry entry %q is third-party; install it manually using --source manual after consulting %s", slug, entry.SourceURL)
	}
	id, err := mcpIDFromDiscovery("registry", entry.Slug)
	if err != nil {
		return McpServerView{}, err
	}
	input := ManualServerInput{
		ID:          id,
		Name:        entry.Name,
		Description: entry.Description,
		Transport:   mcpTransportStdio,
		Command:     entry.Command,
		Args:        append([]string(nil), entry.Args...),
		AutoStart:   autoStart,
	}
	view, err := a.AddMcpServerManual(input)
	if err != nil {
		return view, err
	}
	var stamped McpServerConfig
	if err := withMcpHubState(func(s *mcpHubState) error {
		cfg, ok := s.Servers[view.ID]
		if !ok {
			return nil
		}
		cfg.Source = mcpSourceGitHub
		cfg.SourceURL = entry.SourceURL
		s.Servers[view.ID] = cfg
		stamped = cfg
		return nil
	}); err != nil {
		return view, fmt.Errorf("installed but failed to record provenance: %w", err)
	}
	if stamped.ID == "" {
		return view, nil
	}
	return a.viewForServer(stamped), nil
}

// loadOrFetchMcpRegistry returns the cached catalog if fresh, otherwise
// refetches from all sources.
func (a *App) loadOrFetchMcpRegistry() (mcpRegistryCache, error) {
	if cache, ok := readMcpRegistryCache(); ok {
		if time.Since(cache.FetchedAt) < mcpRegistryTTL && len(cache.Entries) > 0 {
			return cache, nil
		}
	}
	mcpRegistryFetchMu.Lock()
	defer mcpRegistryFetchMu.Unlock()
	if cache, ok := readMcpRegistryCache(); ok {
		if time.Since(cache.FetchedAt) < mcpRegistryTTL && len(cache.Entries) > 0 {
			return cache, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), mcpRegistryFetchTO)
	defer cancel()
	cache, err := a.fetchMcpRegistry(ctx)
	if err != nil {
		if stale, ok := readMcpRegistryCache(); ok && len(stale.Entries) > 0 {
			return stale, nil
		}
		return mcpRegistryCache{}, err
	}
	if err := writeMcpRegistryCache(cache); err != nil {
		return cache, nil
	}
	return cache, nil
}

// fetchMcpRegistry pulls both READMEs in parallel and merges the
// parsed entries. A failure in one source still yields whatever the
// other source produced — covering the official catalog when the
// community list is briefly unreachable, and vice-versa.
func (a *App) fetchMcpRegistry(ctx context.Context) (mcpRegistryCache, error) {
	type result struct {
		origin  string
		entries []McpRegistryEntry
		err     error
	}
	sources := []struct {
		origin string
		url    string
		parse  func(string, string) []McpRegistryEntry
	}{
		{"modelcontextprotocol/servers", mcpRegistrySourceModelcontextprotocol, parseModelcontextprotocolReadme},
		{"punkpeye/awesome-mcp-servers", mcpRegistrySourcePunkpeye, parsePunkpeyeReadme},
	}
	results := make([]result, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func(i int, origin, u string, parse func(string, string) []McpRegistryEntry) {
			defer wg.Done()
			body, err := fetchMcpRegistryBody(ctx, u)
			if err != nil {
				results[i] = result{origin: origin, err: err}
				return
			}
			results[i] = result{origin: origin, entries: parse(body, origin)}
		}(i, src.origin, src.url, src.parse)
	}
	wg.Wait()

	cache := mcpRegistryCache{
		FetchedAt: time.Now(),
		Sources:   make([]string, 0, len(sources)),
	}
	seenSlug := map[string]bool{}
	var firstErr error
	for _, r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		cache.Sources = append(cache.Sources, r.origin)
		for _, e := range r.entries {
			if e.Slug == "" || seenSlug[e.Slug] {
				continue
			}
			seenSlug[e.Slug] = true
			cache.Entries = append(cache.Entries, e)
		}
	}
	if len(cache.Entries) == 0 && firstErr != nil {
		return mcpRegistryCache{}, firstErr
	}
	sort.Slice(cache.Entries, func(i, j int) bool {
		// Reference entries first (one-click installable), then
		// alphabetical within each kind.
		if cache.Entries[i].Kind != cache.Entries[j].Kind {
			return cache.Entries[i].Kind == mcpRegistryKindReference
		}
		return strings.ToLower(cache.Entries[i].Name) < strings.ToLower(cache.Entries[j].Name)
	})
	return cache, nil
}

//isoform:destination metadata
func fetchMcpRegistryBody(ctx context.Context, source string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Yansu-MCP-Hub")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned %s", source, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, mcpRegistryMaxBytes))
	if err != nil {
		return "", fmt.Errorf("read %s body: %w", source, err)
	}
	return string(body), nil
}

// parseModelcontextprotocolReadme pulls only the Reference Servers
// section out of the official README. Third-party entries from this
// README are intentionally dropped — punkpeye's catalog covers the
// same ground with much wider coverage.
func parseModelcontextprotocolReadme(content string, origin string) []McpRegistryEntry {
	walker := newRegistryWalker(content)
	var entries []McpRegistryEntry
	for walker.next() {
		h2, _ := walker.section()
		if !sectionMatchesReference(h2) {
			continue
		}
		ref := referenceServerFromLink(walker.link, walker.name)
		if ref.slug == "" {
			continue
		}
		entries = append(entries, McpRegistryEntry{
			Slug:        ref.slug,
			Name:        walker.name,
			Description: cleanupRegistryDescription(walker.descRaw),
			SourceURL:   resolveRegistryLink(walker.link),
			Section:     cleanupRegistryHeading(h2),
			Origin:      origin,
			Kind:        mcpRegistryKindReference,
			NpmPackage:  ref.npmPackage,
			Command:     "npx",
			Args:        []string{"-y", ref.npmPackage},
			Installable: true,
		})
	}
	return entries
}

// parsePunkpeyeReadme walks bullets in punkpeye/awesome-mcp-servers and
// surfaces them as third-party entries, gated on:
//
//   - the H2 ancestor is not in the skip-list (Frameworks/Tutorials/etc.);
//   - the entry URL points at a known repo host (github/gitlab/bitbucket).
//
// Without those gates the README's "Tutorials" / "Community" /
// "Star History" sections leak into the registry as fake server rows.
func parsePunkpeyeReadme(content string, origin string) []McpRegistryEntry {
	walker := newRegistryWalker(content)
	var entries []McpRegistryEntry
	for walker.next() {
		h2, h3 := walker.section()
		cleanH2 := cleanupRegistryHeading(h2)
		if mcpRegistryPunkpeyeSkipH2[cleanH2] {
			continue
		}
		if !registryHostAllowed(walker.link) {
			continue
		}
		display := cleanupRegistryHeading(h3)
		if display == "" {
			display = cleanH2
		}
		slug := thirdPartySlug(walker.name, walker.link)
		if slug == "" {
			continue
		}
		entries = append(entries, McpRegistryEntry{
			Slug:        slug,
			Name:        walker.name,
			Description: cleanupRegistryDescription(walker.descRaw),
			SourceURL:   resolveRegistryLink(walker.link),
			Section:     display,
			Origin:      origin,
			Kind:        mcpRegistryKindThirdParty,
			Installable: false,
		})
	}
	return entries
}

// registryWalker enumerates README bullets while keeping a running
// H2/H3 context. We replace the previous "find heading by position"
// scan because it can't tell H2 from H3 — punkpeye nests H3 categories
// inside `## Server Implementations`, and a bullet in
// `### Aggregators` should know its H2 is `Server Implementations`,
// not just "Aggregators".
type registryWalker struct {
	lines     []string
	idx       int
	currentH2 string
	currentH3 string
	// captured fields for the current bullet
	name    string
	link    string
	descRaw string
}

func newRegistryWalker(content string) *registryWalker {
	return &registryWalker{lines: strings.Split(content, "\n")}
}

func (w *registryWalker) next() bool {
	for w.idx < len(w.lines) {
		line := w.lines[w.idx]
		w.idx++
		if m := mcpRegistryHeadingRegexp.FindStringSubmatch(line); m != nil {
			level := len(m[1])
			title := m[2]
			switch level {
			case 2:
				w.currentH2 = title
				w.currentH3 = ""
			case 3:
				w.currentH3 = title
			}
			continue
		}
		if m := mcpRegistryEntryRegexp.FindStringSubmatch(line); m != nil {
			w.name = strings.TrimSpace(m[1])
			w.link = strings.TrimSpace(m[2])
			w.descRaw = m[3]
			return true
		}
	}
	return false
}

func (w *registryWalker) section() (h2, h3 string) {
	return w.currentH2, w.currentH3
}

// cleanupRegistryHeading strips emoji prefixes, HTML anchor tags, and
// trims surrounding whitespace from a markdown heading. Returns the
// human-readable category title.
func cleanupRegistryHeading(s string) string {
	s = mcpRegistryHTMLAnchorRegexp.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	// Drop leading non-letter glyphs (emojis, decorations) so
	// "🔗 Aggregators" → "Aggregators". Stop on the first ASCII letter.
	for i, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			s = s[i:]
			break
		}
	}
	return strings.TrimSpace(s)
}

// cleanupRegistryDescription strips the trailing/leading badge images,
// emoji tag soup, and the conventional " - " separator that sits
// between an entry's badges and its actual prose description.
//
// Inputs we accept (one bullet line each, normalised by trimming):
//
//	"📇 ☁️ - A unified Model Context Protocol server..."
//	"[![alt](https://glama.ai/...)](https://...) 📇 ☁️ - A server that..."
//	"- Tools to read git history."        // already prose
//
// We strip badges, then split on the first " - ", then return the
// right side (preferring prose over decoration).
func cleanupRegistryDescription(raw string) string {
	s := mcpRegistryImageRegexp.ReplaceAllString(raw, "")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if idx := strings.Index(s, " - "); idx >= 0 {
		s = strings.TrimSpace(s[idx+3:])
	} else if idx := strings.Index(s, " — "); idx >= 0 {
		s = strings.TrimSpace(s[idx+5:])
	} else if idx := strings.Index(s, " – "); idx >= 0 {
		s = strings.TrimSpace(s[idx+5:])
	}
	if len(s) > 280 {
		s = strings.TrimSpace(s[:280]) + "..."
	}
	return s
}

// sectionMatchesReference returns true for any H2 heading that
// indicates "official reference servers". The README has used a few
// emoji + wording variants over its history.
func sectionMatchesReference(section string) bool {
	cleaned := strings.ToLower(cleanupRegistryHeading(section))
	return strings.Contains(cleaned, "reference")
}

// registryHostAllowed gates third-party entries on URL host so that
// links to articles, twitter handles, glama.ai score badges, and
// discussions threads don't masquerade as installable servers.
func registryHostAllowed(link string) bool {
	parsed, err := url.Parse(link)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Host)
	if _, ok := mcpRegistryThirdPartyHostAllowList[host]; !ok {
		return false
	}
	// Require an owner/repo shape: at least two non-empty path segments
	// after the host. github.com/orgs/<...>, github.com/discussions, and
	// other non-repo URLs fail this check.
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	if strings.EqualFold(parts[0], "orgs") || strings.EqualFold(parts[0], "topics") {
		return false
	}
	return true
}

type referenceServerInfo struct {
	slug       string
	npmPackage string
}

// referenceServerFromLink derives the npm package + slug for an entry
// in the modelcontextprotocol/servers Reference Servers section. The
// README links to either:
//
//	https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem
//
// or the relative form `src/filesystem`. We pick the trailing path
// component as the package suffix.
func referenceServerFromLink(link string, name string) referenceServerInfo {
	if matches := mcpRegistryRefSlugRegexp.FindStringSubmatch(link); len(matches) == 2 {
		slug := strings.TrimSpace(matches[1])
		if slug != "" {
			return referenceServerInfo{
				slug:       slug,
				npmPackage: "@modelcontextprotocol/server-" + slug,
			}
		}
	}
	slug := sanitiseMcpDiscoveryName(name)
	if slug == "" {
		return referenceServerInfo{}
	}
	return referenceServerInfo{
		slug:       slug,
		npmPackage: "@modelcontextprotocol/server-" + slug,
	}
}

// thirdPartySlug builds a stable identifier for a community server.
// We prefer "tp-<owner>-<repo>" derived from the GitHub URL because
// owner+repo is the canonical identity — names in the bullet are
// stylistic and change between README revisions.
func thirdPartySlug(name string, link string) string {
	if u, err := url.Parse(link); err == nil && u.Host != "" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			owner := sanitiseMcpDiscoveryName(parts[0])
			repo := sanitiseMcpDiscoveryName(parts[1])
			if owner != "" && repo != "" {
				return "tp-" + owner + "-" + repo
			}
		}
	}
	if san := sanitiseMcpDiscoveryName(name); san != "" {
		return "tp-" + san
	}
	return ""
}

func resolveRegistryLink(link string) string {
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return link
	}
	return "https://github.com/modelcontextprotocol/servers/tree/main/" + strings.TrimLeft(link, "./")
}

func mcpRegistryCachePath() (string, error) {
	base, err := mcpHubBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, mcpRegistryCacheFile), nil
}

func writeMcpRegistryCache(cache mcpRegistryCache) error {
	path, err := mcpRegistryCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func readMcpRegistryCache() (mcpRegistryCache, bool) {
	path, err := mcpRegistryCachePath()
	if err != nil {
		return mcpRegistryCache{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return mcpRegistryCache{}, false
	}
	var cache mcpRegistryCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return mcpRegistryCache{}, false
	}
	return cache, true
}
