package main

import (
	"hash/fnv"
	"math"
	"sync"
)

const (
	// dedup tuning — matches screenpipe defaults
	dedupDownscaleFactor = 6    // 1920→320, 2056→342
	dedupThreshold       = 0.02 // Hellinger distance; below this = "same frame"
)

// frameComparer detects duplicate/near-duplicate screenshots so we can skip
// saving them. Mirrors screenpipe's two-layer approach:
//
//  1. Hash the downscaled grayscale thumbnail — if identical, skip instantly.
//  2. Compute the Hellinger distance between grayscale histograms — if < threshold, skip.
type frameComparer struct {
	mu       sync.Mutex
	prevHash uint64
	prevHist [256]float64 // normalised histogram of previous frame
	hasPrev  bool
	grayBuf  []byte // reusable buffer for grayscale downscale
}

// distance returns the Hellinger distance between the current RGBA frame and
// the previous one.  Returns 1.0 for the first frame (no previous to compare)
// and 0.0 for an exact hash match.
func (fc *frameComparer) distance(rgba []byte, w, h int) float64 {
	// 1. Downscale to ~1/6 using nearest-neighbour and convert to grayscale.
	tw := w / dedupDownscaleFactor
	th := h / dedupDownscaleFactor
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}

	fc.mu.Lock()
	defer fc.mu.Unlock()

	needed := tw * th
	if cap(fc.grayBuf) < needed {
		fc.grayBuf = make([]byte, needed)
	}
	gray := fc.grayBuf[:needed]

	for y := range th {
		srcY := y * dedupDownscaleFactor
		for x := range tw {
			srcX := x * dedupDownscaleFactor
			off := (srcY*w + srcX) * 4
			r := uint32(rgba[off])
			g := uint32(rgba[off+1])
			b := uint32(rgba[off+2])
			// ITU-R BT.601 luma
			gray[y*tw+x] = byte((r*299 + g*587 + b*114) / 1000)
		}
	}

	return fc.compareGrayLocked(gray)
}

// distanceFromGray returns the Hellinger distance using pre-computed grayscale
// pixels. Used by checkVisualChange which captures at reduced resolution.
func (fc *frameComparer) distanceFromGray(gray []byte) float64 {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.compareGrayLocked(gray)
}

// compareGrayLocked is the shared hash+histogram comparison logic.
// Must be called with fc.mu held.
func (fc *frameComparer) compareGrayLocked(gray []byte) float64 {
	hasher := fnv.New64a()
	hasher.Write(gray)
	hash := hasher.Sum64()

	if !fc.hasPrev {
		fc.prevHash = hash
		fc.prevHist = buildHistogram(gray)
		fc.hasPrev = true
		return 1.0 // first frame is always "different"
	}

	// Fast path: identical bytes → skip.
	if hash == fc.prevHash {
		return 0.0
	}

	// Histogram comparison (Hellinger distance).
	hist := buildHistogram(gray)
	dist := hellingerDistance(fc.prevHist, hist)

	fc.prevHash = hash
	fc.prevHist = hist

	return dist
}

// reset clears the comparison state (e.g. on session boundary).
func (fc *frameComparer) reset() {
	fc.mu.Lock()
	fc.hasPrev = false
	fc.grayBuf = nil
	fc.mu.Unlock()
}

// bgraToGray converts raw BGRA pixel data to grayscale using BT.601 luma.
// Used for visual change detection with pre-downscaled screen captures.
func bgraToGray(bgra []byte, w, h int) []byte {
	gray := make([]byte, w*h)
	for y := range h {
		for x := range w {
			off := (y*w + x) * 4
			b := uint32(bgra[off])
			g := uint32(bgra[off+1])
			r := uint32(bgra[off+2])
			// ITU-R BT.601 luma
			gray[y*w+x] = byte((r*299 + g*587 + b*114) / 1000)
		}
	}
	return gray
}

// buildHistogram builds a normalised 256-bin grayscale histogram.
func buildHistogram(gray []byte) [256]float64 {
	var counts [256]int
	for _, v := range gray {
		counts[v]++
	}
	total := float64(len(gray))
	var hist [256]float64
	for i, c := range counts {
		hist[i] = float64(c) / total
	}
	return hist
}

// hellingerDistance computes the Hellinger distance between two normalised
// histograms.  Returns a value in [0, 1] where 0 = identical.
func hellingerDistance(a, b [256]float64) float64 {
	var sum float64
	for i := range 256 {
		diff := math.Sqrt(a[i]) - math.Sqrt(b[i])
		sum += diff * diff
	}
	return math.Sqrt(sum / 2)
}
