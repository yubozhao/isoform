package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// One card per idea by meaning (design review v1.0.8 W40, Y33; v1.0.9 R24).
// Titles are compared with the memory store's embedder when the app has one.
// Embedding is slow on a busy machine, so nothing here blocks a page: the
// binding answers from memory or the disk cache at once, and while titles are
// still being embedded it answers pending. The page keeps its word matcher until
// groups arrive. Vectors are cached on disk per embedding model, so a restart
// does not embed them again.

// Thresholds are the cosine similarity at which a title joins the group whose
// first (newest) title it matches. Know-how (How you do things) uses 0.65: on
// the user's own titles, the date-sensitive test cluster and the test-timeout
// and not-ready-store clusters fold, and the groups sampled looked like one
// idea each. Below 0.6 unrelated titles start to join. Beliefs use 0.75.
const (
	ideaMeaningThresholdKnowHow = 0.65
	ideaMeaningThresholdBeliefs = 0.75
)

// ideaMeaningBatch is how many titles one embedder call takes. Batches sleep
// between calls so the embedder never holds the CPU for long.
const (
	ideaMeaningBatch     = 32
	ideaMeaningPause     = 100 * time.Millisecond
	ideaMeaningMaxTitles = 6000
	ideaMeaningMaxResult = 8
)

// IdeaMeaningResult is what the page reads: groups aligned with the titles it
// sent, or pending while the app is still embedding them.
type IdeaMeaningResult struct {
	Groups  []int `json:"groups"`
	Pending bool  `json:"pending"`
}

type ideaMeaningStore struct {
	mu       sync.Mutex
	loaded   bool
	file     string
	vectors  map[[sha256.Size]byte][]float64
	results  map[[sha256.Size]byte][]int
	running  bool
	disabled bool
}

// ideaMeaningCacheRoot is where the vectors live; tests point it elsewhere.
var ideaMeaningCacheRoot = func() (string, error) {
	root, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "cache", "idea-meaning"), nil
}

var ideaMeanings = &ideaMeaningStore{vectors: map[[sha256.Size]byte][]float64{}, results: map[[sha256.Size]byte][]int{}}

var unsafeModelName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// IdeaMeaningGroups returns the meaning groups for the titles, newest first,
// joining titles at the given cosine threshold (0.5 to 0.95).
// It never embeds on the caller's time: it answers from memory, returns
// pending while titles are embedded in the background, and returns nil when the
// memory store has no embedder.
func (a *App) IdeaMeaningGroups(titles []string, threshold float64) (*IdeaMeaningResult, error) {
	if a == nil || len(titles) == 0 || len(titles) > ideaMeaningMaxTitles || threshold < 0.5 || threshold > 0.95 {
		return nil, nil
	}
	a.mu.Lock()
	store := a.memoryStore
	a.mu.Unlock()
	embedder := store.onDeviceEmbedder()
	if embedder == nil {
		return nil, nil
	}
	return ideaMeanings.lookup(embedder, titles, threshold, func() {
		a.emitBrowserEvent(ideaMeaningsReadyEvent)
	}), nil
}

// ideaMeaningsReadyEvent tells the pages that groups finished building, so they
// re-read once. Nothing re-reads on a timer.
const ideaMeaningsReadyEvent = "idea-meanings:ready"

func (s *ideaMeaningStore) lookup(embedder EmbeddingProvider, titles []string, threshold float64, ready func()) *IdeaMeaningResult {
	hashes := make([][sha256.Size]byte, len(titles))
	key := sha256.New()
	binary.Write(key, binary.LittleEndian, math.Float64bits(threshold))
	for i, title := range titles {
		hashes[i] = sha256.Sum256([]byte(title))
		key.Write(hashes[i][:])
	}
	var resultKey [sha256.Size]byte
	copy(resultKey[:], key.Sum(nil))

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded(embedder)
	if groups, ok := s.results[resultKey]; ok {
		return &IdeaMeaningResult{Groups: groups}
	}
	if !s.running {
		s.running = true
		go s.build(embedder, titles, hashes, resultKey, threshold, ready)
	}
	return &IdeaMeaningResult{Pending: true}
}

// build embeds the titles it lacks, in low-priority batches, then clusters.
func (s *ideaMeaningStore) build(embedder EmbeddingProvider, titles []string, hashes [][sha256.Size]byte, resultKey [sha256.Size]byte, threshold float64, ready func()) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	var missing []int
	s.mu.Lock()
	for i, h := range hashes {
		if _, ok := s.vectors[h]; !ok {
			missing = append(missing, i)
		}
	}
	s.mu.Unlock()
	for start := 0; start < len(missing); start += ideaMeaningBatch {
		end := min(start+ideaMeaningBatch, len(missing))
		texts := make([]string, 0, end-start)
		for _, i := range missing[start:end] {
			texts = append(texts, titles[i])
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		vecs, err := embedder.EmbedBatch(ctx, texts)
		cancel()
		if err != nil || len(vecs) != len(texts) {
			return
		}
		s.mu.Lock()
		for k, i := range missing[start:end] {
			unit := normalizeVector(vecs[k])
			s.vectors[hashes[i]] = unit
			s.appendLocked(hashes[i], unit)
		}
		s.mu.Unlock()
		time.Sleep(ideaMeaningPause)
	}
	s.mu.Lock()
	vectors := make([][]float64, len(hashes))
	allEmbedded := true
	for i, h := range hashes {
		vec, ok := s.vectors[h]
		if !ok {
			allEmbedded = false
			break
		}
		vectors[i] = vec
	}
	s.mu.Unlock()
	if !allEmbedded {
		return
	}
	groups := clusterIdeaMeanings(vectors, threshold)
	s.mu.Lock()
	if len(s.results) >= ideaMeaningMaxResult {
		clear(s.results)
	}
	s.results[resultKey] = groups
	s.mu.Unlock()
	if ready != nil {
		ready()
	}
}

// ensureLoaded reads the vectors for this embedding model from disk once.
// Caller holds s.mu.
func (s *ideaMeaningStore) ensureLoaded(embedder EmbeddingProvider) {
	if s.loaded || s.disabled {
		return
	}
	s.loaded = true
	root, err := ideaMeaningCacheRoot()
	if err != nil {
		s.disabled = true
		return
	}
	model := unsafeModelName.ReplaceAllString(embedder.Provider()+"-"+embedder.Model(), "_")
	if err := os.MkdirAll(root, 0o755); err != nil {
		s.disabled = true
		return
	}
	s.file = filepath.Join(root, "vectors-"+model+".bin")
	data, err := os.ReadFile(s.file)
	if err != nil {
		return
	}
	// Each record: a 32-byte title hash, a uint16 length, then that many float32 values.
	for len(data) >= sha256.Size+2 {
		var h [sha256.Size]byte
		copy(h[:], data[:sha256.Size])
		n := int(binary.LittleEndian.Uint16(data[sha256.Size:]))
		data = data[sha256.Size+2:]
		if len(data) < 4*n {
			break
		}
		vec := make([]float64, n)
		for i := range vec {
			vec[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(data[4*i:])))
		}
		s.vectors[h] = vec
		data = data[4*n:]
	}
}

// appendLocked writes one vector to the disk cache. Caller holds s.mu. A failed
// write only costs a new embedding next time.
func (s *ideaMeaningStore) appendLocked(h [sha256.Size]byte, vec []float64) {
	if s.file == "" || len(vec) > math.MaxUint16 {
		return
	}
	record := make([]byte, 0, sha256.Size+2+4*len(vec))
	record = append(record, h[:]...)
	record = binary.LittleEndian.AppendUint16(record, uint16(len(vec)))
	for _, x := range vec {
		record = binary.LittleEndian.AppendUint32(record, math.Float32bits(float32(x)))
	}
	f, err := os.OpenFile(s.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(record)
	_ = f.Close()
}

func normalizeVector(v []float64) []float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	if sum == 0 {
		return v
	}
	scale := 1 / math.Sqrt(sum)
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x * scale
	}
	return out
}

// clusterIdeaMeanings assigns each unit vector to the first group whose head it
// matches, else starts a group. The cosine of two unit vectors is their dot product.
func clusterIdeaMeanings(vectors [][]float64, threshold float64) []int {
	out := make([]int, len(vectors))
	var heads []int
	for i, v := range vectors {
		group := -1
		for g, h := range heads {
			if dot(vectors[h], v) >= threshold {
				group = g
				break
			}
		}
		if group < 0 {
			heads = append(heads, i)
			group = len(heads) - 1
		}
		out[i] = group
	}
	return out
}

func dot(a, b []float64) float64 {
	var sum float64
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}
