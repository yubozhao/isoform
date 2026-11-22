package pii

import (
	"fmt"
	"log"
	"math"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"wails-gui/internal/onnxdl"
	"wails-gui/internal/platform/ocr"
)

const (
	glinerMaxWidth           = 12 // maximum span width in words (from gliner_config)
	glinerONNXIntraOpThreads = 2
	glinerONNXInterOpThreads = 1
	glinerONNXCPUMemArena    = false
	glinerONNXMemPattern     = false
)

// defaultEntityTypes lists the PII entity types sent to GLiNER.
// GLiNER is zero-shot: these labels can be freely changed without retraining.
var defaultEntityTypes = []string{
	"credit_card",
	"cvv",
	"password",
	"one_time_password",
	"street_address",
	"api_key",
}

// glinerDetector implements Detector using the GLiNER ONNX model.
type glinerDetector struct {
	session      *ort.DynamicAdvancedSession
	tokenizer    *unigramTokenizer
	entityTypes  []string
	entityTokens [][]int // pre-tokenized entity type names
	mu           sync.Mutex
	// unhealthy flips to true after the first inference error. Once set, we
	// stop calling session.Run — repeated failures inside ONNX Runtime can
	// corrupt internal state and crash the process natively, so we'd rather
	// disable PII masking than risk a SIGSEGV.
	unhealthy bool
}

// NewDetector initializes the GLiNER-based PII detector.
// configDir is the base config directory (e.g. ~/.yansu-agent).
// onProgress is an optional callback for download progress.
func NewDetector(configDir string, onProgress ProgressFn) (Detector, error) {
	if det, handled, err := newExternalDetector(configDir); handled {
		return det, err
	}
	return newLocalDetector(configDir, onProgress)
}

func newLocalDetector(configDir string, onProgress ProgressFn) (Detector, error) {
	paths, err := ensurePIIModel(configDir, onProgress)
	if err != nil {
		return nil, fmt.Errorf("ensure pii model: %w", err)
	}

	if err := onnxdl.InitializeRuntime(paths.RuntimeLib); err != nil {
		return nil, fmt.Errorf("init onnx runtime: %w", err)
	}

	tok, err := newUnigramTokenizer(paths.TokenizerFile)
	if err != nil {
		return nil, fmt.Errorf("load tokenizer: %w", err)
	}

	options, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("create onnx session options: %w", err)
	}
	defer options.Destroy()

	if err := configureGLiNERSessionOptions(options); err != nil {
		return nil, err
	}

	session, err := ort.NewDynamicAdvancedSession(
		paths.ModelFile,
		[]string{"input_ids", "attention_mask", "words_mask", "text_lengths", "span_idx", "span_mask"},
		[]string{"logits"},
		options,
	)
	if err != nil {
		return nil, fmt.Errorf("create onnx session: %w", err)
	}

	d := &glinerDetector{
		session:     session,
		tokenizer:   tok,
		entityTypes: defaultEntityTypes,
	}

	// Pre-tokenize entity type names.
	d.entityTokens = make([][]int, len(d.entityTypes))
	for i, ent := range d.entityTypes {
		d.entityTokens[i] = tok.Encode(ent)
	}

	// Verify the model can actually run before we hand it to callers. If the
	// ONNX export uses ops the local runtime can't handle, the failure shows
	// up here as a clean Go error instead of a SIGSEGV days later.
	if err := d.smokeTest(); err != nil {
		d.session.Destroy()
		d.session = nil
		return nil, fmt.Errorf("model smoke test: %w", err)
	}

	log.Printf("[pii] GLiNER detector ready (entities=%d, intra_threads=%d, inter_threads=%d, cpu_arena=%t, mem_pattern=%t)",
		len(d.entityTypes), glinerONNXIntraOpThreads, glinerONNXInterOpThreads, glinerONNXCPUMemArena, glinerONNXMemPattern)
	return d, nil
}

type glinerSessionOptions interface {
	SetIntraOpNumThreads(int) error
	SetInterOpNumThreads(int) error
	SetExecutionMode(ort.ExecutionMode) error
	SetCpuMemArena(bool) error
	SetMemPattern(bool) error
}

func configureGLiNERSessionOptions(options glinerSessionOptions) error {
	// Do not remove these limits. ORT's default intra-op thread count can be
	// the physical core count; on Windows fp32 GLiNER that saturated all cores
	// and made activity PII p95 climb into tens of seconds. The CPU arena and
	// memory pattern are also disabled because the background PII path has
	// dynamic input sizes; retaining peak native inference buffers inflated
	// process RSS far beyond Go heap usage.
	if err := options.SetIntraOpNumThreads(glinerONNXIntraOpThreads); err != nil {
		return fmt.Errorf("set onnx intra-op threads: %w", err)
	}
	if err := options.SetInterOpNumThreads(glinerONNXInterOpThreads); err != nil {
		return fmt.Errorf("set onnx inter-op threads: %w", err)
	}
	if err := options.SetExecutionMode(ort.ExecutionModeSequential); err != nil {
		return fmt.Errorf("set onnx execution mode: %w", err)
	}
	if err := options.SetCpuMemArena(glinerONNXCPUMemArena); err != nil {
		return fmt.Errorf("set onnx cpu memory arena: %w", err)
	}
	if err := options.SetMemPattern(glinerONNXMemPattern); err != nil {
		return fmt.Errorf("set onnx memory pattern: %w", err)
	}
	return nil
}

// smokeTest runs a minimal inference on a single dummy word so any model /
// runtime incompatibility surfaces before the detector is wired into the
// activity pipeline.
func (d *glinerDetector) smokeTest() error {
	dummy := []wordSpan{{Word: "test", Start: 0, End: 4}}
	prevUnhealthy := d.unhealthy
	defer func() { d.unhealthy = prevUnhealthy }()
	_ = d.infer(dummy)
	if d.unhealthy {
		return fmt.Errorf("inference returned an error on dummy input")
	}
	return nil
}

func (d *glinerDetector) IsReady() bool {
	return d.session != nil && !d.unhealthy
}

func (d *glinerDetector) Destroy() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.session != nil {
		d.session.Destroy()
		d.session = nil
	}
}

// Detect runs the GLiNER PII model on each OCR observation.
// All observation texts are concatenated, processed in a single inference call,
// and detected spans are mapped back to observation bounding boxes.
func (d *glinerDetector) Detect(observations []ocr.TextObservation) []DetectionResult {
	if len(observations) == 0 {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.session == nil || d.unhealthy {
		return nil
	}

	// Concatenate all observation texts into one string for batch processing.
	// Track which observation each word belongs to.
	var fullText strings.Builder
	type obsRange struct {
		charStart int
		charEnd   int
	}
	obsRanges := make([]obsRange, len(observations))
	offset := 0
	for i, obs := range observations {
		text := strings.TrimSpace(obs.Text)
		if i > 0 {
			fullText.WriteRune(' ')
			offset++
		}
		obsRanges[i] = obsRange{charStart: offset, charEnd: offset + len(text)}
		fullText.WriteString(text)
		offset += len(text)
	}

	// Split into words.
	text := fullText.String()
	words := splitWords(text)
	if len(words) == 0 {
		return nil
	}

	// Map each word to its source observation by character offset.
	wordToObs := make([]int, len(words))
	for i, w := range words {
		for oi := range observations {
			if w.Start >= obsRanges[oi].charStart && w.Start < obsRanges[oi].charEnd {
				wordToObs[i] = oi
				break
			}
		}
	}

	// Run GLiNER inference.
	spans := d.infer(words)
	if len(spans) == 0 {
		return nil
	}

	// Map detected spans back to observation bounding boxes.
	var results []DetectionResult
	for _, sp := range spans {
		results = append(results, spanRegions(observations, wordToObs[sp.startWord], wordToObs[sp.endWord], sp)...)
	}

	return results
}

// spanRegions returns the regions to mask for a span covering observations
// obsStart..obsEnd: their outer bounding box when it is the size of real PII,
// otherwise each observation's own box. A span whose observations are
// scattered across the screen (OCR reading-order artifact, common on chat or
// tiled UIs) would mask most of the window as one rectangle; masking the
// lines it covers keeps the PII hidden without blanking the window. A line
// that is itself over the mask cap makes MaskRegions fail, so the frame is
// dropped rather than written with that line visible.
func spanRegions(observations []ocr.TextObservation, obsStart, obsEnd int, sp detectedSpan) []DetectionResult {
	minX, minY := math.MaxFloat64, math.MaxFloat64
	maxX, maxY := 0.0, 0.0
	for oi := obsStart; oi <= obsEnd; oi++ {
		obs := observations[oi]
		if obs.X < minX {
			minX = obs.X
		}
		if obs.Y < minY {
			minY = obs.Y
		}
		if obs.X+obs.Width > maxX {
			maxX = obs.X + obs.Width
		}
		if obs.Y+obs.Height > maxY {
			maxY = obs.Y + obs.Height
		}
	}

	if spanBBoxCoherent(maxX-minX, maxY-minY) {
		return []DetectionResult{{
			Text:       sp.text,
			Category:   sp.entityType,
			X:          minX,
			Y:          minY,
			Width:      maxX - minX,
			Height:     maxY - minY,
			Confidence: sp.score,
		}}
	}

	log.Printf("[pii] GLiNER span covers scattered observations (bbox=%.2fx%.2f entity=%s score=%.2f); masking its %d line(s) one by one",
		maxX-minX, maxY-minY, sp.entityType, sp.score, obsEnd-obsStart+1)
	regions := make([]DetectionResult, 0, obsEnd-obsStart+1)
	for oi := obsStart; oi <= obsEnd; oi++ {
		obs := observations[oi]
		regions = append(regions, DetectionResult{
			Text:       sp.text,
			Category:   sp.entityType,
			X:          obs.X,
			Y:          obs.Y,
			Width:      obs.Width,
			Height:     obs.Height,
			Confidence: sp.score,
		})
	}
	return regions
}

type detectedSpan struct {
	startWord  int
	endWord    int
	text       string
	entityType string
	score      float64
}

// infer runs the GLiNER ONNX model on the given words.
func (d *glinerDetector) infer(words []wordSpan) []detectedSpan {
	numWords := len(words)
	if numWords == 0 {
		return nil
	}

	tok := d.tokenizer

	// 1. Build input token sequence:
	//    [CLS] <<ENT>> ent1_toks <<ENT>> ent2_toks ... <<ENT>> entN_toks <<SEP>> word_toks... [SEP]
	var inputIDs []int64
	inputIDs = append(inputIDs, int64(tok.clsID))

	for _, entToks := range d.entityTokens {
		inputIDs = append(inputIDs, int64(tok.entID))
		for _, id := range entToks {
			inputIDs = append(inputIDs, int64(id))
		}
	}
	inputIDs = append(inputIDs, int64(tok.esepID))

	// 2. Tokenize each word and build words_mask.
	entitySectionLen := len(inputIDs)
	wordsMask := make([]int64, entitySectionLen) // 0 for entity section

	wordIdx := int64(1) // 1-indexed
	for _, w := range words {
		wordToks := tok.EncodeWord(w.Word)
		if len(wordToks) == 0 {
			wordToks = []int{tok.unkID}
		}

		// First subtoken gets the word index; rest get 0.
		wordsMask = append(wordsMask, wordIdx)
		inputIDs = append(inputIDs, int64(wordToks[0]))

		for _, id := range wordToks[1:] {
			wordsMask = append(wordsMask, 0)
			inputIDs = append(inputIDs, int64(id))
		}
		wordIdx++
	}

	inputIDs = append(inputIDs, int64(tok.sepID))
	wordsMask = append(wordsMask, 0)

	seqLen := int64(len(inputIDs))

	// 3. Attention mask (all 1s for actual tokens).
	attentionMask := make([]int64, seqLen)
	for i := range attentionMask {
		attentionMask[i] = 1
	}

	// 4. text_lengths.
	textLengths := []int64{int64(numWords)}

	// 5. Span grid: numWords * maxWidth spans.
	numSpans := numWords * glinerMaxWidth
	spanIdx := make([]int64, numSpans*2)
	spanMask := make([]bool, numSpans)

	for i := 0; i < numWords; i++ {
		for w := 0; w < glinerMaxWidth; w++ {
			idx := i*glinerMaxWidth + w
			j := i + w
			spanIdx[idx*2] = int64(i)
			spanIdx[idx*2+1] = int64(j)
			if j < numWords {
				spanMask[idx] = true
			}
		}
	}

	// 6. Create tensors and run inference.
	inputIDsTensor, err := ort.NewTensor(ort.NewShape(1, seqLen), inputIDs)
	if err != nil {
		log.Printf("[pii] input_ids tensor: %v", err)
		d.unhealthy = true
		return nil
	}
	defer inputIDsTensor.Destroy()

	attMaskTensor, err := ort.NewTensor(ort.NewShape(1, seqLen), attentionMask)
	if err != nil {
		log.Printf("[pii] attention_mask tensor: %v", err)
		d.unhealthy = true
		return nil
	}
	defer attMaskTensor.Destroy()

	wordsMaskTensor, err := ort.NewTensor(ort.NewShape(1, seqLen), wordsMask)
	if err != nil {
		log.Printf("[pii] words_mask tensor: %v", err)
		d.unhealthy = true
		return nil
	}
	defer wordsMaskTensor.Destroy()

	textLenTensor, err := ort.NewTensor(ort.NewShape(1, 1), textLengths)
	if err != nil {
		log.Printf("[pii] text_lengths tensor: %v", err)
		d.unhealthy = true
		return nil
	}
	defer textLenTensor.Destroy()

	spanIdxTensor, err := ort.NewTensor(ort.NewShape(1, int64(numSpans), 2), spanIdx)
	if err != nil {
		log.Printf("[pii] span_idx tensor: %v", err)
		d.unhealthy = true
		return nil
	}
	defer spanIdxTensor.Destroy()

	spanMaskTensor, err := ort.NewTensor(ort.NewShape(1, int64(numSpans)), spanMask)
	if err != nil {
		log.Printf("[pii] span_mask tensor: %v", err)
		d.unhealthy = true
		return nil
	}
	defer spanMaskTensor.Destroy()

	// Output shape: (1, numWords, maxWidth, numEntityTypes)
	numEntities := int64(len(d.entityTypes))
	outputShape := ort.NewShape(1, int64(numWords), int64(glinerMaxWidth), numEntities)
	outputTensor, err := ort.NewEmptyTensor[float32](outputShape)
	if err != nil {
		log.Printf("[pii] output tensor: %v", err)
		d.unhealthy = true
		return nil
	}
	defer outputTensor.Destroy()

	releaseInfer := onnxdl.AcquireInference()
	err = d.session.Run(
		[]ort.Value{inputIDsTensor, attMaskTensor, wordsMaskTensor, textLenTensor, spanIdxTensor, spanMaskTensor},
		[]ort.Value{outputTensor},
	)
	releaseInfer()
	if err != nil {
		log.Printf("[pii] inference error (disabling detector): %v", err)
		d.unhealthy = true
		return nil
	}

	// 7. Decode output: apply sigmoid, threshold, and greedy NMS.
	logits := outputTensor.GetData()
	return d.decodeSpans(logits, words, numWords, int(numEntities))
}

const (
	detectionThreshold = 0.35

	// Spatial sanity bounds for a GLiNER span's outer bbox. Real PII fits on
	// 1–3 text lines; when a span maps to observations scattered across the
	// screen (OCR reading-order artifact, common on chat / tiled UIs), the
	// union rectangle blows up and would mask most of the window. Mask the
	// covered lines one by one instead (spanRegions).
	maxSpanHeightFrac = 0.08
	maxSpanWidthFrac  = 0.6
)

// decodeSpans extracts detected entity spans from model output logits.
func (d *glinerDetector) decodeSpans(logits []float32, words []wordSpan, numWords, numEntities int) []detectedSpan {
	// logits layout: [word_i][width_w][entity_c]
	stride2 := numEntities
	stride1 := glinerMaxWidth * stride2

	type candidate struct {
		startWord int
		endWord   int
		entityIdx int
		score     float64
	}

	var candidates []candidate

	for i := 0; i < numWords; i++ {
		for w := 0; w < glinerMaxWidth; w++ {
			j := i + w
			if j >= numWords {
				break
			}
			for c := 0; c < numEntities; c++ {
				logit := logits[i*stride1+w*stride2+c]
				prob := sigmoid(float64(logit))
				if prob >= detectionThreshold {
					candidates = append(candidates, candidate{
						startWord: i,
						endWord:   j,
						entityIdx: c,
						score:     prob,
					})
				}
			}
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	// Sort by score descending (insertion sort for typically small N).
	for i := 1; i < len(candidates); i++ {
		key := candidates[i]
		j := i - 1
		for j >= 0 && candidates[j].score < key.score {
			candidates[j+1] = candidates[j]
			j--
		}
		candidates[j+1] = key
	}

	// Greedy NMS: suppress overlapping spans.
	used := make([]bool, len(candidates))
	var results []detectedSpan

	for i, c := range candidates {
		if used[i] {
			continue
		}

		var spanWords []string
		for wi := c.startWord; wi <= c.endWord; wi++ {
			spanWords = append(spanWords, words[wi].Word)
		}

		results = append(results, detectedSpan{
			startWord:  c.startWord,
			endWord:    c.endWord,
			text:       strings.Join(spanWords, " "),
			entityType: d.entityTypes[c.entityIdx],
			score:      c.score,
		})

		// Suppress overlapping candidates.
		for j := i + 1; j < len(candidates); j++ {
			if !used[j] && candidates[j].startWord <= c.endWord && c.startWord <= candidates[j].endWord {
				used[j] = true
			}
		}
	}

	return results
}

func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}

// spanBBoxCoherent rejects spans whose outer bbox is implausibly large for
// real PII. See maxSpanHeightFrac / maxSpanWidthFrac for the rationale.
func spanBBoxCoherent(width, height float64) bool {
	return width <= maxSpanWidthFrac && height <= maxSpanHeightFrac
}
