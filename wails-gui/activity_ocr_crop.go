package main

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"math"
)

// minOCRCropPixels is the lower bound below which we skip cropping and fall
// back to the full frame. Tiny rects (e.g. a single-pixel utility window or a
// stale rect from an app that just quit) produce useless OCR output and
// degrade the audit visualisation downstream.
const minOCRCropPixels = 64

// cropImageToFocusRect crops img to the normalized [0,1] focused-window
// rect and returns the cropped image plus the absolute pixel rectangle.
// The third return value is false when the rect is degenerate (zero
// dimensions or smaller than minOCRCropPixels after clamping); callers
// should fall back to the uncropped image in that case.
//
// Operates entirely in the image.Image domain so callers can chain it
// with upscaleImage2x without an intermediate JPEG round trip.
func cropImageToFocusRect(img image.Image, nx, ny, nw, nh float64) (image.Image, image.Rectangle, bool) {
	if img == nil || nw <= 0 || nh <= 0 {
		return img, image.Rectangle{}, false
	}
	bounds := img.Bounds()
	imgW := float64(bounds.Dx())
	imgH := float64(bounds.Dy())
	if imgW <= 0 || imgH <= 0 {
		return img, image.Rectangle{}, false
	}

	x0 := int(math.Floor(math.Max(0, nx*imgW)))
	y0 := int(math.Floor(math.Max(0, ny*imgH)))
	x1 := int(math.Ceil(math.Min(imgW, (nx+nw)*imgW)))
	y1 := int(math.Ceil(math.Min(imgH, (ny+nh)*imgH)))

	if x1-x0 < minOCRCropPixels || y1-y0 < minOCRCropPixels {
		return img, image.Rectangle{}, false
	}

	cropRect := image.Rect(bounds.Min.X+x0, bounds.Min.Y+y0, bounds.Min.X+x1, bounds.Min.Y+y1)

	// Use SubImage when available (image.RGBA etc) to avoid an extra copy;
	// otherwise draw into a fresh RGBA. The stdlib jpeg.Decode usually
	// returns *image.YCbCr which does implement SubImage.
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	if si, ok := img.(subImager); ok {
		return si.SubImage(cropRect), cropRect, true
	}
	dst := image.NewRGBA(image.Rect(0, 0, x1-x0, y1-y0))
	draw.Draw(dst, dst.Bounds(), img, cropRect.Min, draw.Src)
	return dst, cropRect, true
}

// upscaleImage2x doubles each axis of img via nearest-neighbor sampling.
//
// Nearest-neighbor is intentional: bilinear / Lanczos smooth the glyph
// edges which Vision is trained to read sharply. The blocky upscale
// preserves edge contrast at the cost of artifacts that the OCR engine
// doesn't care about.
//
// Returns img unchanged when its bounds are degenerate.
func upscaleImage2x(img image.Image) image.Image {
	if img == nil {
		return img
	}
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return img
	}

	dstW := srcW * 2
	dstH := srcH * 2
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < dstH; y++ {
		srcY := bounds.Min.Y + y/2
		for x := 0; x < dstW; x++ {
			srcX := bounds.Min.X + x/2
			dst.Set(x, y, img.At(srcX, srcY))
		}
	}
	return dst
}

// cropJPEGToFocusRect decodes the JPEG, crops it to the normalized [0,1]
// focused-window rect, and re-encodes. Used by the OCR worker to scope
// recognition to the focused window so unrelated apps and on-screen
// distractions don't bleed into the triage prompt.
//
// Returns (originalData, image.Rectangle{}, nil) when the rect is missing
// (zeroes — legacy snapshot, multi-display fallback, non-darwin) or
// degenerate (smaller than minOCRCropPixels on either axis after clamping).
// In those cases callers should run OCR on the original full-frame bytes.
//
// quality is the JPEG re-encode quality; pass the same value used at capture
// time so the crop doesn't accumulate visible artifacts on subsequent passes.
//
// Callers that need to chain multiple image transforms (crop + upscale +
// other) should decode once and use cropImageToFocusRect /
// upscaleImage2x directly to avoid intermediate JPEG round trips.
func cropJPEGToFocusRect(jpegData []byte, nx, ny, nw, nh float64, quality int) ([]byte, image.Rectangle, error) {
	if nw <= 0 || nh <= 0 {
		return jpegData, image.Rectangle{}, nil
	}
	img, _, err := image.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, image.Rectangle{}, fmt.Errorf("decode jpeg: %w", err)
	}
	cropped, cropRect, ok := cropImageToFocusRect(img, nx, ny, nw, nh)
	if !ok {
		return jpegData, image.Rectangle{}, nil
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, cropped, &jpeg.Options{Quality: quality}); err != nil {
		return nil, image.Rectangle{}, fmt.Errorf("encode cropped jpeg: %w", err)
	}
	return buf.Bytes(), cropRect, nil
}

// cropForOCR crops a snapshot's JPEG for background OCR. WeChat additionally has
// its left conversation-list sidebar removed (cropJPEGToWeChatMainArea), so the
// group/contact list never dominates the OCR text and misleads event extraction
// into "browsing groups" noise. All other apps use the plain focus-rect crop.
func cropForOCR(snap ActivitySnapshotRow, jpegData []byte, quality int) ([]byte, image.Rectangle, error) {
	if snap.App == "tencent_chat" {
		return cropJPEGToWeChatMainArea(jpegData,
			snap.FocusRectX, snap.FocusRectY, snap.FocusRectW, snap.FocusRectH,
			wechatSidebarFraction, quality)
	}
	return cropJPEGToFocusRect(jpegData,
		snap.FocusRectX, snap.FocusRectY, snap.FocusRectW, snap.FocusRectH, quality)
}

// cropJPEGToWeChatMainArea crops to the focused window, then drops the left
// conversation-list sidebar, leaving only the active chat area. Falls back to the
// focus-rect crop (or the original frame) when the rect is missing or the sidebar
// cut would be degenerate.
func cropJPEGToWeChatMainArea(jpegData []byte, nx, ny, nw, nh, sidebarFraction float64, quality int) ([]byte, image.Rectangle, error) {
	img, _, err := image.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, image.Rectangle{}, fmt.Errorf("decode jpeg: %w", err)
	}
	win, winRect, ok := cropImageToFocusRect(img, nx, ny, nw, nh)
	if !ok {
		return jpegData, image.Rectangle{}, nil
	}
	b := win.Bounds()
	cut := b.Min.X + int(float64(b.Dx())*sidebarFraction)
	mainRect := image.Rect(cut, b.Min.Y, b.Max.X, b.Max.Y)
	if mainRect.Dx() < minOCRCropPixels || mainRect.Dy() < minOCRCropPixels {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, win, &jpeg.Options{Quality: quality}); err != nil {
			return nil, image.Rectangle{}, fmt.Errorf("encode focus crop: %w", err)
		}
		return buf.Bytes(), winRect, nil
	}
	var main image.Image
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	if si, ok := win.(subImager); ok {
		main = si.SubImage(mainRect)
	} else {
		dst := image.NewRGBA(image.Rect(0, 0, mainRect.Dx(), mainRect.Dy()))
		draw.Draw(dst, dst.Bounds(), win, mainRect.Min, draw.Src)
		main = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, main, &jpeg.Options{Quality: quality}); err != nil {
		return nil, image.Rectangle{}, fmt.Errorf("encode wechat main: %w", err)
	}
	return buf.Bytes(), mainRect, nil
}
