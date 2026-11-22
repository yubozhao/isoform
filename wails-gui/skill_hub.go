package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"sync/atomic"
	"time"
	"unicode"

	"github.com/skratchdot/open-golang/open"
)

// ErrSkillNotInstalled is returned by SkillHub mutation paths when the caller
// references a skill ID that isn't in state. Handlers map this to 404 instead
// of 500.
var ErrSkillNotInstalled = errors.New("skill not installed")

// skillHubStateMu serialises load → modify → save sequences against the
// state file. Wails dispatches each RPC handler on its own goroutine, so
// concurrent toggles / installs would otherwise race and clobber each
// other's writes via the read-modify-write cycle.
var skillHubStateMu sync.Mutex

// saveTmpSeq disambiguates the temp file used by saveSkillHubState across
// callers; combined with the mutex above, this guarantees every successful
// rename promotes a fully-written file even if a previous save aborted.
var saveTmpSeq atomic.Uint64

// withSkillHubState atomically loads the state, runs fn against it, and
// saves the result back. All mutators in this package go through this
// helper so the state file can never lose updates under concurrency.
func withSkillHubState(fn func(*skillHubState) error) error {
	skillHubStateMu.Lock()
	defer skillHubStateMu.Unlock()
	state, err := loadSkillHubState()
	if err != nil {
		return err
	}
	if err := fn(state); err != nil {
		return err
	}
	return saveSkillHubState(state)
}

// skillHubInstallLocks gives each slug its own mutex so that the
// "replaceDir → writeOrigin → state mutation" sequence runs serially per
// destination. Without it, two concurrent installs of the same slug could
// interleave — A writes its files, B overwrites them, B writes state, A
// then writes state — leaving the on-disk skill (B) and the state record
// (A) describing different content.
//
// Different slugs still install in parallel; the per-slug mutex is created
// lazily and never deleted (the set of slugs the user actually installs is
// small and bounded by skill hub disk usage).
var (
	skillHubInstallLocks   = make(map[string]*sync.Mutex)
	skillHubInstallLocksMu sync.Mutex
)

func acquireSkillHubInstallLock(slug string) func() {
	skillHubInstallLocksMu.Lock()
	mu, ok := skillHubInstallLocks[slug]
	if !ok {
		mu = &sync.Mutex{}
		skillHubInstallLocks[slug] = mu
	}
	skillHubInstallLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

const (
	skillHubStateVersion = 1
	clawHubRegistryURL   = "https://clawhub.ai"

	skillHubDomainChat       = "chat"
	skillHubDomainProject    = "project"
	skillHubDomainAutomation = "automation"

	maxClawHubSkillZipBytes = 64 << 20
	// maxSkillContextBytes caps the total bytes injected as the always-on
	// metadata index. Bodies are NOT eagerly injected — the LLM Reads
	// each skill's SKILL.md on demand once a description matches the task
	// (Anthropic's progressive-disclosure model). Keep this small; the
	// index is one bullet per enabled skill.
	maxSkillContextBytes = 8 << 10
	// maxSkillDescriptionBytes truncates a single skill's description in
	// the metadata index so a runaway summary can't blow the index cap.
	maxSkillDescriptionBytes = 600
)

const skillHubSourceAgentDirs = "agent-dirs"

var (
	clawHubSlugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	skillHubDomains    = []string{skillHubDomainChat, skillHubDomainProject, skillHubDomainAutomation}
)

type SkillHubCatalogSkill struct {
	ID             string `json:"id"`
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Author         string `json:"author,omitempty"`
	Version        string `json:"version,omitempty"`
	SourceURL      string `json:"source_url"`
	Downloads      int    `json:"downloads,omitempty"`
	Stars          int    `json:"stars,omitempty"`
	UpdatedAt      int64  `json:"updated_at,omitempty"`
	SecurityStatus string `json:"security_status,omitempty"`
}

type SkillHubInstalledSkill struct {
	ID             string          `json:"id"`
	Slug           string          `json:"slug"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Source         string          `json:"source"`
	SourceURL      string          `json:"source_url"`
	Version        string          `json:"version"`
	Path           string          `json:"path"`
	Enabled        bool            `json:"enabled"`
	Domains        map[string]bool `json:"domains"`
	InstalledAt    int64           `json:"installed_at"`
	UpdatedAt      int64           `json:"updated_at"`
	SecurityStatus string          `json:"security_status,omitempty"`
}

type SkillHubDiscoveredSkill struct {
	ID          string `json:"id"`
	AgentID     string `json:"agent_id"`
	AgentName   string `json:"agent_name"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	SourceURL   string `json:"source_url"`
	Installed   bool   `json:"installed"`
	InstalledID string `json:"installed_id,omitempty"`
}

type skillHubState struct {
	Version int                               `json:"version"`
	Skills  map[string]SkillHubInstalledSkill `json:"skills"`
}

type clawHubSearchResponse struct {
	Results []struct {
		Slug        string  `json:"slug"`
		DisplayName *string `json:"displayName"`
		Summary     *string `json:"summary"`
		Version     *string `json:"version"`
		Score       float64 `json:"score"`
		UpdatedAt   int64   `json:"updatedAt"`
	} `json:"results"`
}

type clawHubSkillListResponse struct {
	Items []clawHubSkillListItem `json:"items"`
}

type clawHubSkillListItem struct {
	Slug        string  `json:"slug"`
	DisplayName string  `json:"displayName"`
	Summary     *string `json:"summary"`
	Stats       struct {
		Downloads int `json:"downloads"`
		Stars     int `json:"stars"`
	} `json:"stats"`
	UpdatedAt     int64 `json:"updatedAt"`
	LatestVersion *struct {
		Version string `json:"version"`
	} `json:"latestVersion"`
}

type clawHubSkillResponse struct {
	Skill *struct {
		Slug        string  `json:"slug"`
		DisplayName string  `json:"displayName"`
		Summary     *string `json:"summary"`
		Stats       struct {
			Downloads int `json:"downloads"`
			Stars     int `json:"stars"`
		} `json:"stats"`
		UpdatedAt int64 `json:"updatedAt"`
	} `json:"skill"`
	LatestVersion *struct {
		Version string `json:"version"`
	} `json:"latestVersion"`
	Owner *struct {
		Handle      string `json:"handle"`
		DisplayName string `json:"displayName"`
	} `json:"owner"`
	Moderation *struct {
		IsSuspicious     bool     `json:"isSuspicious"`
		IsMalwareBlocked bool     `json:"isMalwareBlocked"`
		Verdict          string   `json:"verdict"`
		ReasonCodes      []string `json:"reasonCodes"`
		Summary          string   `json:"summary"`
	} `json:"moderation"`
}

type clawHubOrigin struct {
	Version          int    `json:"version"`
	Registry         string `json:"registry"`
	Slug             string `json:"slug"`
	InstalledVersion string `json:"installedVersion"`
	InstalledAt      int64  `json:"installedAt"`
}

func (a *App) ListSkillHubInstalledSkills() ([]SkillHubInstalledSkill, error) {
	state, err := loadSkillHubState()
	if err != nil {
		return nil, err
	}
	skills := make([]SkillHubInstalledSkill, 0, len(state.Skills))
	for _, skill := range state.Skills {
		skills = append(skills, normalizeInstalledSkill(skill))
	}
	sort.Slice(skills, func(i, j int) bool {
		return strings.ToLower(skills[i].Name) < strings.ToLower(skills[j].Name)
	})
	return skills, nil
}

func (a *App) SearchClawHubSkills(query string) ([]SkillHubCatalogSkill, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	query = strings.TrimSpace(query)
	if query == "" {
		return listPopularClawHubSkills(ctx)
	}

	if slug, ok := clawHubSlugFromRef(query); ok && strings.Contains(query, "://") {
		detail, err := fetchClawHubSkillDetail(ctx, slug)
		if err != nil {
			return nil, err
		}
		return []SkillHubCatalogSkill{catalogSkillFromDetail(detail)}, nil
	}

	values := url.Values{}
	values.Set("q", query)
	values.Set("limit", "12")
	values.Set("nonSuspiciousOnly", "true")

	var response clawHubSearchResponse
	if err := clawHubGetJSON(ctx, "/api/v1/search", values, &response); err != nil {
		return nil, err
	}

	results := make([]SkillHubCatalogSkill, 0, len(response.Results))
	for _, item := range response.Results {
		if item.Slug == "" {
			continue
		}
		name := stringValue(item.DisplayName)
		if name == "" {
			name = item.Slug
		}
		results = append(results, SkillHubCatalogSkill{
			ID:          item.Slug,
			Slug:        item.Slug,
			Name:        name,
			Description: stringValue(item.Summary),
			Version:     stringValue(item.Version),
			SourceURL:   clawHubSourceURL("", item.Slug),
			UpdatedAt:   item.UpdatedAt,
		})
	}
	return results, nil
}

func (a *App) InstallClawHubSkill(ref string) (*SkillHubInstalledSkill, error) {
	slug, ok := clawHubSlugFromRef(ref)
	if !ok {
		return nil, fmt.Errorf("enter a ClawHub skill slug or URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	detail, err := fetchClawHubSkillDetail(ctx, slug)
	if err != nil {
		return nil, err
	}
	if detail.Skill == nil {
		return nil, fmt.Errorf("ClawHub skill not found: %s", slug)
	}
	if detail.Moderation != nil {
		if detail.Moderation.IsMalwareBlocked || detail.Moderation.Verdict == "malicious" {
			return nil, fmt.Errorf("ClawHub blocked %q as malicious", slug)
		}
		if detail.Moderation.IsSuspicious || detail.Moderation.Verdict == "suspicious" {
			return nil, fmt.Errorf("ClawHub flagged %q as suspicious; review it on ClawHub before installing", slug)
		}
	}

	canonicalSlug := detail.Skill.Slug
	if canonicalSlug == "" {
		canonicalSlug = slug
	}
	version := ""
	if detail.LatestVersion != nil {
		version = detail.LatestVersion.Version
	}
	if version == "" {
		return nil, fmt.Errorf("ClawHub skill %q has no installable version", canonicalSlug)
	}

	targetDir, err := skillHubSkillDir(canonicalSlug)
	if err != nil {
		return nil, err
	}
	tempDir, err := os.MkdirTemp("", "yansu-skillhub-*")
	if err != nil {
		return nil, fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	zipPath := filepath.Join(tempDir, canonicalSlug+".zip")
	if err := downloadClawHubSkillZip(ctx, canonicalSlug, version, zipPath); err != nil {
		return nil, err
	}

	extractDir := filepath.Join(tempDir, "extract")
	if err := extractZipArchive(zipPath, extractDir); err != nil {
		return nil, fmt.Errorf("extract ClawHub skill: %w", err)
	}
	sourceRoot, err := resolveExtractedSkillRoot(extractDir)
	if err != nil {
		return nil, err
	}

	// Per-slug install lock: serialise the file-replace + origin-write +
	// state-write triple so a concurrent install of the same slug can't
	// leave on-disk content describing one version while state.json
	// describes another.
	releaseInstallLock := acquireSkillHubInstallLock(canonicalSlug)
	defer releaseInstallLock()

	if err := replaceDir(sourceRoot, targetDir); err != nil {
		return nil, fmt.Errorf("install skill files: %w", err)
	}

	now := time.Now().UnixMilli()
	if err := writeClawHubOrigin(targetDir, clawHubOrigin{
		Version:          1,
		Registry:         clawHubRegistry(),
		Slug:             canonicalSlug,
		InstalledVersion: version,
		InstalledAt:      now,
	}); err != nil {
		return nil, err
	}

	var skill SkillHubInstalledSkill
	if err := withSkillHubState(func(state *skillHubState) error {
		existing := state.Skills[canonicalSlug]
		installedAt := existing.InstalledAt
		if installedAt == 0 {
			installedAt = now
		}
		owner := ""
		if detail.Owner != nil {
			owner = detail.Owner.Handle
		}
		name := detail.Skill.DisplayName
		if name == "" {
			name = canonicalSlug
		}
		skill = SkillHubInstalledSkill{
			ID:             canonicalSlug,
			Slug:           canonicalSlug,
			Name:           name,
			Description:    stringValue(detail.Skill.Summary),
			Source:         "clawhub",
			SourceURL:      clawHubSourceURL(owner, canonicalSlug),
			Version:        version,
			Path:           targetDir,
			Enabled:        true,
			Domains:        defaultSkillHubDomains(),
			InstalledAt:    installedAt,
			UpdatedAt:      now,
			SecurityStatus: moderationStatus(detail.Moderation),
		}
		if existing.Domains != nil {
			skill.Domains = normalizeSkillHubDomains(existing.Domains)
		}
		state.Skills[canonicalSlug] = skill
		return nil
	}); err != nil {
		return nil, err
	}
	return &skill, nil
}

func (a *App) SetSkillHubSkillEnabled(id string, enabled bool) (*SkillHubInstalledSkill, error) {
	var skill SkillHubInstalledSkill
	if err := withSkillHubState(func(state *skillHubState) error {
		existing, ok := state.Skills[id]
		if !ok {
			return fmt.Errorf("%w: %s", ErrSkillNotInstalled, id)
		}
		existing.Enabled = enabled
		existing.UpdatedAt = time.Now().UnixMilli()
		existing = normalizeInstalledSkill(existing)
		state.Skills[id] = existing
		skill = existing
		return nil
	}); err != nil {
		return nil, err
	}
	return &skill, nil
}

func (a *App) SetSkillHubSkillDomains(id string, domains map[string]bool) (*SkillHubInstalledSkill, error) {
	var skill SkillHubInstalledSkill
	if err := withSkillHubState(func(state *skillHubState) error {
		existing, ok := state.Skills[id]
		if !ok {
			return fmt.Errorf("%w: %s", ErrSkillNotInstalled, id)
		}
		existing.Domains = normalizeSkillHubDomains(domains)
		existing.UpdatedAt = time.Now().UnixMilli()
		existing = normalizeInstalledSkill(existing)
		state.Skills[id] = existing
		skill = existing
		return nil
	}); err != nil {
		return nil, err
	}
	return &skill, nil
}

func (a *App) PatchSkillHubSkill(id string, enabled *bool, domainPatch map[string]bool) (*SkillHubInstalledSkill, error) {
	var skill SkillHubInstalledSkill
	if err := withSkillHubState(func(state *skillHubState) error {
		existing, ok := state.Skills[id]
		if !ok {
			return fmt.Errorf("%w: %s", ErrSkillNotInstalled, id)
		}
		if enabled == nil && domainPatch == nil {
			return fmt.Errorf("no skill fields to update")
		}
		existing = normalizeInstalledSkill(existing)
		if enabled != nil {
			existing.Enabled = *enabled
		}
		if domainPatch != nil {
			domains := normalizeSkillHubDomains(existing.Domains)
			for domain, value := range domainPatch {
				domains[domain] = value
			}
			existing.Domains = normalizeSkillHubDomains(domains)
		}
		existing.UpdatedAt = time.Now().UnixMilli()
		existing = normalizeInstalledSkill(existing)
		state.Skills[id] = existing
		skill = existing
		return nil
	}); err != nil {
		return nil, err
	}
	return &skill, nil
}

func (a *App) UninstallSkillHubSkill(id string) error {
	return withSkillHubState(func(state *skillHubState) error {
		skill, ok := state.Skills[id]
		if !ok {
			return fmt.Errorf("%w: %s", ErrSkillNotInstalled, id)
		}
		if skill.Path != "" {
			if err := os.RemoveAll(skill.Path); err != nil {
				return fmt.Errorf("remove skill directory: %w", err)
			}
		}
		delete(state.Skills, id)
		return nil
	})
}

func (a *App) OpenSkillHubSkillFolder(id string) error {
	state, err := loadSkillHubState()
	if err != nil {
		return err
	}
	skill, ok := state.Skills[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrSkillNotInstalled, id)
	}
	if skill.Path == "" {
		return fmt.Errorf("skill has no install path: %s", id)
	}
	if _, err := os.Stat(skill.Path); err != nil {
		return err
	}
	return open.Start(skill.Path)
}

func (a *App) injectSkillContext(prompt string, domain string) string {
	context := a.skillHubContext(domain)
	if context == "" {
		return prompt
	}
	return context + prompt
}

// skillHubContext renders the always-on metadata index for installed
// skills. Each enabled skill contributes one bullet (name, description,
// SKILL.md path) — no body. The LLM follows Anthropic's
// progressive-disclosure model: it reads SKILL.md via the Read tool only
// when a description matches the user's task, then chases sub-files
// (e.g. WEB_APPS.md) referenced inside that body. Eagerly injecting
// bodies wastes context and double-loads the same content the LLM would
// have Read on demand anyway.
func (a *App) skillHubContext(domain string) string {
	return a.skillHubContextMatching(domain, nil)
}

// namedAutomationSkillContext preserves discovery for a task that names an
// enabled automation skill, without advertising the unrelated standing catalog.
// Match complete name/slug/id words, not descriptions or semantic guesses.
func (a *App) namedAutomationSkillContext(task string) string {
	words := func(text string) string {
		return " " + strings.ToLower(strings.Join(strings.FieldsFunc(text, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}), " ")) + " "
	}
	taskWords := words(task)
	return a.skillHubContextMatching(skillHubDomainAutomation, func(skill SkillHubInstalledSkill) bool {
		for _, name := range []string{skill.Name, skill.Slug, skill.ID} {
			named := words(name)
			if strings.TrimSpace(named) != "" && strings.Contains(taskWords, named) {
				return true
			}
		}
		return false
	})
}

func (a *App) skillHubContextMatching(domain string, include func(SkillHubInstalledSkill) bool) string {
	domain = normalizeSkillHubDomain(domain)
	state, err := loadSkillHubState()
	if err != nil || len(state.Skills) == 0 {
		return ""
	}

	skills := make([]SkillHubInstalledSkill, 0, len(state.Skills))
	for _, skill := range state.Skills {
		skill = normalizeInstalledSkill(skill)
		if !skill.Enabled || !skill.Domains[domain] || include != nil && !include(skill) {
			continue
		}
		skills = append(skills, skill)
	}
	if len(skills) == 0 {
		return ""
	}
	sort.Slice(skills, func(i, j int) bool {
		return strings.ToLower(skills[i].Name) < strings.ToLower(skills[j].Name)
	})

	var b strings.Builder
	b.WriteString("<user-installed-skills source=\"Isoform Skill Hub\" domain=\"")
	b.WriteString(domain)
	b.WriteString("\">\n")
	b.WriteString("The following user-installed skills are available. Each entry lists the skill's name, a short description, and the absolute path to its SKILL.md. When a description matches the user's task, Read the SKILL.md to load the full instructions; that file may reference further sub-files (e.g. WEB_APPS.md, RECORDING.md) — Read those too as needed. Treat the SKILL.md's directory as the skill's baseDir for any relative paths it mentions.\n\n")

	written := b.Len()
	emitted := 0
	for _, skill := range skills {
		skillPath := filepath.Join(skill.Path, "SKILL.md")
		// Refuse to dereference a symlink — a malicious local skill could
		// otherwise point SKILL.md at ~/.ssh/id_rsa or similar. We don't
		// read the file under metadata-only mode, but we still gate on
		// "regular file exists" so we don't advertise a broken skill.
		info, err := os.Lstat(skillPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		desc := strings.TrimSpace(skill.Description)
		if desc == "" {
			// Fall back to scraping the description from SKILL.md
			// frontmatter — handles old state.json entries that pre-date
			// the Description field being populated.
			if data, err := os.ReadFile(skillPath); err == nil {
				desc = skillSummaryFromBody(data)
			}
		}
		desc = collapseWhitespace(desc)
		if len(desc) > maxSkillDescriptionBytes {
			desc = desc[:maxSkillDescriptionBytes] + "..."
		}
		chunk := fmt.Sprintf("- name: %s\n  description: %s\n  skill_md: %s\n\n", skill.Name, desc, skillPath)
		if written+len(chunk) > maxSkillContextBytes {
			break
		}
		b.WriteString(chunk)
		written += len(chunk)
		emitted++
	}
	if emitted == 0 {
		return ""
	}
	b.WriteString("</user-installed-skills>\n\n")
	return b.String()
}

// collapseWhitespace squashes any run of whitespace (including newlines)
// into a single space so a multi-line description doesn't break the
// one-bullet-per-skill layout of the metadata index.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func listPopularClawHubSkills(ctx context.Context) ([]SkillHubCatalogSkill, error) {
	values := url.Values{}
	values.Set("limit", "12")
	values.Set("sort", "downloads")
	values.Set("nonSuspiciousOnly", "true")

	var response clawHubSkillListResponse
	if err := clawHubGetJSON(ctx, "/api/v1/skills", values, &response); err != nil {
		return nil, err
	}
	results := make([]SkillHubCatalogSkill, 0, len(response.Items))
	for _, item := range response.Items {
		version := ""
		if item.LatestVersion != nil {
			version = item.LatestVersion.Version
		}
		name := item.DisplayName
		if name == "" {
			name = item.Slug
		}
		results = append(results, SkillHubCatalogSkill{
			ID:          item.Slug,
			Slug:        item.Slug,
			Name:        name,
			Description: stringValue(item.Summary),
			Version:     version,
			SourceURL:   clawHubSourceURL("", item.Slug),
			Downloads:   item.Stats.Downloads,
			Stars:       item.Stats.Stars,
			UpdatedAt:   item.UpdatedAt,
		})
	}
	return results, nil
}

func fetchClawHubSkillDetail(ctx context.Context, slug string) (*clawHubSkillResponse, error) {
	if !clawHubSlugPattern.MatchString(slug) {
		return nil, fmt.Errorf("invalid ClawHub skill slug: %s", slug)
	}
	var response clawHubSkillResponse
	path := "/api/v1/skills/" + url.PathEscape(slug)
	if err := clawHubGetJSON(ctx, path, nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func catalogSkillFromDetail(detail *clawHubSkillResponse) SkillHubCatalogSkill {
	if detail == nil || detail.Skill == nil {
		return SkillHubCatalogSkill{}
	}
	owner := ""
	if detail.Owner != nil {
		owner = detail.Owner.Handle
	}
	version := ""
	if detail.LatestVersion != nil {
		version = detail.LatestVersion.Version
	}
	name := detail.Skill.DisplayName
	if name == "" {
		name = detail.Skill.Slug
	}
	return SkillHubCatalogSkill{
		ID:             detail.Skill.Slug,
		Slug:           detail.Skill.Slug,
		Name:           name,
		Description:    stringValue(detail.Skill.Summary),
		Author:         owner,
		Version:        version,
		SourceURL:      clawHubSourceURL(owner, detail.Skill.Slug),
		Downloads:      detail.Skill.Stats.Downloads,
		Stars:          detail.Skill.Stats.Stars,
		UpdatedAt:      detail.Skill.UpdatedAt,
		SecurityStatus: moderationStatus(detail.Moderation),
	}
}

//isoform:destination metadata
func clawHubGetJSON(ctx context.Context, path string, values url.Values, out any) error {
	reqURL := strings.TrimRight(clawHubRegistry(), "/") + path
	if len(values) > 0 {
		reqURL += "?" + values.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("build ClawHub request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Yansu-Desktop/"+Version)

	resp, err := httpClientWithTimeout.Do(req)
	if err != nil {
		return fmt.Errorf("ClawHub request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if resp.StatusCode == http.StatusTooManyRequests {
			retry := strings.TrimSpace(resp.Header.Get("Retry-After"))
			if retry != "" {
				return fmt.Errorf("ClawHub rate limited this request; retry after %ss", retry)
			}
			return fmt.Errorf("ClawHub rate limited this request")
		}
		return fmt.Errorf("ClawHub returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode ClawHub response: %w", err)
	}
	return nil
}

//isoform:destination metadata
func downloadClawHubSkillZip(ctx context.Context, slug, version, destPath string) error {
	values := url.Values{}
	values.Set("slug", slug)
	values.Set("version", version)
	reqURL := strings.TrimRight(clawHubRegistry(), "/") + "/api/v1/download?" + values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("build ClawHub download request: %w", err)
	}
	req.Header.Set("User-Agent", "Yansu-Desktop/"+Version)

	resp, err := httpClientWithTimeout.Do(req)
	if err != nil {
		return fmt.Errorf("download ClawHub skill: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if resp.StatusCode == http.StatusTooManyRequests {
			retry := strings.TrimSpace(resp.Header.Get("Retry-After"))
			if retry != "" {
				return fmt.Errorf("ClawHub download rate limited; retry after %ss", retry)
			}
			return fmt.Errorf("ClawHub download rate limited")
		}
		return fmt.Errorf("ClawHub download returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	file, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer file.Close()

	written, err := io.Copy(file, io.LimitReader(resp.Body, maxClawHubSkillZipBytes+1))
	if err != nil {
		return fmt.Errorf("write ClawHub skill archive: %w", err)
	}
	if written > maxClawHubSkillZipBytes {
		return fmt.Errorf("ClawHub skill archive is too large")
	}
	return nil
}

func resolveExtractedSkillRoot(extractDir string) (string, error) {
	if skillHubFileExists(filepath.Join(extractDir, "SKILL.md")) {
		return extractDir, nil
	}

	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return "", err
	}
	var childDirs []string
	hasTopLevelFiles := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.IsDir() {
			childDirs = append(childDirs, filepath.Join(extractDir, entry.Name()))
			continue
		}
		hasTopLevelFiles = true
	}
	if !hasTopLevelFiles && len(childDirs) == 1 && skillHubFileExists(filepath.Join(childDirs[0], "SKILL.md")) {
		return childDirs[0], nil
	}
	return "", fmt.Errorf("downloaded archive does not contain SKILL.md at its root")
}

func loadSkillHubState() (*skillHubState, error) {
	path, err := skillHubStatePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &skillHubState{Version: skillHubStateVersion, Skills: map[string]SkillHubInstalledSkill{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skill hub state: %w", err)
	}
	var state skillHubState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse skill hub state: %w", err)
	}
	if state.Version == 0 {
		state.Version = skillHubStateVersion
	}
	if state.Skills == nil {
		state.Skills = map[string]SkillHubInstalledSkill{}
	}
	for id, skill := range state.Skills {
		state.Skills[id] = normalizeInstalledSkill(skill)
	}
	return &state, nil
}

func saveSkillHubState(state *skillHubState) error {
	if state == nil {
		state = &skillHubState{}
	}
	state.Version = skillHubStateVersion
	if state.Skills == nil {
		state.Skills = map[string]SkillHubInstalledSkill{}
	}

	path, err := skillHubStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create skill hub directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal skill hub state: %w", err)
	}
	data = append(data, '\n')
	// Use a unique suffix so two callers (or a previous aborted run) can't
	// race on the same `.tmp` path; the rename below is atomic, so the worst
	// case is a leftover `.tmp.<seq>` file that we sweep on the next save.
	tmpPath := fmt.Sprintf("%s.tmp.%d.%d", path, time.Now().UnixNano(), saveTmpSeq.Add(1))
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write skill hub state: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace skill hub state: %w", err)
	}
	return nil
}

func normalizeInstalledSkill(skill SkillHubInstalledSkill) SkillHubInstalledSkill {
	if skill.ID == "" {
		skill.ID = skill.Slug
	}
	if skill.Slug == "" {
		skill.Slug = skill.ID
	}
	if skill.Name == "" {
		skill.Name = skill.Slug
	}
	if skill.Source == "" {
		skill.Source = "clawhub"
	}
	if skill.Domains == nil {
		skill.Domains = defaultSkillHubDomains()
	} else {
		skill.Domains = normalizeSkillHubDomains(skill.Domains)
	}
	if skill.Path == "" && skill.Slug != "" {
		if dir, err := skillHubSkillDir(skill.Slug); err == nil {
			skill.Path = dir
		}
	}
	// "unverified" was a synthetic label we used to write for non-ClawHub
	// sources; the source field already conveys the same information, so we
	// drop it on read to avoid showing a redundant warning in the UI.
	if skill.SecurityStatus == "unverified" {
		skill.SecurityStatus = ""
	}
	return skill
}

func defaultSkillHubDomains() map[string]bool {
	domains := make(map[string]bool, len(skillHubDomains))
	for _, domain := range skillHubDomains {
		domains[domain] = true
	}
	return domains
}

func normalizeSkillHubDomains(input map[string]bool) map[string]bool {
	out := defaultSkillHubDomains()
	for _, domain := range skillHubDomains {
		if enabled, ok := input[domain]; ok {
			out[domain] = enabled
		}
	}
	return out
}

func normalizeSkillHubDomain(domain string) string {
	switch domain {
	case skillHubDomainProject, skillHubDomainAutomation:
		return domain
	default:
		return skillHubDomainChat
	}
}

func skillHubBaseDir() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(dataRoot, "skill-hub"), nil
}

func skillHubStatePath() (string, error) {
	base, err := skillHubBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "state.json"), nil
}

func skillHubSkillDir(slug string) (string, error) {
	if !clawHubSlugPattern.MatchString(slug) {
		return "", fmt.Errorf("invalid skill slug: %s", slug)
	}
	base, err := skillHubBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "skills", slug), nil
}

func clawHubSlugFromRef(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	if parsed, err := url.Parse(ref); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		host := strings.ToLower(parsed.Hostname())
		if host != "clawhub.ai" && host != "www.clawhub.ai" {
			return "", false
		}
		if slug := parsed.Query().Get("slug"); slug != "" {
			return sanitizeClawHubSlug(slug)
		}
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		filtered := make([]string, 0, len(parts))
		for _, part := range parts {
			if part != "" {
				filtered = append(filtered, part)
			}
		}
		if len(filtered) == 0 {
			return "", false
		}
		if filtered[0] == "skills" && len(filtered) >= 2 {
			return sanitizeClawHubSlug(filtered[1])
		}
		return sanitizeClawHubSlug(filtered[len(filtered)-1])
	}
	fields := strings.Fields(ref)
	if len(fields) > 1 {
		ref = fields[len(fields)-1]
	}
	ref = strings.Trim(ref, `"'`)
	if strings.Contains(ref, "..") {
		return "", false
	}
	if strings.Contains(ref, "/") {
		parts := strings.Split(strings.Trim(ref, "/"), "/")
		ref = parts[len(parts)-1]
	}
	return sanitizeClawHubSlug(ref)
}

func sanitizeClawHubSlug(slug string) (string, bool) {
	slug = strings.TrimSpace(strings.Trim(slug, `"'`))
	if !clawHubSlugPattern.MatchString(slug) {
		return "", false
	}
	return slug, true
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func clawHubRegistry() string {
	if registry := strings.TrimSpace(os.Getenv("CLAWHUB_REGISTRY")); registry != "" {
		return strings.TrimRight(registry, "/")
	}
	return clawHubRegistryURL
}

func clawHubSourceURL(owner, slug string) string {
	if owner != "" {
		return clawHubRegistryURL + "/" + url.PathEscape(owner) + "/" + url.PathEscape(slug)
	}
	return clawHubRegistryURL + "/skills/" + url.PathEscape(slug)
}

func moderationStatus(moderation *struct {
	IsSuspicious     bool     `json:"isSuspicious"`
	IsMalwareBlocked bool     `json:"isMalwareBlocked"`
	Verdict          string   `json:"verdict"`
	ReasonCodes      []string `json:"reasonCodes"`
	Summary          string   `json:"summary"`
}) string {
	if moderation == nil {
		return "clean"
	}
	if moderation.IsMalwareBlocked || moderation.Verdict == "malicious" {
		return "malicious"
	}
	if moderation.IsSuspicious || moderation.Verdict == "suspicious" {
		return "suspicious"
	}
	if moderation.Verdict != "" {
		return moderation.Verdict
	}
	return "clean"
}

func writeClawHubOrigin(skillDir string, origin clawHubOrigin) error {
	data, err := json.MarshalIndent(origin, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(skillDir, ".clawhub", "origin.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func skillHubFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
