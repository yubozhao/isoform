package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	githubAPIBaseURL       = "https://api.github.com"
	githubArchiveBaseURL   = "https://codeload.github.com"
	maxGitHubSkillZipBytes = 64 << 20

	skillHubSourceGitHub = "github"
)

var (
	githubOwnerPattern  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	githubRepoPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	githubSubdirPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
)

// githubSkillRef captures the parsed coordinates of a GitHub-hosted skill.
type githubSkillRef struct {
	Owner  string
	Repo   string
	Ref    string // branch, tag, or commit SHA. Empty means resolve to default branch.
	Subdir string // optional path to a sub-directory inside the repo.
}

type githubOrigin struct {
	Version     int    `json:"version"`
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	Ref         string `json:"ref"`
	Subdir      string `json:"subdir,omitempty"`
	CommitSHA   string `json:"commitSha,omitempty"`
	InstalledAt int64  `json:"installedAt"`
}

type githubRepoResponse struct {
	DefaultBranch string `json:"default_branch"`
	Description   string `json:"description"`
	HTMLURL       string `json:"html_url"`
}

type githubCommitResponse struct {
	SHA string `json:"sha"`
}

// InstallSkillFromGitHub installs a skill from a public GitHub repository.
// It accepts shorthand (owner/repo[@ref][#subdir]) or full GitHub URLs and
// records the install with source="github" so the rest of the skill hub
// machinery (enable/disable, domains, prompt injection) keeps working.
func (a *App) InstallSkillFromGitHub(ref string) (*SkillHubInstalledSkill, error) {
	parsed, err := parseGitHubSkillRef(ref)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	repoInfo, err := fetchGitHubRepoInfo(ctx, parsed.Owner, parsed.Repo)
	if err != nil {
		return nil, err
	}
	gitRef := parsed.Ref
	if gitRef == "" {
		gitRef = repoInfo.DefaultBranch
	}
	if gitRef == "" {
		return nil, fmt.Errorf("cannot determine GitHub ref for %s/%s", parsed.Owner, parsed.Repo)
	}

	// Best-effort commit SHA — falling back to the branch/tag ref keeps
	// installs working when the user is unauthenticated and has hit the
	// 60/hour anonymous GitHub API ceiling, but log the failure so the
	// underlying cause (rate-limit, network, deleted ref) is debuggable
	// instead of silently producing an unpinned install.
	commitSHA, err := fetchGitHubCommitSHA(ctx, parsed.Owner, parsed.Repo, gitRef)
	if err != nil {
		log.Printf("[skill-hub][github] fetchGitHubCommitSHA(%s/%s@%s) failed, using ref as version: %v", parsed.Owner, parsed.Repo, gitRef, err)
	}

	slug, err := githubSkillSlug(parsed)
	if err != nil {
		return nil, err
	}

	targetDir, err := skillHubSkillDir(slug)
	if err != nil {
		return nil, err
	}
	tempDir, err := os.MkdirTemp("", "yansu-skillhub-gh-*")
	if err != nil {
		return nil, fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	zipPath := filepath.Join(tempDir, slug+".zip")
	if err := downloadGitHubSkillZip(ctx, parsed.Owner, parsed.Repo, gitRef, zipPath); err != nil {
		return nil, err
	}

	extractDir := filepath.Join(tempDir, "extract")
	if err := extractZipArchive(zipPath, extractDir); err != nil {
		return nil, fmt.Errorf("extract GitHub archive: %w", err)
	}

	repoRoot, err := singleTopLevelDir(extractDir)
	if err != nil {
		return nil, err
	}
	sourceRoot, err := resolveGitHubSkillRoot(repoRoot, parsed.Subdir)
	if err != nil {
		return nil, err
	}

	// Per-slug install lock: serialise the file-replace + origin-write +
	// state-write triple so two parallel installs of the same slug can't
	// leave on-disk content and state.json describing different versions.
	releaseInstallLock := acquireSkillHubInstallLock(slug)
	defer releaseInstallLock()

	if err := replaceDir(sourceRoot, targetDir); err != nil {
		return nil, fmt.Errorf("install skill files: %w", err)
	}

	now := time.Now().UnixMilli()
	if err := writeGitHubOrigin(targetDir, githubOrigin{
		Version:     1,
		Owner:       parsed.Owner,
		Repo:        parsed.Repo,
		Ref:         gitRef,
		Subdir:      parsed.Subdir,
		CommitSHA:   commitSHA,
		InstalledAt: now,
	}); err != nil {
		return nil, err
	}

	displayName := skillNameFromGitHub(parsed)
	description := strings.TrimSpace(repoInfo.Description)
	if parsed.Subdir != "" && description != "" {
		description = fmt.Sprintf("%s · %s", description, parsed.Subdir)
	}

	version := commitSHA
	if version == "" {
		version = gitRef
	} else if len(version) > 12 {
		version = version[:12]
	}

	var skill SkillHubInstalledSkill
	if err := withSkillHubState(func(state *skillHubState) error {
		existing := state.Skills[slug]
		installedAt := existing.InstalledAt
		if installedAt == 0 {
			installedAt = now
		}
		skill = SkillHubInstalledSkill{
			ID:          slug,
			Slug:        slug,
			Name:        displayName,
			Description: description,
			Source:      skillHubSourceGitHub,
			SourceURL:   githubSourceURL(parsed),
			Version:     version,
			Path:        targetDir,
			Enabled:     true,
			Domains:     defaultSkillHubDomains(),
			InstalledAt: installedAt,
			UpdatedAt:   now,
		}
		if existing.Domains != nil {
			skill.Domains = normalizeSkillHubDomains(existing.Domains)
		}
		state.Skills[slug] = skill
		return nil
	}); err != nil {
		return nil, err
	}
	return &skill, nil
}

// parseGitHubSkillRef accepts:
//   - owner/repo
//   - owner/repo@ref
//   - owner/repo#sub/dir
//   - owner/repo@ref#sub/dir
//   - https://github.com/owner/repo[/tree/ref[/sub/dir]]
//   - git@github.com:owner/repo.git (best-effort)
func parseGitHubSkillRef(input string) (githubSkillRef, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return githubSkillRef{}, fmt.Errorf("enter a GitHub repo URL or owner/repo")
	}

	// git@github.com:owner/repo.git -> rewrite to https form for parser reuse.
	if rest, ok := strings.CutPrefix(input, "git@github.com:"); ok {
		input = "https://github.com/" + rest
	}
	input = strings.TrimSuffix(input, ".git")

	if parsed, err := url.Parse(input); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		host := strings.ToLower(parsed.Hostname())
		if host != "github.com" && host != "www.github.com" {
			return githubSkillRef{}, fmt.Errorf("only github.com URLs are supported")
		}
		segs := splitNonEmpty(parsed.Path, "/")
		if len(segs) < 2 {
			return githubSkillRef{}, fmt.Errorf("URL must include owner and repo")
		}
		owner := segs[0]
		repo := strings.TrimSuffix(segs[1], ".git")

		ref := ""
		var subdirSegs []string
		if len(segs) >= 4 && (segs[2] == "tree" || segs[2] == "blob") {
			ref = segs[3]
			if len(segs) > 4 {
				subdirSegs = segs[4:]
			}
		}

		fragment := strings.TrimPrefix(parsed.Fragment, "path=")
		fragment = strings.Trim(fragment, "/")

		subdir := strings.Join(subdirSegs, "/")
		if fragment != "" {
			subdir = fragment
		}

		return buildGitHubSkillRef(owner, repo, ref, subdir)
	}

	// Shorthand form: owner/repo[@ref][#subdir]
	rest := input
	subdir := ""
	if idx := strings.Index(rest, "#"); idx >= 0 {
		subdir = strings.Trim(rest[idx+1:], "/")
		rest = rest[:idx]
	}
	ref := ""
	if idx := strings.Index(rest, "@"); idx >= 0 {
		ref = rest[idx+1:]
		rest = rest[:idx]
	}
	parts := splitNonEmpty(rest, "/")
	if len(parts) != 2 {
		return githubSkillRef{}, fmt.Errorf("expected owner/repo, got %q", input)
	}
	return buildGitHubSkillRef(parts[0], parts[1], ref, subdir)
}

func buildGitHubSkillRef(owner, repo, ref, subdir string) (githubSkillRef, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(strings.TrimSuffix(repo, ".git"))
	ref = strings.TrimSpace(ref)
	subdir = strings.Trim(strings.TrimSpace(subdir), "/")

	if !githubOwnerPattern.MatchString(owner) {
		return githubSkillRef{}, fmt.Errorf("invalid GitHub owner: %q", owner)
	}
	if !githubRepoPattern.MatchString(repo) {
		return githubSkillRef{}, fmt.Errorf("invalid GitHub repo: %q", repo)
	}
	if subdir != "" {
		if strings.Contains(subdir, "..") || strings.Contains(subdir, "\\") || !githubSubdirPattern.MatchString(subdir) {
			return githubSkillRef{}, fmt.Errorf("invalid subdirectory: %q", subdir)
		}
	}
	if ref != "" {
		// GitHub refs allow a wide character set, but reject anything path-y.
		if strings.ContainsAny(ref, " \t\n\r\\") || strings.Contains(ref, "..") {
			return githubSkillRef{}, fmt.Errorf("invalid ref: %q", ref)
		}
	}
	return githubSkillRef{Owner: owner, Repo: repo, Ref: ref, Subdir: subdir}, nil
}

// githubSkillSlug derives a deterministic slug for a GitHub-installed skill.
// Format: gh-<owner>-<repo>[-<flattened-subdir>] with collapsed dashes.
// If the resulting slug exceeds the slug pattern length, it is truncated and
// suffixed with a short hash to keep collisions improbable.
func githubSkillSlug(ref githubSkillRef) (string, error) {
	flatten := strings.NewReplacer("/", "-", " ", "-", "_", "-").Replace
	parts := []string{"gh", flatten(strings.ToLower(ref.Owner)), flatten(strings.ToLower(ref.Repo))}
	if ref.Subdir != "" {
		flattened := flatten(strings.ToLower(ref.Subdir))
		flattened = strings.TrimFunc(flattened, func(r rune) bool { return r == '.' || r == '-' })
		if flattened != "" {
			parts = append(parts, flattened)
		}
	}
	slug := strings.Join(parts, "-")
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-.")

	if len(slug) > 100 {
		sum := sha1.Sum([]byte(slug))
		slug = slug[:90] + "-" + hex.EncodeToString(sum[:4])
	}
	if !clawHubSlugPattern.MatchString(slug) {
		return "", fmt.Errorf("derived slug %q is not a valid skill identifier", slug)
	}
	return slug, nil
}

func skillNameFromGitHub(ref githubSkillRef) string {
	if ref.Subdir != "" {
		return fmt.Sprintf("%s/%s · %s", ref.Owner, ref.Repo, ref.Subdir)
	}
	return fmt.Sprintf("%s/%s", ref.Owner, ref.Repo)
}

func githubSourceURL(ref githubSkillRef) string {
	u := "https://github.com/" + url.PathEscape(ref.Owner) + "/" + url.PathEscape(ref.Repo)
	switch {
	case ref.Ref != "" && ref.Subdir != "":
		u += "/tree/" + url.PathEscape(ref.Ref) + "/" + ref.Subdir
	case ref.Ref != "":
		u += "/tree/" + url.PathEscape(ref.Ref)
	case ref.Subdir != "":
		u += "/tree/HEAD/" + ref.Subdir
	}
	return u
}

func resolveGitHubSkillRoot(repoRoot, subdir string) (string, error) {
	if subdir != "" {
		candidate := filepath.Join(repoRoot, filepath.FromSlash(subdir))
		rel, err := filepath.Rel(repoRoot, candidate)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("subdirectory escapes repo root: %s", subdir)
		}
		if !skillHubFileExists(filepath.Join(candidate, "SKILL.md")) {
			return "", fmt.Errorf("subdirectory %q does not contain SKILL.md", subdir)
		}
		return candidate, nil
	}
	if skillHubFileExists(filepath.Join(repoRoot, "SKILL.md")) {
		return repoRoot, nil
	}
	// Fall back to the same single-skill detection used for ClawHub archives.
	resolved, err := resolveExtractedSkillRoot(repoRoot)
	if err != nil {
		return "", fmt.Errorf("repo root has no SKILL.md (point at a sub-directory with #path)")
	}
	return resolved, nil
}

// singleTopLevelDir returns the lone top-level directory inside an extracted
// GitHub archive (codeload prepends `<owner>-<repo>-<sha>/`).
func singleTopLevelDir(extractDir string) (string, error) {
	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return "", err
	}
	var dirs []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(extractDir, name))
		}
	}
	if len(dirs) != 1 {
		return "", fmt.Errorf("expected single top-level directory in archive, got %d", len(dirs))
	}
	return dirs[0], nil
}

func fetchGitHubRepoInfo(ctx context.Context, owner, repo string) (*githubRepoResponse, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s",
		githubAPIBase(),
		url.PathEscape(owner),
		url.PathEscape(repo),
	)
	req, err := newGitHubAPIRequest(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	resp, err := httpClientWithTimeout.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub request failed: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var info githubRepoResponse
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return nil, fmt.Errorf("decode GitHub repo response: %w", err)
		}
		return &info, nil
	case http.StatusNotFound:
		return nil, fmt.Errorf("GitHub repo %s/%s not found or is private", owner, repo)
	case http.StatusForbidden:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("GitHub denied this request (rate limit?). Set GITHUB_TOKEN to raise the limit. Detail: %s", strings.TrimSpace(string(body)))
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("GitHub returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

func fetchGitHubCommitSHA(ctx context.Context, owner, repo, ref string) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/commits/%s",
		githubAPIBase(),
		url.PathEscape(owner),
		url.PathEscape(repo),
		url.PathEscape(ref),
	)
	req, err := newGitHubAPIRequest(ctx, endpoint)
	if err != nil {
		return "", err
	}
	resp, err := httpClientWithTimeout.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub commit lookup HTTP %d", resp.StatusCode)
	}
	var commit githubCommitResponse
	if err := json.NewDecoder(resp.Body).Decode(&commit); err != nil {
		return "", err
	}
	return commit.SHA, nil
}

//isoform:destination metadata
func newGitHubAPIRequest(ctx context.Context, endpoint string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build GitHub request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Yansu-Desktop/"+Version)
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

//isoform:destination metadata
func downloadGitHubSkillZip(ctx context.Context, owner, repo, ref, destPath string) error {
	endpoint := fmt.Sprintf("%s/%s/%s/zip/%s",
		githubArchiveBase(),
		url.PathEscape(owner),
		url.PathEscape(repo),
		url.PathEscape(ref),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build GitHub archive request: %w", err)
	}
	req.Header.Set("User-Agent", "Yansu-Desktop/"+Version)
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := httpClientWithTimeout.Do(req)
	if err != nil {
		return fmt.Errorf("download GitHub archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("GitHub archive HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	file, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer file.Close()

	written, err := io.Copy(file, io.LimitReader(resp.Body, maxGitHubSkillZipBytes+1))
	if err != nil {
		return fmt.Errorf("write GitHub archive: %w", err)
	}
	if written > maxGitHubSkillZipBytes {
		return fmt.Errorf("GitHub archive is too large (>%d bytes)", maxGitHubSkillZipBytes)
	}
	return nil
}

func writeGitHubOrigin(skillDir string, origin githubOrigin) error {
	data, err := json.MarshalIndent(origin, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(skillDir, ".yansu-skillhub", "github.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func githubAPIBase() string {
	if v := strings.TrimSpace(os.Getenv("YANSU_GITHUB_API_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return githubAPIBaseURL
}

func githubArchiveBase() string {
	if v := strings.TrimSpace(os.Getenv("YANSU_GITHUB_ARCHIVE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return githubArchiveBaseURL
}

func splitNonEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
