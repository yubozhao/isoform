package models

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

type ChoiceEntry struct {
	Entry
	Downloaded bool `json:"downloaded"`
}
type JobChoices struct {
	Job          string        `json:"job"`
	ChosenID     string        `json:"chosenId"`
	Alternatives []ChoiceEntry `json:"alternatives"`
	Locked       bool          `json:"locked"`
}
type Choices struct {
	Jobs                 []JobChoices `json:"jobs"`
	TotalBytesToDownload int64        `json:"totalBytesToDownload"`
	FreeBytes            int64        `json:"freeBytes"`
	ModelsFolder         string       `json:"modelsFolder"`
}
type PlanItem struct {
	ID            string  `json:"id"`
	Bytes         int64   `json:"bytes"`
	FileCount     int     `json:"fileCount"`
	Source        Source  `json:"source"`
	Host          string  `json:"host"`
	Licence       Licence `json:"licence"`
	NonCommercial bool    `json:"nonCommercial"`
}
type Plan struct {
	Choices       map[string]string `json:"choices"`
	PlanID        string            `json:"planId"`
	Items         []PlanItem        `json:"items"`
	RequiredBytes int64             `json:"requiredBytes"`
	TotalBytes    int64             `json:"totalBytes"`
	FreeBytes     int64             `json:"freeBytes"`
	EnoughSpace   bool              `json:"enoughSpace"`
	ModelsFolder  string            `json:"modelsFolder"`
}
type Download struct {
	PauseReason string `json:"pauseReason"`
	ID          string `json:"id"`
	State       string `json:"state"`
	BytesDone   int64  `json:"bytesDone"`
	BytesTotal  int64  `json:"bytesTotal"`
	Error       string `json:"error"`
	// Deferred: confirmed with Later; it waits until the computer is on
	// power and idle (ReleaseDeferred) or the person resumes it.
	Deferred bool `json:"deferred,omitempty"`
}
type savedJob struct {
	Download
	Source      Source `json:"source"`
	Fingerprint string `json:"fingerprint"`
}
type savedState struct {
	LastAfterCall string            `json:"lastAfterCall,omitempty"`
	Choices       map[string]string `json:"choices"`
	Jobs          []savedJob        `json:"jobs"`
}

type Manager struct {
	networkProbe   func() NetworkState
	monitorOnce    sync.Once
	lastAfterCall  string
	reserved       map[string]int64
	network        NetworkState
	jobCancels     map[string]context.CancelFunc
	jobDone        map[string]chan struct{}
	mu             sync.Mutex
	folder, legacy string
	entries        []Entry
	choices        map[string]string
	jobs           map[string]*savedJob
	order          []string
	plans          map[string]Plan
	running        map[string]bool
	batch          map[string]bool
	free           func(string) (int64, error)
	emit           func([]Download)
	lastEmit       time.Time
	pending        *time.Timer
	ctx            context.Context
	cancel         context.CancelFunc
}

func New(folder, legacy string, entries []Entry, free func(string) (int64, error), emit func([]Download)) (*Manager, error) {
	if err := Validate(entries); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{reserved: map[string]int64{}, network: NetworkState{Known: true}, jobCancels: map[string]context.CancelFunc{}, jobDone: map[string]chan struct{}{}, folder: folder, legacy: legacy, entries: entries, free: free, emit: emit, choices: map[string]string{}, jobs: map[string]*savedJob{}, plans: map[string]Plan{}, running: map[string]bool{}, batch: map[string]bool{}, ctx: ctx, cancel: cancel}
	for _, e := range entries {
		if e.Recommended {
			m.choices[e.Job] = e.ID
		}
	}
	raw, err := os.ReadFile(m.statePath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		cancel()
		return nil, err
	}
	if len(raw) > 0 {
		var saved savedState
		if err := json.Unmarshal(raw, &saved); err != nil {
			cancel()
			return nil, err
		}
		m.lastAfterCall = saved.LastAfterCall
		for job, id := range saved.Choices {
			if e, ok := m.entry(id); ok && e.Job == job {
				m.choices[job] = id
			}
		}
		for _, j := range saved.Jobs {
			e, ok := m.entry(j.ID)
			if !ok || fingerprint(e) != j.Fingerprint {
				continue
			}
			copy := j
			if j.State == "downloading" {
				copy.State = "queued"
			}
			m.jobs[j.ID] = &copy
			m.order = append(m.order, j.ID)
		}
	}
	return m, nil
}

// Stop cancels immediately. A caller can join the returned channel within its
// shared shutdown budget without delaying critical persistence on disk IO.
func (m *Manager) Stop() <-chan struct{} {
	m.cancel()
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	return done
}
func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	if m.pending != nil {
		m.pending.Stop()
	}
	done := make([]chan struct{}, 0, len(m.jobDone))
	for _, ch := range m.jobDone {
		done = append(done, ch)
	}
	m.mu.Unlock()
	for _, ch := range done {
		<-ch
	}
}
func (m *Manager) statePath() string { return filepath.Join(m.folder, ".setup-models.json") }
func (m *Manager) entry(id string) (Entry, bool) {
	for _, e := range m.entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}
func fingerprint(e Entry) string { raw, _ := json.Marshal(e); sum := sha256Bytes(raw); return sum }
func (m *Manager) saveLocked() error {
	s := savedState{LastAfterCall: m.lastAfterCall, Choices: m.choices, Jobs: []savedJob{}}
	for _, id := range m.order {
		s.Jobs = append(s.Jobs, *m.jobs[id])
	}
	return writeJSON(m.statePath(), s)
}
func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "    ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".setup-state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
func (m *Manager) GetChoices() (Choices, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := Choices{Jobs: []JobChoices{}, ModelsFolder: m.folder}
	free, err := m.free(m.folder)
	if err != nil {
		return result, err
	}
	result.FreeBytes = free
	for _, job := range []string{"live", "afterCall", "voice", "privacy", "memory", "speakerPack"} {
		c := JobChoices{Job: job, ChosenID: m.choices[job], Alternatives: []ChoiceEntry{}, Locked: job == "privacy" || job == "memory" || job == "speakerPack"}
		for _, e := range m.entries {
			if e.Job != job {
				continue
			}
			p := presentAt(m.folder, m.legacy, e)
			c.Alternatives = append(c.Alternatives, ChoiceEntry{e, p})
			if e.ID == c.ChosenID && !p && e.SizeBytes != nil {
				result.TotalBytesToDownload += *e.SizeBytes
			}
		}
		result.Jobs = append(result.Jobs, c)
	}
	return result, nil
}
func (m *Manager) SetChoice(job, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entry(id)
	if !ok || e.Job != job {
		return errors.New("model does not serve this job")
	}
	if (job == "privacy" || job == "memory" || job == "speakerPack") && m.choices[job] != id {
		return errors.New("job choice is locked")
	}
	old := m.choices[job]
	previousAfterCall := m.lastAfterCall
	if job == "afterCall" && old != "none" {
		m.lastAfterCall = old
	}
	m.choices[job] = id
	if err := m.saveLocked(); err != nil {
		m.choices[job] = old
		m.lastAfterCall = previousAfterCall
		return err
	}
	return nil
}
func (m *Manager) BuildPlan(choices map[string]string, overrides map[string]string) (Plan, error) {
	return m.BuildPlanSkipping(choices, overrides, nil)
}

// BuildPlanSkipping plans the chosen models except those serving the skipped
// jobs: setup's "Runs on this computer" leaves a job's choice as it is and only
// leaves its download out.
func (m *Manager) BuildPlanSkipping(choices map[string]string, overrides map[string]string, skipJobs []string) (Plan, error) {
	skip := map[string]bool{}
	for _, job := range skipJobs {
		skip[job] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	selected := map[string]string{}
	for job, id := range m.choices {
		selected[job] = id
	}
	for job, id := range choices {
		e, ok := m.entry(id)
		if !ok || e.Job != job {
			return Plan{}, fmt.Errorf("unknown choice %s/%s", job, id)
		}
		if (job == "privacy" || job == "memory" || job == "speakerPack") && id != m.choices[job] {
			return Plan{}, errors.New("job choice is locked")
		}
		selected[job] = id
	}
	p := Plan{Choices: selected, Items: []PlanItem{}, ModelsFolder: m.folder}
	for _, e := range m.entries {
		if selected[e.Job] != e.ID || e.ID == "none" || skip[e.Job] || presentAt(m.folder, m.legacy, e) {
			continue
		}
		if e.SizeBytes == nil || len(e.Files) == 0 {
			return Plan{}, fmt.Errorf("%s has unknown size", e.ID)
		}
		for _, f := range e.Files {
			if f.SHA256 == nil || f.Bytes == nil || (f.Archive != nil && len(f.Archive.Files) == 0) {
				return Plan{}, fmt.Errorf("%s has unverified metadata", e.ID)
			}
		}
		source := defaultSource(e)
		if override := overrides[e.ID]; override != "" {
			found := false
			for _, s := range e.Sources {
				if override == s.Kind || override == s.URL {
					source = s
					found = true
					break
				}
			}
			if !found {
				u, err := url.Parse(override)
				if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
					return Plan{}, fmt.Errorf("invalid mirror for %s", e.ID)
				}
				source = Source{Kind: "mirror", URL: override}
			}
		}
		u, err := url.Parse(source.URL)
		if err != nil || u.Host == "" {
			return Plan{}, errors.New("invalid manifest source")
		}
		p.Items = append(p.Items, PlanItem{ID: e.ID, Bytes: *e.SizeBytes, FileCount: len(e.Files), Source: source, Host: planHosts(e, source), Licence: e.Licence, NonCommercial: e.Licence.NonCommercial})
		p.TotalBytes += *e.SizeBytes
	}
	free, err := m.free(m.folder)
	if err != nil {
		return Plan{}, err
	}
	p.FreeBytes = free
	p.RequiredBytes = m.requiredSpace(p)
	p.EnoughSpace = free >= p.RequiredBytes
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Plan{}, err
	}
	p.PlanID = hex.EncodeToString(nonce[:])
	raw, _ := json.Marshal(p)
	var approved Plan
	_ = json.Unmarshal(raw, &approved)
	m.plans[p.PlanID] = approved
	return p, nil
}
func (m *Manager) requiredSpace(p Plan) int64 {
	ids := make([]string, 0, len(p.Items))
	for _, item := range p.Items {
		ids = append(ids, item.ID)
	}
	return m.spaceNeeded(ids)
}
func (m *Manager) Start(p Plan) error { return m.accept(p, false) }

// Defer confirms a reviewed plan for later: its jobs are saved but none starts
// until ReleaseDeferred (power and idle) or an explicit resume. Space is
// checked when each job is admitted, so a plan that does not fit yet can wait.
func (m *Manager) Defer(p Plan) error { return m.accept(p, true) }

func (m *Manager) accept(p Plan, deferred bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	original, ok := m.plans[p.PlanID]
	if !ok || !reflect.DeepEqual(original, p) {
		return errors.New("download plan is missing or changed; review a new plan")
	}
	if !deferred {
		free, err := m.free(m.folder)
		if err != nil {
			return err
		}
		if free < m.requiredSpace(p) {
			return ErrNoSpace
		}
	}
	for _, item := range p.Items {
		if m.running[item.ID] {
			return fmt.Errorf("%s is already downloading", item.ID)
		}
	}
	previousChoices, previousLast, previousOrder := m.choices, m.lastAfterCall, append([]string(nil), m.order...)
	previousJobs := make(map[string]*savedJob, len(m.jobs))
	for id, job := range m.jobs {
		previousJobs[id] = job
	}
	if old := m.choices["afterCall"]; old != "none" {
		m.lastAfterCall = old
	}
	m.choices = map[string]string{}
	for job, id := range p.Choices {
		m.choices[job] = id
	}
	for _, item := range p.Items {
		e, _ := m.entry(item.ID)
		if _, ok := m.jobs[item.ID]; !ok {
			m.order = append(m.order, item.ID)
		}
		m.jobs[item.ID] = &savedJob{Download: Download{ID: item.ID, State: "queued", BytesTotal: item.Bytes, Deferred: deferred}, Source: item.Source, Fingerprint: fingerprint(e)}
	}
	if err := m.saveLocked(); err != nil {
		m.choices, m.lastAfterCall, m.order, m.jobs = previousChoices, previousLast, previousOrder, previousJobs
		return err
	}
	delete(m.plans, p.PlanID)
	if !deferred {
		for _, item := range p.Items {
			m.batch[item.ID] = true
			_ = m.launchLocked(item.ID)
		}
	}
	m.changedLocked()
	return nil
}
func (m *Manager) Resume() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.order {
		if j := m.jobs[id]; (j.State == "queued" || j.State == "paused") && !j.Deferred {
			m.batch[id] = true
			_ = m.launchLocked(id)
		}
	}
	_ = m.saveLocked()
	m.changedLocked()
}
func (m *Manager) Retry(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok || j.State != "failed" {
		return errors.New("no failed confirmed download")
	}
	j.State = "queued"
	j.Error = ""
	if err := m.saveLocked(); err != nil {
		return err
	}
	m.batch[id] = true
	err := m.launchLocked(id)
	if saveErr := m.saveLocked(); saveErr != nil {
		return saveErr
	}
	m.changedLocked()
	return err
}
func (m *Manager) snapshotLocked() []Download {
	out := []Download{}
	for _, id := range m.order {
		out = append(out, m.jobs[id].Download)
	}
	return out
}
func (m *Manager) Downloads() []Download { m.mu.Lock(); defer m.mu.Unlock(); return m.snapshotLocked() }

// HasDeferred reports confirmed jobs still waiting for power and idle.
func (m *Manager) HasDeferred() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.order {
		if m.jobs[id].Deferred {
			return true
		}
	}
	return false
}

// ReleaseDeferred starts the jobs confirmed with Later. It never adds a job:
// only those the person already confirmed are released.
func (m *Manager) ReleaseDeferred() {
	m.mu.Lock()
	defer m.mu.Unlock()
	released := []string{}
	for _, id := range m.order {
		if j := m.jobs[id]; j.Deferred {
			j.Deferred = false
			released = append(released, id)
			m.batch[id] = true
		}
	}
	if len(released) == 0 {
		return
	}
	_ = m.saveLocked()
	for _, id := range released {
		_ = m.launchLocked(id)
	}
	_ = m.saveLocked()
	m.changedLocked()
}

// Progress is the share of the current batch (the jobs started together,
// plus later retries) already on disk, for the menu bar's
// "Downloading models" line. Active is false once every job in the batch
// has finished or failed; the batch then starts over.
func (m *Manager) Progress() (percent int, active bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var done, total int64
	for id := range m.batch {
		j, ok := m.jobs[id]
		if !ok || j.Deferred {
			delete(m.batch, id)
			continue
		}
		switch j.State {
		case "queued", "downloading", "paused":
			active = true
		}
		done += j.BytesDone
		total += j.BytesTotal
	}
	if !active {
		m.batch = map[string]bool{}
		return 0, false
	}
	if total > 0 {
		percent = int(done * 100 / total)
	}
	return percent, true
}

// Done closes when the manager stops.
func (m *Manager) Done() <-chan struct{} { return m.ctx.Done() }
func (m *Manager) changedLocked() {
	if m.emit == nil || m.pending != nil {
		return
	}
	delay := time.Until(m.lastEmit.Add(500 * time.Millisecond))
	if delay < 0 {
		delay = 0
	}
	m.pending = time.AfterFunc(delay, func() {
		m.mu.Lock()
		s := m.snapshotLocked()
		m.lastEmit = time.Now()
		m.pending = nil
		m.mu.Unlock()
		if m.ctx.Err() == nil {
			m.emit(s)
		}
	})
}
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entry(id)
	if !ok {
		return errors.New("unknown model")
	}
	if m.running[id] {
		return errors.New("model download is in progress")
	}
	for _, f := range e.Files {
		paths := []string{filepath.Join(m.folder, f.Path), filepath.Join(m.folder, f.Path) + ".partial", filepath.Join(m.folder, f.Path) + ".partial.validator"}
		if stringsHasSherpa(f.Path) {
			paths = append(paths, filepath.Join(m.legacy, f.Path))
		}
		if f.Archive != nil {
			paths = append(paths, filepath.Join(m.folder, f.Archive.Destination), filepath.Join(m.legacy, f.Archive.Destination))
		}
		for _, p := range paths {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
	}
	delete(m.jobs, id)
	order := m.order[:0]
	for _, key := range m.order {
		if key != id {
			order = append(order, key)
		}
	}
	m.order = order
	m.changedLocked()
	return m.saveLocked()
}

// defaultSource is where a download goes unless the person picks another:
// the first upstream source (Hugging Face, GitHub). Isoform's own mirror
// (model-assets.yansu.app) stays listed first in the catalog, as the layout a
// custom mirror copies, and can still be picked by name, but it answers 403
// to every request (2026-11-15), so a new install could download no speech,
// voice or speaker model at all. The daily trial checks every default source
// answers (TestCatalogDefaultSourcesAnswer).
func defaultSource(e Entry) Source {
	for _, s := range e.Sources {
		if s.Kind != "mirror" {
			return s
		}
	}
	return e.Sources[0]
}

func planHosts(e Entry, source Source) string {
	hosts := []string{}
	seen := map[string]bool{}
	for _, f := range e.Files {
		u, err := url.Parse(fileURL(e, f, source))
		if err == nil && !seen[u.Host] {
			seen[u.Host] = true
			hosts = append(hosts, u.Host)
		}
	}
	return strings.Join(hosts, ", ")
}

// Cancel preserves the partial file for a later explicit retry. Used by the
// existing after-call disable/remove actions; it does not add another binding.
func (m *Manager) Cancel(id string) {
	m.mu.Lock()
	cancel, done := m.jobCancels[id], m.jobDone[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}
func (m *Manager) Selected(job string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.choices[job]
}

// VoiceForLanguage honors the saved choice. Only a language absent from that
// voice may fall back, and fallback never authorizes acquiring another model.
func (m *Manager) VoiceForLanguage(language string) (Entry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	chosen, _ := m.entry(m.choices["voice"])
	if coversLanguage(chosen, language) {
		return chosen, presentAt(m.folder, m.legacy, chosen)
	}
	for _, e := range m.entries {
		if e.Job == "voice" && coversLanguage(e, language) && presentAt(m.folder, m.legacy, e) {
			return e, true
		}
	}
	return Entry{}, false
}
func coversLanguage(e Entry, language string) bool {
	language = strings.ToLower(strings.Split(language, "-")[0])
	for _, supported := range e.Languages {
		if supported == language || supported == "mul" {
			return true
		}
	}
	return false
}

// SetAfterCallEnabled makes the legacy switch an editor of the same choice
// used by setup. Turning on after None restores the last non-None selection.
func (m *Manager) SetAfterCallEnabled(enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.choices["afterCall"]
	previousLast := m.lastAfterCall
	selected := "none"
	if enabled {
		selected = m.lastAfterCall
		if old != "none" && old != "" {
			selected = old
		}
		if e, ok := m.entry(selected); !ok || e.Job != "afterCall" || selected == "none" {
			for _, e := range m.entries {
				if e.Job == "afterCall" && e.Recommended {
					selected = e.ID
					break
				}
			}
		}
	} else if old != "none" {
		m.lastAfterCall = old
	}
	m.choices["afterCall"] = selected
	if err := m.saveLocked(); err != nil {
		m.choices["afterCall"] = old
		m.lastAfterCall = previousLast
		return err
	}
	return nil
}

func (m *Manager) HasSavedChoices() bool { _, err := os.Stat(m.statePath()); return err == nil }

func (m *Manager) MonitorNetwork() {
	m.monitorOnce.Do(func() {
		read := WatchNetwork(m.ctx, m.SetNetworkState)
		m.mu.Lock()
		m.networkProbe = read
		m.mu.Unlock()
	})
}
