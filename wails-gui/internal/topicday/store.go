package topicday

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const (
	copyFile     = "topics.json"
	feedbackFile = "feedback.jsonl"
)

// Store keeps the saved copy and the feedback log in one folder. Writes are
// atomic, so a reader never sees half a copy.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore returns a store over dir; the folder is made on the first write.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Load returns the saved copy, and false when there is none or it cannot be read.
func (s *Store) Load() (Copy, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) load() (Copy, bool) {
	raw, err := os.ReadFile(filepath.Join(s.dir, copyFile))
	if err != nil {
		return Copy{}, false
	}
	var c Copy
	if json.Unmarshal(raw, &c) != nil || c.Version != CopyVersion {
		return Copy{}, false
	}
	return c, true
}

// Save replaces the saved copy.
func (s *Store) Save(c Copy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(c)
}

func (s *Store) save(c Copy) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, copyFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ErrNotFound is returned when a topic or its proposal is not in the copy.
var ErrNotFound = errors.New("topic proposal not found")

// UpdateProposal changes one topic's proposal in the saved copy under the
// store's lock and returns the topic as saved. proposalID must match, so an
// answer to yesterday's proposal never changes today's.
func (s *Store) UpdateProposal(topicID, proposalID string, change func(*Topic, *Proposal) error) (Topic, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.load()
	if !ok {
		return Topic{}, ErrNotFound
	}
	for i := range c.Topics {
		t := &c.Topics[i]
		if t.ID != topicID || t.Proposal == nil || t.Proposal.ID != proposalID {
			continue
		}
		if err := change(t, t.Proposal); err != nil {
			return Topic{}, err
		}
		if err := s.save(c); err != nil {
			return Topic{}, err
		}
		return *t, nil
	}
	return Topic{}, ErrNotFound
}

// UpdateByTask changes the proposal whose task is taskID; false when none is.
func (s *Store) UpdateByTask(taskID string, change func(*Proposal) bool) (bool, error) {
	if taskID == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.load()
	if !ok {
		return false, nil
	}
	for i := range c.Topics {
		p := c.Topics[i].Proposal
		if p == nil || p.TaskID != taskID {
			continue
		}
		if !change(p) {
			return false, nil
		}
		return true, s.save(c)
	}
	return false, nil
}

// AppendFeedback adds one answer to the log.
func (s *Store) AppendFeedback(f Feedback) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(s.dir, feedbackFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// Feedback returns every answer, oldest first. Unreadable lines are skipped.
func (s *Store) Feedback() ([]Feedback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(filepath.Join(s.dir, feedbackFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var out []Feedback
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		var f Feedback
		if json.Unmarshal(scanner.Bytes(), &f) == nil && f.TopicID != "" {
			out = append(out, f)
		}
	}
	return out, scanner.Err()
}

// NotMine returns the topics the person said are not theirs, by ID and
// lowercased name, for Choices.Forgotten.
func NotMine(feedback []Feedback) map[string]bool {
	out := map[string]bool{}
	for _, f := range feedback {
		if f.Verdict == VerdictNotMine {
			out[f.TopicID] = true
			if n := normalize(f.TopicName); n != "" {
				out[n] = true
			}
		}
	}
	return out
}

// KeepFeedback writes the answer log again with what keep returns for each
// answer, dropping those it says false for (Your data's Delete your answers
// keeps Not mine, which keeps a topic the person said isn't theirs away).
func (s *Store) KeepFeedback(keep func(Feedback) (Feedback, bool)) error {
	all, err := s.Feedback()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var buf []byte
	for _, f := range all {
		f, ok := keep(f)
		if !ok {
			continue
		}
		raw, err := json.Marshal(f)
		if err != nil {
			return err
		}
		buf = append(append(buf, raw...), '\n')
	}
	path := filepath.Join(s.dir, feedbackFile)
	if len(all) == 0 {
		return nil
	}
	if err := os.WriteFile(path+".tmp", buf, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
