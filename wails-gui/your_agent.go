package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"wails-gui/internal/pii"
)

// Your agent (coming soon), Mac side. The Mac computes the specialties and the
// taste from How you do things. Neither leaves the Mac: only the page name,
// the typed email, the hub id, a hash of the token and the language reach the
// Isoform server (see docs/briefs/your-agent-1.0/README.md).

const (
	yourAgentSpecialtyMinSeen = 5
	yourAgentSpecialtyLimit   = 3
	// A specialty is what someone has done for weeks, not in one session.
	yourAgentSpecialtyMinSpan = 14 * 24 * time.Hour
	yourAgentTasteLimit       = 3
	yourAgentDefaultAPI       = "https://www.isoform.ai/api/agents"
	yourAgentStateFileName    = "your-agent.json"
	yourAgentStateVersion     = 1
	yourAgentNameMin          = 3
	yourAgentNameMax          = 30
	yourAgentTasteMinRunes    = 12
	yourAgentThingMaxRunes    = 40
)

// yourAgentReservedNames are refused by the page name rules (Q2). The server
// keeps the same list.
var yourAgentReservedNames = map[string]bool{
	"isoform": true, "agents": true, "agent": true, "admin": true, "administrator": true,
	"support": true, "help": true, "www": true, "api": true, "app": true, "apps": true,
	"mail": true, "email": true, "root": true, "system": true, "status": true, "blog": true,
	"docs": true, "about": true, "login": true, "signup": true, "settings": true, "bo": true,
	"team": true, "staff": true, "official": true, "security": true, "billing": true,
}

var yourAgentEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

// yourAgentCodePattern is the 6-digit code the email carries. A malformed code
// is refused on the Mac and never reaches the server.
var yourAgentCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

var yourAgentPersonalWords = map[string]bool{
	"home": true, "family": true, "kids": true, "kid": true, "school": true, "health": true,
	"medical": true, "doctor": true, "money": true, "bank": true, "tax": true, "taxes": true,
	"rent": true, "wedding": true, "birthday": true, "personal": true, "recipe": true,
	"vacation": true, "pet": true,
}

// Chinese words for the same personal areas; matched as substrings.
var yourAgentPersonalCJK = []string{"家庭", "家里", "学校", "健康", "医疗", "医生", "银行", "房租", "婚礼", "生日", "个人", "食谱", "宠物", "税"}

var yourAgentThingStopWords = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "for": true, "to": true, "and": true,
	"in": true, "on": true, "with": true, "my": true, "your": true, "our": true, "how": true,
	"what": true, "why": true, "is": true, "are": true, "from": true, "at": true, "by": true,
	"this": true, "that": true, "it": true, "as": true, "into": true, "when": true,
	"where": true, "about": true, "do": true, "does": true,
}

var (
	yourAgentRedactTag   = regexp.MustCompile(`\[REDACTED:[a-z_]+\]`)
	yourAgentDigitRun    = regexp.MustCompile(`[0-9]+([.,:/%-][0-9]+)*`)
	yourAgentSpaceRun    = regexp.MustCompile(`\s+`)
	yourAgentNameWordRun = regexp.MustCompile(`[a-z0-9]+`)
)

// yourAgentStateMu serializes every read and write of the state file.
var yourAgentStateMu sync.Mutex

// YourAgentSpecialty is one kind of work, named by the thing made.
type YourAgentSpecialty struct {
	Name string `json:"name"`
	Seen int    `json:"seen"`
}

// Reservation statuses the server answers with. A missing status (an older
// server, or a state file from before v2.2.0) means verified.
const (
	yourAgentStatusVerified    = "verified"
	yourAgentStatusPending     = "pending"
	yourAgentStatusUnconfirmed = "unconfirmed"
	yourAgentStatusChoice      = "choice"
)

// yourAgentClientMac is the client the Mac names in a reserve.
const yourAgentClientMac = "mac"

// YourAgentReservation is the page name held on the server for this Mac.
// Status is pending while a code is out, choice when the email already has a
// page, unconfirmed for a request made before the code step, and verified once
// the code is in. Place is set for verified only; OldName for choice only.
type YourAgentReservation struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	ReservedAt string `json:"reservedAt"`
	Status     string `json:"status,omitempty"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
	Place      *int   `json:"place,omitempty"`
	OldName    string `json:"oldName,omitempty"`
}

// normalizeYourAgentReservation fills in the status an older answer or state
// file left out. Such a reservation is verified. It returns the same pointer.
func normalizeYourAgentReservation(reservation *YourAgentReservation) *YourAgentReservation {
	if reservation != nil && reservation.Status == "" {
		reservation.Status = yourAgentStatusVerified
	}
	return reservation
}

// YourAgentInsights is the computed part of the page. It is cached per day
// and per state of How you do things.
type YourAgentInsights struct {
	Specialties   []YourAgentSpecialty `json:"specialties"`
	Taste         []string             `json:"taste"`
	StillLearning bool                 `json:"stillLearning"`
}

// YourAgentPage is what Settings › Your agent shows.
type YourAgentPage struct {
	Specialties    []YourAgentSpecialty  `json:"specialties"`
	Taste          []string              `json:"taste"`
	StillLearning  bool                  `json:"stillLearning"`
	NameCandidates []string              `json:"nameCandidates"`
	Reservation    *YourAgentReservation `json:"reservation"`
	Online         bool                  `json:"online"`
	// Computed is false until the Mac has computed the insights once. The page
	// shows placeholder rows then, never Still learning.
	Computed bool `json:"computed"`
}

// YourAgentCheckResult is the answer to a name check. Reason is empty when the
// name is available. "invalid" and "not_allowed" are decided on the Mac; the
// rest come from the server, and "offline" means the server was unreachable.
type YourAgentCheckResult struct {
	Name       string `json:"name"`
	Available  bool   `json:"available"`
	Reason     string `json:"reason"`
	Suggestion string `json:"suggestion"`
}

// YourAgentReserveResult reports one reserve, change, code step or release.
// Status is "reserved" (verified), "pending" (a code is out), "choice" (the
// email already has a page), "unconfirmed", "released", "wrong_code", "expired",
// "none" (a resend found nothing pending), "not_pending", "taken", "not_allowed",
// "invalid_name", "invalid_email", "token", "rate", or "offline".
type YourAgentReserveResult struct {
	Status      string                `json:"status"`
	Suggestion  string                `json:"suggestion"`
	Reservation *YourAgentReservation `json:"reservation"`
	// RetryAt is set with status "rate": RFC 3339, when the next try is allowed.
	RetryAt string `json:"retryAt,omitempty"`
}

// yourAgentSnapshot is the cached insights with the day and the store
// fingerprint they were computed under.
const yourAgentInsightsVersion = 1

type yourAgentSnapshot struct {
	Version     int               `json:"version"`
	Day         string            `json:"day"`
	Fingerprint string            `json:"fingerprint"`
	Insights    YourAgentInsights `json:"insights"`
}

// yourAgentStateFile is your-agent.json. It holds the agent token, which is a
// secret: the file is written with mode 0600.
type yourAgentStateFile struct {
	Version     int                   `json:"version"`
	Hub         string                `json:"hub"`
	Token       string                `json:"token"`
	Reservation *YourAgentReservation `json:"reservation,omitempty"`
	Insights    *yourAgentSnapshot    `json:"insights,omitempty"`
}

// yourAgentEnv is everything the page needs from the Mac. Tests supply temp
// paths, a fake server and fixed entries; the App fills it from its stores.
type yourAgentEnv struct {
	stateFile   string
	client      *yourAgentClient
	fullName    string
	lang        string
	now         func() time.Time
	fingerprint func() (string, error)
	entries     func() ([]KnowledgeEntry, error)
	hubFallback func() string
}

// yourAgentFullName is the macOS full name from Users & Groups.
func yourAgentFullName() string {
	current, err := user.Current()
	if err != nil {
		return ""
	}
	return current.Name
}

// yourAgentNameWords folds accents, lowercases and keeps the letter and digit
// runs. "José Núñez" gives [jose nunez]; a name with no Latin letters gives nil.
func yourAgentNameWords(fullName string) []string {
	folded, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), fullName)
	if err != nil {
		folded = fullName
	}
	return yourAgentNameWordRun.FindAllString(strings.ToLower(folded), -1)
}

// yourAgentNameCandidates is the order the spec gives: the full-name form, the
// same without dots, then the first initial and the last word. Candidates that
// are not valid page names are left out.
func yourAgentNameCandidates(fullName string) []string {
	words := yourAgentNameWords(fullName)
	if len(words) == 0 {
		return nil
	}
	raw := []string{strings.Join(words, ".")}
	raw = append(raw, strings.Join(words, ""))
	if len(words) >= 2 {
		raw = append(raw, words[0][:1]+"."+words[len(words)-1])
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, candidate := range raw {
		if yourAgentNameValid(candidate) && !seen[candidate] {
			seen[candidate] = true
			out = append(out, candidate)
		}
	}
	return out
}

// yourAgentRequestCandidates is the list sent with a check or a request: the
// Mac's full-name forms, then the typed name last. The server suffixes the
// typed name with 2, 3 and so on when every earlier candidate is taken (v2.0.0).
func yourAgentRequestCandidates(fullName, typed string) []string {
	candidates := yourAgentNameCandidates(fullName)
	if yourAgentNameValid(typed) {
		candidates = append(candidates, typed)
	}
	return candidates
}

// yourAgentNameValid applies the page name rule: 3 to 30 characters of a to z,
// 0 to 9 and dots, no dot first or last, and never two dots in a row. This is
// the spec's pattern without lookahead, which Go's regexp does not support.
func yourAgentNameValid(name string) bool {
	if len(name) < yourAgentNameMin || len(name) > yourAgentNameMax {
		return false
	}
	last := len(name) - 1
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '.':
			if i == 0 || i == last || name[i+1] == '.' {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func yourAgentNameReserved(name string) bool {
	return yourAgentReservedNames[name]
}

// yourAgentNormalizeName lowercases and trims a typed name.
func yourAgentNormalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func yourAgentEmailValid(email string) bool {
	return len(email) <= 254 && yourAgentEmailPattern.MatchString(email)
}

// yourAgentIsMailOrMessages keeps anything from Mail or Messages out.
func yourAgentIsMailOrMessages(app string) bool {
	switch strings.ToLower(strings.TrimSpace(app)) {
	case "mail", "mail.app", "messages", "messages.app", "imessage", "com.apple.mail", "com.apple.mobilesms":
		return true
	}
	return false
}

// yourAgentEntryExcluded reports whether an entry must stay out of the page:
// it came from Mail or Messages, from an app the person keeps out of Isoform,
// or it is a personal or family pattern.
func yourAgentEntryExcluded(entry KnowledgeEntry) bool {
	for _, app := range splitCommaList(entry.Apps) {
		if yourAgentIsMailOrMessages(app) || pii.IsSensitiveAppName(app) {
			return true
		}
	}
	return yourAgentPersonal(entry.Title)
}

func yourAgentPersonal(title string) bool {
	for _, word := range strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if yourAgentPersonalWords[word] {
			return true
		}
	}
	for _, word := range yourAgentPersonalCJK {
		if strings.Contains(title, word) {
			return true
		}
	}
	return false
}

// yourAgentThingKey names the thing made. Without a stored "thing made" field,
// the key is the first three content words of the pattern's own title, with a
// trailing plural s dropped for matching: "Weekly update" and "Weekly updates"
// share one key. The display keeps the title's own words.
func yourAgentThingKey(title string) (key string, display string) {
	words := strings.FieldsFunc(strings.TrimSpace(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	kept := make([]string, 0, yourAgentSpecialtyLimit)
	for _, word := range words {
		if yourAgentThingStopWords[strings.ToLower(word)] {
			continue
		}
		kept = append(kept, word)
		if len(kept) == yourAgentSpecialtyLimit {
			break
		}
	}
	if len(kept) == 0 {
		return "", ""
	}
	display = strings.Join(kept, " ")
	if rs := []rune(display); len(rs) > yourAgentThingMaxRunes {
		display = strings.TrimSpace(string(rs[:yourAgentThingMaxRunes]))
	}
	keyWords := make([]string, len(kept))
	for i, word := range kept {
		keyWords[i] = strings.ToLower(word)
	}
	last := keyWords[len(keyWords)-1]
	if len(last) > 3 && strings.HasSuffix(last, "s") && !strings.HasSuffix(last, "ss") {
		keyWords[len(keyWords)-1] = strings.TrimSuffix(last, "s")
	}
	return strings.Join(keyWords, " "), display
}

type yourAgentGroup struct {
	display     string
	sessions    map[string]bool
	entries     []KnowledgeEntry
	first, last time.Time
}

// yourAgentEntryTime is when the entry was saved, or zero when its date is unreadable.
func yourAgentEntryTime(entry KnowledgeEntry) time.Time {
	value := strings.TrimSpace(entry.Date)
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02"} {
		if at, err := time.Parse(layout, value); err == nil {
			return at
		}
	}
	return time.Time{}
}

var (
	yourAgentToolCase   = regexp.MustCompile(`^\p{Ll}+\p{Lu}|^\p{Lu}+\p{Ll}+\p{Lu}`)
	yourAgentShortWords = map[string]bool{"web": true, "app": true, "ads": true, "art": true, "tax": true, "pay": true, "law": true, "job": true}
)

// yourAgentToolWord reports a word that reads as a product or a command: a
// CamelCase brand, digits or code punctuation, or a short lowercase command.
func yourAgentToolWord(word string) bool {
	if strings.ContainsAny(word, "0123456789_./`") || yourAgentToolCase.MatchString(word) {
		return true
	}
	if len(word) > 3 || word != strings.ToLower(word) || yourAgentShortWords[word] {
		return false
	}
	for _, r := range word {
		if r > unicode.MaxASCII || !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// yourAgentSpecialtyNamed keeps a specialty a buyer would understand: never a
// product or tool name alone, so a tool word, or nothing but the entries' own
// app names, leaves it out.
func yourAgentSpecialtyNamed(display string, entries []KnowledgeEntry) bool {
	apps := map[string]bool{}
	for _, entry := range entries {
		for _, app := range splitCommaList(entry.Apps) {
			for _, word := range strings.Fields(strings.ToLower(app)) {
				apps[word] = true
			}
		}
	}
	plain := 0
	for _, word := range strings.Fields(display) {
		if yourAgentToolWord(word) {
			return false
		}
		if !apps[strings.ToLower(word)] {
			plain++
		}
	}
	return plain > 0
}

// computeYourAgentInsights groups work patterns by the thing made and counts
// the separate pieces: each session that contributed to an entry is one piece,
// and a piece is counted once even when two patterns make the same thing. It
// keeps the top three groups seen at least five times over at least two weeks,
// named in plain words rather than a product or tool, and takes the taste from
// the top group's entries, then from the next groups.
// yourAgentSpecialtySource supplies the same specialty calculation to the
// Settings page and the work-profile builder.
type yourAgentSpecialtySource interface {
	Insights([]KnowledgeEntry, string) YourAgentInsights
}

type knowHowSpecialtySource struct{}

func (knowHowSpecialtySource) Insights(entries []KnowledgeEntry, fullName string) YourAgentInsights {
	return computeYourAgentInsights(entries, fullName)
}

func computeYourAgentInsights(entries []KnowledgeEntry, fullName string) YourAgentInsights {
	groups := map[string]*yourAgentGroup{}
	for _, entry := range entries {
		if yourAgentEntryExcluded(entry) {
			continue
		}
		key, display := yourAgentThingKey(entry.Title)
		if key == "" {
			continue
		}
		group, ok := groups[key]
		if !ok {
			group = &yourAgentGroup{display: display, sessions: map[string]bool{}}
			groups[key] = group
		}
		for _, id := range entry.SessionIDs {
			if id != "" {
				group.sessions[id] = true
			}
		}
		group.entries = append(group.entries, entry)
		if at := yourAgentEntryTime(entry); !at.IsZero() {
			if group.first.IsZero() || at.Before(group.first) {
				group.first = at
			}
			if at.After(group.last) {
				group.last = at
			}
		}
	}

	ranked := make([]YourAgentSpecialty, 0, len(groups))
	byName := map[string]*yourAgentGroup{}
	for _, group := range groups {
		if len(group.sessions) < yourAgentSpecialtyMinSeen ||
			group.last.Sub(group.first) < yourAgentSpecialtyMinSpan ||
			!yourAgentSpecialtyNamed(group.display, group.entries) {
			continue
		}
		ranked = append(ranked, YourAgentSpecialty{Name: group.display, Seen: len(group.sessions)})
		byName[group.display] = group
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Seen != ranked[j].Seen {
			return ranked[i].Seen > ranked[j].Seen
		}
		return ranked[i].Name < ranked[j].Name
	})
	if len(ranked) > yourAgentSpecialtyLimit {
		ranked = ranked[:yourAgentSpecialtyLimit]
	}

	insights := YourAgentInsights{Specialties: ranked, Taste: []string{}}
	if len(ranked) == 0 {
		insights.StillLearning = true
		return insights
	}
	for _, specialty := range ranked {
		group := byName[specialty.Name]
		sort.SliceStable(group.entries, func(i, j int) bool {
			return knowHowCard(group.entries[i]).SavedAt > knowHowCard(group.entries[j]).SavedAt
		})
		for _, entry := range group.entries {
			if len(insights.Taste) >= yourAgentTasteLimit {
				break
			}
			if line := yourAgentTasteLine(entry, fullName); line != "" {
				insights.Taste = append(insights.Taste, line)
			}
		}
		if len(insights.Taste) >= yourAgentTasteLimit {
			break
		}
	}
	return insights
}

// yourAgentTasteLine turns one know-how entry into one line for the taste
// card: its takeaway, or its first point, with names and numbers stripped,
// kept only when it reads as a skill said to the person (yourAgentTasteSkill).
// Names are the person's own name words, plus anything the PII redaction
// finds. An empty result means the entry has nothing usable.
func yourAgentTasteLine(entry KnowledgeEntry, fullName string) string {
	card := knowHowCard(entry)
	candidates := append([]string{card.Takeaway}, card.Points...)
	for _, text := range candidates {
		if line := yourAgentRedactTaste(text, fullName); line != "" && yourAgentTasteSkill(line) {
			return line
		}
	}
	return ""
}

var (
	yourAgentTasteCode  = regexp.MustCompile("`" + `|\$\w|(^|\s)--?\p{Ll}|\b\w+_\w+\b|\b\p{Ll}+\p{Lu}\w*|\w\.(go|ts|tsx|js|py|md|json|sh|ya?ml|toml)\b|(^|\s)[~.]?/\w|\(\)|[{}<>]`)
	yourAgentTasteNever = []string{"observed", "the user", "user's", "users ", "api key", "token", "password", "passcode", "secret", "credential", "private key"}
)

// yourAgentTasteSkill keeps a taste line that is a skill, said to the person
// ("You can ..."): never a log line about "the user", code, or a key, token
// or password.
func yourAgentTasteSkill(line string) bool {
	lower := strings.ToLower(line)
	for _, word := range yourAgentTasteNever {
		if strings.Contains(lower, word) {
			return false
		}
	}
	if yourAgentTasteCode.MatchString(line) {
		return false
	}
	return strings.HasPrefix(line, "You ") || strings.HasPrefix(line, "You'") || strings.HasPrefix(line, "你")
}

func yourAgentRedactTaste(text, fullName string) string {
	redacted, _ := pii.RedactText(text)
	line := yourAgentRedactTag.ReplaceAllString(redacted, "")
	for _, word := range strings.Fields(fullName) {
		if len([]rune(word)) < 2 {
			continue
		}
		pattern := regexp.MustCompile(`(?i)(^|[^\p{L}])` + regexp.QuoteMeta(word) + `([^\p{L}]|$)`)
		line = pattern.ReplaceAllString(line, "$1$2")
	}
	line = yourAgentDigitRun.ReplaceAllString(line, "")
	line = strings.TrimSpace(yourAgentSpaceRun.ReplaceAllString(line, " "))
	line = strings.Trim(line, " ,;:-")
	if len([]rune(line)) < yourAgentTasteMinRunes {
		return ""
	}
	return line
}

// yourAgentDirFingerprint hashes the names, times and sizes of the knowledge
// files. It changes whenever How you do things changes, and costs one listing.
func yourAgentDirFingerprint(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "empty", nil
		}
		return "", err
	}
	hash := sha256.New()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(hash, "%s|%d|%d\n", entry.Name(), info.ModTime().UnixNano(), info.Size())
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// loadYourAgentState reads the state file, or creates it with a new hub id
// and token. The hub id is the Mac's mobile hub id when one exists, so the
// agent and the phone pairing agree; otherwise a new UUID is kept.
func loadYourAgentState(env yourAgentEnv) (*yourAgentStateFile, error) {
	yourAgentStateMu.Lock()
	defer yourAgentStateMu.Unlock()
	return loadYourAgentStateLocked(env)
}

func loadYourAgentStateLocked(env yourAgentEnv) (*yourAgentStateFile, error) {
	data, err := os.ReadFile(env.stateFile)
	if err == nil {
		var state yourAgentStateFile
		if err := json.Unmarshal(data, &state); err == nil && state.Token != "" && state.Hub != "" {
			normalizeYourAgentReservation(state.Reservation)
			return &state, nil
		}
		// A damaged file is replaced below; the token it held is gone with it.
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read your agent state: %w", err)
	}
	token, err := newYourAgentToken()
	if err != nil {
		return nil, err
	}
	hub := ""
	if env.hubFallback != nil {
		hub = env.hubFallback()
	}
	if !mobileValidUUID(hub) {
		hub = uuid.NewString()
	}
	state := &yourAgentStateFile{Version: yourAgentStateVersion, Hub: hub, Token: token}
	if err := writeYourAgentStateLocked(env.stateFile, state); err != nil {
		return nil, err
	}
	return state, nil
}

func newYourAgentToken() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("make your agent token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(random[:]), nil
}

// writeYourAgentStateLocked writes through a temp file so the token file is
// never left half-written, and keeps it at mode 0600.
func writeYourAgentStateLocked(path string, state *yourAgentStateFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("make your agent folder: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".your-agent-*.json")
	if err != nil {
		return fmt.Errorf("write your agent state: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return fmt.Errorf("protect your agent state: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write your agent state: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("write your agent state: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace your agent state: %w", err)
	}
	return nil
}

// updateYourAgentState applies change to the stored state under the lock.
func updateYourAgentState(env yourAgentEnv, change func(state *yourAgentStateFile)) (*yourAgentStateFile, error) {
	yourAgentStateMu.Lock()
	defer yourAgentStateMu.Unlock()
	state, err := loadYourAgentStateLocked(env)
	if err != nil {
		return nil, err
	}
	change(state)
	if err := writeYourAgentStateLocked(env.stateFile, state); err != nil {
		return nil, err
	}
	return state, nil
}

func yourAgentDay(now time.Time) string {
	return now.Format("2006-01-02")
}

// refreshYourAgentInsights recomputes the insights when the day or the store
// changed. It is the only place that reads How you do things, and it runs in
// the background, never on a click.
func refreshYourAgentInsights(env yourAgentEnv) error {
	fingerprint, err := env.fingerprint()
	if err != nil {
		return err
	}
	day := yourAgentDay(env.now())
	state, err := loadYourAgentState(env)
	if err != nil {
		return err
	}
	if snap := state.Insights; snap != nil && snap.Version == yourAgentInsightsVersion && snap.Day == day && snap.Fingerprint == fingerprint {
		return nil
	}
	entries, err := env.entries()
	if err != nil {
		return err
	}
	insights := (knowHowSpecialtySource{}).Insights(entries, env.fullName)
	_, err = updateYourAgentState(env, func(state *yourAgentStateFile) {
		state.Insights = &yourAgentSnapshot{Version: yourAgentInsightsVersion, Day: day, Fingerprint: fingerprint, Insights: insights}
	})
	return err
}

// yourAgentCachedInsights returns the stored insights, or a still-learning
// result when nothing has been computed yet.
func yourAgentCachedInsights(state *yourAgentStateFile) YourAgentInsights {
	if state.Insights == nil || state.Insights.Version != yourAgentInsightsVersion {
		// Not computed yet: nothing to show, and no Still learning claim.
		return YourAgentInsights{Specialties: []YourAgentSpecialty{}, Taste: []string{}}
	}
	return state.Insights.Insights
}

// yourAgentLoadPage builds the page from the cache at once, then asks the
// server for the reservation with the 3 second timeout. An unreachable server
// keeps the cached reservation and reports offline.
// yourAgentCachedPage builds the page from the stored state only. It makes no
// network call, so the page can paint at once.
func yourAgentCachedPage(env yourAgentEnv) (YourAgentPage, error) {
	state, err := loadYourAgentState(env)
	if err != nil {
		return YourAgentPage{}, err
	}
	insights := yourAgentCachedInsights(state)
	return YourAgentPage{
		Specialties:    insights.Specialties,
		Taste:          insights.Taste,
		StillLearning:  insights.StillLearning,
		NameCandidates: yourAgentNameCandidates(env.fullName),
		Reservation:    state.Reservation,
		Online:         true,
		Computed:       state.Insights != nil && state.Insights.Version == yourAgentInsightsVersion,
	}, nil
}

// YourAgentReservationResult is the answer of the reservation read. Online is
// false when the server could not be reached; Reservation is then the cached one.
// RanOut is set when the pending or choice request this Mac knew about is gone:
// the page refills its form from it and says the name was let go.
type YourAgentReservationResult struct {
	Online      bool                  `json:"online"`
	Reservation *YourAgentReservation `json:"reservation"`
	RanOut      *YourAgentReservation `json:"ranOut,omitempty"`
}

// yourAgentReadReservation asks the server for this Mac's reservation, with the
// 3 second timeout. A refused token or an odd answer keeps the cache and is
// not reported as offline. A pending or choice request the server no longer
// holds is let go on this Mac, and RanOut reports it.
func yourAgentReadReservation(ctx context.Context, env yourAgentEnv) (YourAgentReservationResult, error) {
	state, err := loadYourAgentState(env)
	if err != nil {
		return YourAgentReservationResult{}, err
	}
	reservation, err := env.client.reservation(ctx, state.Hub, state.Token)
	if errors.Is(err, errYourAgentOffline) {
		return YourAgentReservationResult{Online: false, Reservation: state.Reservation}, nil
	}
	if err != nil {
		return YourAgentReservationResult{Online: true, Reservation: state.Reservation}, nil
	}
	if ranOut := yourAgentRanOut(state.Reservation, reservation, env.now()); ranOut != nil {
		// An expired email change can reveal the still-verified original.
		// Keep that server answer while the form refills the expired change.
		if reservation != nil && yourAgentHeld(reservation.Status) {
			reservation = nil
		}
		if _, saveErr := updateYourAgentState(env, func(state *yourAgentStateFile) {
			state.Reservation = reservation
		}); saveErr != nil {
			return YourAgentReservationResult{}, saveErr
		}
		return YourAgentReservationResult{Online: true, Reservation: reservation, RanOut: ranOut}, nil
	}
	if _, saveErr := updateYourAgentState(env, func(state *yourAgentStateFile) {
		state.Reservation = reservation
	}); saveErr != nil {
		return YourAgentReservationResult{Online: true, Reservation: state.Reservation}, nil
	}
	return YourAgentReservationResult{Online: true, Reservation: reservation}, nil
}

// yourAgentLoadPage is the cache plus the reservation read, for the phone route.
func yourAgentLoadPage(ctx context.Context, env yourAgentEnv) (YourAgentPage, error) {
	page, err := yourAgentCachedPage(env)
	if err != nil {
		return YourAgentPage{}, err
	}
	read, err := yourAgentReadReservation(ctx, env)
	if err != nil {
		return YourAgentPage{}, err
	}
	page.Online = read.Online
	page.Reservation = read.Reservation
	return page, nil
}

// yourAgentRouteView is the sealed GET /your-agent body. The token travels
// only inside the sealed channel; the phone keeps it for the reserve calls.
type yourAgentRouteView struct {
	Specialties    []YourAgentSpecialty  `json:"specialties"`
	Taste          []string              `json:"taste"`
	StillLearning  bool                  `json:"stillLearning"`
	Computed       bool                  `json:"computed"`
	NameCandidates []string              `json:"nameCandidates"`
	Hub            string                `json:"hub"`
	Token          string                `json:"token"`
	Reservation    *YourAgentReservation `json:"reservation"`
	RanOut         *YourAgentReservation `json:"ranOut,omitempty"`
}

func yourAgentRoute(ctx context.Context, env yourAgentEnv) (yourAgentRouteView, error) {
	page, err := yourAgentCachedPage(env)
	if err != nil {
		return yourAgentRouteView{}, err
	}
	read, err := yourAgentReadReservation(ctx, env)
	if err != nil {
		return yourAgentRouteView{}, err
	}
	state, err := loadYourAgentState(env)
	if err != nil {
		return yourAgentRouteView{}, err
	}
	return yourAgentRouteView{
		Specialties:    page.Specialties,
		Taste:          page.Taste,
		StillLearning:  page.StillLearning,
		Computed:       page.Computed,
		NameCandidates: page.NameCandidates,
		Hub:            state.Hub,
		Token:          state.Token,
		Reservation:    read.Reservation,
		RanOut:         read.RanOut,
	}, nil
}

// yourAgentCheck validates the name on the Mac, then asks the server. The
// name is available to this Mac when the server says so, including when this
// Mac already holds it.
func yourAgentCheck(ctx context.Context, env yourAgentEnv, name string) (YourAgentCheckResult, error) {
	normalized := yourAgentNormalizeName(name)
	result := YourAgentCheckResult{Name: normalized}
	if !yourAgentNameValid(normalized) {
		result.Reason = "invalid"
		return result, nil
	}
	if yourAgentNameReserved(normalized) {
		result.Reason = "not_allowed"
		return result, nil
	}
	state, err := loadYourAgentState(env)
	if err != nil {
		return result, err
	}
	request := yourAgentCheckRequest{Name: normalized, Candidates: yourAgentRequestCandidates(env.fullName, normalized)}
	if state.Reservation != nil {
		request.Hub = state.Hub
		request.Token = state.Token
	}
	answer, err := env.client.check(ctx, request)
	if errors.Is(err, errYourAgentOffline) {
		result.Reason = "offline"
		return result, nil
	}
	if err != nil {
		return result, err
	}
	return answer, nil
}

// yourAgentReserve checks the typed name and email on the Mac, then reserves
// the name for this Mac. A reserve for a Mac that already holds a name is a
// change on the server. While a code is out the answer is pending.
func yourAgentReserve(ctx context.Context, env yourAgentEnv, name, email string) (YourAgentReserveResult, error) {
	normalized := yourAgentNormalizeName(name)
	trimmedEmail := strings.TrimSpace(email)
	if !yourAgentNameValid(normalized) {
		return YourAgentReserveResult{Status: "invalid_name"}, nil
	}
	if yourAgentNameReserved(normalized) {
		return YourAgentReserveResult{Status: "not_allowed"}, nil
	}
	if !yourAgentEmailValid(trimmedEmail) {
		return YourAgentReserveResult{Status: "invalid_email"}, nil
	}
	candidates := yourAgentRequestCandidates(env.fullName, normalized)
	reservation, err := yourAgentWithToken(env, func(hub, token string) (YourAgentReservation, error) {
		return env.client.reserve(ctx, yourAgentReserveRequest{
			Hub:        hub,
			Token:      token,
			Name:       normalized,
			Email:      trimmedEmail,
			Lang:       env.lang,
			Candidates: candidates,
			Client:     yourAgentClientMac,
		})
	})
	if err != nil {
		return yourAgentReserveFailure(err), nil
	}
	return yourAgentKeep(env, reservation)
}

// yourAgentRelease gives the name back. The local reservation is cleared only
// after the server accepts the release.
func yourAgentRelease(ctx context.Context, env yourAgentEnv) (YourAgentReserveResult, error) {
	state, err := loadYourAgentState(env)
	if err != nil {
		return YourAgentReserveResult{}, err
	}
	if err := env.client.release(ctx, state.Hub, state.Token); err != nil {
		return yourAgentReserveFailure(err), nil
	}
	if _, err := updateYourAgentState(env, func(state *yourAgentStateFile) {
		state.Reservation = nil
	}); err != nil {
		return YourAgentReserveResult{}, err
	}
	return YourAgentReserveResult{Status: "released"}, nil
}

// yourAgentKeep stores the reservation the server answered with and returns the
// result for it. Pending and choice answers are kept too, so the code step and
// the choice card come back after a restart.
func yourAgentKeep(env yourAgentEnv, reservation YourAgentReservation) (YourAgentReserveResult, error) {
	if _, err := updateYourAgentState(env, func(state *yourAgentStateFile) {
		state.Reservation = &reservation
	}); err != nil {
		return YourAgentReserveResult{}, err
	}
	return yourAgentReservedResult(reservation), nil
}

// yourAgentReservedResult is the result of a call that answered with a reservation.
func yourAgentReservedResult(reservation YourAgentReservation) YourAgentReserveResult {
	return YourAgentReserveResult{Status: yourAgentResultStatus(&reservation), Reservation: &reservation}
}

// yourAgentResultStatus names what a reservation answer means for the page. An
// unconfirmed request answers pending once a code is out.
func yourAgentResultStatus(reservation *YourAgentReservation) string {
	switch reservation.Status {
	case yourAgentStatusPending:
		return "pending"
	case yourAgentStatusChoice:
		return "choice"
	case yourAgentStatusUnconfirmed:
		if reservation.ExpiresAt != "" {
			return "pending"
		}
		return "unconfirmed"
	}
	return "reserved"
}

// yourAgentForget clears the reservation this Mac keeps, because the server no
// longer holds it.
func yourAgentForget(env yourAgentEnv) error {
	_, err := updateYourAgentState(env, func(state *yourAgentStateFile) {
		state.Reservation = nil
	})
	return err
}

// yourAgentCodeStatus maps an error from a call that answers with a code or a
// reservation to the status the page shows. The reserve statuses cover the rest.
func yourAgentCodeStatus(err error) YourAgentReserveResult {
	var apiErr *yourAgentAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "wrong_code", "expired", "none", "not_pending":
			return YourAgentReserveResult{Status: apiErr.Code}
		}
	}
	return yourAgentReserveFailure(err)
}

// yourAgentCodeFailure is yourAgentCodeStatus for the calls that can let the
// request go. Taken and none both mean the server no longer holds the request,
// so this Mac forgets it too.
func yourAgentCodeFailure(env yourAgentEnv, err error) (YourAgentReserveResult, error) {
	result := yourAgentCodeStatus(err)
	if result.Status == "taken" || result.Status == "none" {
		if forgetErr := yourAgentForget(env); forgetErr != nil {
			return YourAgentReserveResult{}, forgetErr
		}
	}
	return result, nil
}

// yourAgentVerify checks the 6-digit code from the email for the pending
// request. A malformed code is refused here and never reaches the server.
func yourAgentVerify(ctx context.Context, env yourAgentEnv, code string) (YourAgentReserveResult, error) {
	if !yourAgentCodePattern.MatchString(code) {
		return YourAgentReserveResult{Status: "wrong_code"}, nil
	}
	reservation, err := yourAgentWithToken(env, func(hub, token string) (YourAgentReservation, error) {
		return env.client.verify(ctx, hub, token, code)
	})
	if err != nil {
		return yourAgentCodeFailure(env, err)
	}
	return yourAgentKeep(env, reservation)
}

// yourAgentResend asks for a new code. The old code stops working and the hold
// restarts. A none answer means nothing is pending any more, and the page then
// reserves the same name and email again.
func yourAgentResend(ctx context.Context, env yourAgentEnv) (YourAgentReserveResult, error) {
	reservation, err := yourAgentWithToken(env, func(hub, token string) (YourAgentReservation, error) {
		return env.client.resend(ctx, hub, token)
	})
	if err != nil {
		return yourAgentCodeFailure(env, err)
	}
	return yourAgentKeep(env, reservation)
}

// yourAgentChoose answers "already has a page". Keep makes the email's existing
// page this Mac's; otherwise the request moves to the new name and the old page
// is let go.
func yourAgentChoose(ctx context.Context, env yourAgentEnv, keep bool) (YourAgentReserveResult, error) {
	reservation, err := yourAgentWithToken(env, func(hub, token string) (YourAgentReservation, error) {
		return env.client.choose(ctx, hub, token, keep)
	})
	if err != nil {
		result := yourAgentCodeStatus(err)
		if result.Status != "expired" {
			return result, nil
		}
		return yourAgentReadAfterLapse(ctx, env, result)
	}
	return yourAgentKeep(env, reservation)
}

// yourAgentReadAfterLapse reads what the server holds after a choice ran out:
// the verified original, or nothing. An unreachable server keeps this Mac's copy.
func yourAgentReadAfterLapse(ctx context.Context, env yourAgentEnv, result YourAgentReserveResult) (YourAgentReserveResult, error) {
	current, err := yourAgentWithToken(env, func(hub, token string) (*YourAgentReservation, error) {
		return env.client.reservation(ctx, hub, token)
	})
	if err != nil {
		return result, nil
	}
	if _, saveErr := updateYourAgentState(env, func(state *yourAgentStateFile) {
		state.Reservation = current
	}); saveErr != nil {
		return YourAgentReserveResult{}, saveErr
	}
	result.Reservation = current
	return result, nil
}

// yourAgentCancel is "Change the name or email": it lets a pending hold or a
// choice go. A verified original stays, and the result carries it.
func yourAgentCancel(ctx context.Context, env yourAgentEnv) (YourAgentReserveResult, error) {
	left, err := yourAgentWithToken(env, func(hub, token string) (*YourAgentReservation, error) {
		return env.client.cancel(ctx, hub, token)
	})
	if err != nil {
		return yourAgentCodeFailure(env, err)
	}
	if _, saveErr := updateYourAgentState(env, func(state *yourAgentStateFile) {
		state.Reservation = left
	}); saveErr != nil {
		return YourAgentReserveResult{}, saveErr
	}
	if left == nil {
		return YourAgentReserveResult{Status: "released"}, nil
	}
	return yourAgentReservedResult(*left), nil
}

// yourAgentRanOut reports the pending or choice request this Mac knew about
// when the server no longer holds it: it answered none, or the hold has passed.
func yourAgentRanOut(cached, answer *YourAgentReservation, now time.Time) *YourAgentReservation {
	if cached == nil || !yourAgentHeld(cached.Status) {
		return nil
	}
	if answer == nil {
		return cached
	}
	if yourAgentHeld(answer.Status) {
		if yourAgentLapsed(answer.ExpiresAt, now) {
			return answer
		}
		return nil // A newer server expiry can reflect a resend on another device.
	}
	if yourAgentLapsed(cached.ExpiresAt, now) && (cached.Name != answer.Name || cached.Email != answer.Email) {
		return cached
	}
	return nil
}

// yourAgentHeld is true for the statuses that hold a name for a short time.
func yourAgentHeld(status string) bool {
	return status == yourAgentStatusPending || status == yourAgentStatusChoice
}

// yourAgentLapsed is true when expiresAt (RFC 3339) has passed.
func yourAgentLapsed(expiresAt string, now time.Time) bool {
	expiry, err := time.Parse(time.RFC3339, expiresAt)
	return err == nil && !now.Before(expiry)
}

// yourAgentReserveFailure maps a client error to the status the page shows.
func yourAgentReserveFailure(err error) YourAgentReserveResult {
	if errors.Is(err, errYourAgentOffline) {
		return YourAgentReserveResult{Status: "offline"}
	}
	var apiErr *yourAgentAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 409:
			return YourAgentReserveResult{Status: "taken", Suggestion: apiErr.Suggestion}
		case 422:
			return YourAgentReserveResult{Status: apiErr.Code}
		case 403:
			return YourAgentReserveResult{Status: "token"}
		case 429:
			retryAt := apiErr.RetryAt
			if retryAt == "" {
				// Until the server sends retryAt, the next try is the next UTC hour.
				retryAt = yourAgentNextUTCHour(time.Now()).Format(time.RFC3339)
			}
			return YourAgentReserveResult{Status: "rate", RetryAt: retryAt}
		}
	}
	return YourAgentReserveResult{Status: "offline"}
}

// yourAgentRefused reports whether the server answered 403, which means the
// token was refused.
func yourAgentRefused(err error) bool {
	var apiErr *yourAgentAPIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden
}

// yourAgentWithToken runs one call with the hub and token the state file holds.
// A refused token is re-read once and the call tried again, as reserve does.
func yourAgentWithToken[T any](env yourAgentEnv, call func(hub, token string) (T, error)) (T, error) {
	state, err := loadYourAgentState(env)
	if err != nil {
		var zero T
		return zero, err
	}
	value, err := call(state.Hub, state.Token)
	if !yourAgentRefused(err) {
		return value, err
	}
	fresh, loadErr := loadYourAgentState(env)
	if loadErr != nil {
		var zero T
		return zero, loadErr
	}
	return call(fresh.Hub, fresh.Token)
}

// yourAgentNextUTCHour is the start of the next UTC hour after now.
func yourAgentNextUTCHour(now time.Time) time.Time {
	utc := now.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), utc.Hour()+1, 0, 0, 0, time.UTC)
}

// yourAgentEnvFor builds the environment from the App's stores. The state
// folder is the data folder, so the token file is ~/.isoform-agent/your-agent.json.
func (a *App) yourAgentEnvFor() (yourAgentEnv, error) {
	dir, err := flavorDataDir()
	if err != nil {
		return yourAgentEnv{}, err
	}
	return yourAgentEnv{
		stateFile: filepath.Join(dir, yourAgentStateFileName),
		client:    newYourAgentClient(os.Getenv("ISOFORM_AGENTS_API")),
		fullName:  yourAgentFullName(),
		lang:      normalizeUILanguage(a.currentUILanguage()),
		now:       time.Now,
		fingerprint: func() (string, error) {
			knowledge, err := knowledgeDir()
			if err != nil {
				return "", err
			}
			return yourAgentDirFingerprint(knowledge)
		},
		entries: func() ([]KnowledgeEntry, error) {
			return a.GetKnowledgeEntries()
		},
		hubFallback: func() string {
			identity, err := loadMobileHubIdentity(mobileStateDirectoryOrEmpty())
			if err != nil || identity == nil {
				return ""
			}
			return identity.HubID
		},
	}, nil
}

func mobileStateDirectoryOrEmpty() string {
	dir, err := mobileStateDirectory()
	if err != nil {
		return ""
	}
	return dir
}

// yourAgentRefreshInBackground starts one background refresh at a time. A page
// open never waits for it.
func (a *App) yourAgentRefreshInBackground(env yourAgentEnv) {
	a.yourAgentMu.Lock()
	if a.yourAgentRefreshing {
		a.yourAgentMu.Unlock()
		return
	}
	a.yourAgentRefreshing = true
	a.yourAgentMu.Unlock()
	go func() {
		defer func() {
			a.yourAgentMu.Lock()
			a.yourAgentRefreshing = false
			a.yourAgentMu.Unlock()
		}()
		if err := refreshYourAgentInsights(env); err != nil {
			logYourAgent("refresh insights", err)
		}
	}()
}

func logYourAgent(step string, err error) {
	log.Printf("[your-agent] %s: %v", step, err)
}

// GetYourAgentPage returns the cached page at once. The server reservation is
// read with a 3 second timeout; the insights refresh runs in the background.
func (a *App) GetYourAgentPage() (perfValue YourAgentPage, perfErr error) {
	defer logPageBinding("GetYourAgentPage", time.Now(), &perfValue)
	env, err := a.yourAgentEnvFor()
	if err != nil {
		return YourAgentPage{}, err
	}
	a.yourAgentRefreshInBackground(env)
	return yourAgentCachedPage(env)
}

// GetYourAgentReservation reads this Mac's reservation from the server. The
// page calls it after the cached page has painted, and updates in place.
func (a *App) GetYourAgentReservation() (perfValue YourAgentReservationResult, perfErr error) {
	defer logPageBinding("GetYourAgentReservation", time.Now(), &perfValue)
	env, err := a.yourAgentEnvFor()
	if err != nil {
		return YourAgentReservationResult{}, err
	}
	return yourAgentReadReservation(context.Background(), env)
}

// CheckYourAgentName reports whether a page name is free.
func (a *App) CheckYourAgentName(name string) (YourAgentCheckResult, error) {
	env, err := a.yourAgentEnvFor()
	if err != nil {
		return YourAgentCheckResult{}, err
	}
	return yourAgentCheck(context.Background(), env, name)
}

// ReserveYourAgent reserves the page name and the email, or changes them.
func (a *App) ReserveYourAgent(name, email string) (YourAgentReserveResult, error) {
	env, err := a.yourAgentEnvFor()
	if err != nil {
		return YourAgentReserveResult{}, err
	}
	return yourAgentReserve(context.Background(), env, name, email)
}

// ReleaseYourAgent gives the reserved page name back.
func (a *App) ReleaseYourAgent() (YourAgentReserveResult, error) {
	env, err := a.yourAgentEnvFor()
	if err != nil {
		return YourAgentReserveResult{}, err
	}
	return yourAgentRelease(context.Background(), env)
}

// VerifyYourAgentCode checks the 6-digit code from the email for the pending request.
func (a *App) VerifyYourAgentCode(code string) (YourAgentReserveResult, error) {
	env, err := a.yourAgentEnvFor()
	if err != nil {
		return YourAgentReserveResult{}, err
	}
	return yourAgentVerify(context.Background(), env, code)
}

// ResendYourAgentCode sends a new 6-digit code for the pending request.
func (a *App) ResendYourAgentCode() (YourAgentReserveResult, error) {
	env, err := a.yourAgentEnvFor()
	if err != nil {
		return YourAgentReserveResult{}, err
	}
	return yourAgentResend(context.Background(), env)
}

// ChooseYourAgentPage answers "already has a page": keep the email's page, or
// switch the request to the new name.
func (a *App) ChooseYourAgentPage(keep bool) (YourAgentReserveResult, error) {
	env, err := a.yourAgentEnvFor()
	if err != nil {
		return YourAgentReserveResult{}, err
	}
	return yourAgentChoose(context.Background(), env, keep)
}

// CancelYourAgentRequest lets a pending request or a choice go, so the name and
// email can change. A verified original stays.
func (a *App) CancelYourAgentRequest() (YourAgentReserveResult, error) {
	env, err := a.yourAgentEnvFor()
	if err != nil {
		return YourAgentReserveResult{}, err
	}
	return yourAgentCancel(context.Background(), env)
}

// serveYourAgent answers GET /your-agent on the sealed channel.
func (bridge *mobileBridge) serveYourAgent(w http.ResponseWriter) {
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	env, err := bridge.app.yourAgentEnvFor()
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	bridge.app.yourAgentRefreshInBackground(env)
	serveYourAgentView(w, env)
}

// serveYourAgentView writes the route body for env. The caller starts the
// background refresh; this only reads the cache and the reservation.
func serveYourAgentView(w http.ResponseWriter, env yourAgentEnv) {
	view, err := yourAgentRoute(context.Background(), env)
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	mobileJSON(w, http.StatusOK, view, "")
}
