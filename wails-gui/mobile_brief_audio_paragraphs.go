package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf8"
)

// Paragraph timings (PROTOCOL.md section 8.1): the voice reads each spoken
// paragraph on its own, the readings are joined with a short pause, and the
// start of each one is recorded against the written paragraph it reads, so a
// phone can follow along and play from any paragraph.

// mobileBriefParagraphGapSeconds is the pause between two spoken paragraphs.
const mobileBriefParagraphGapSeconds = 0.45

var errMobileBriefWAVFormat = errors.New("the readings do not share one WAV format")

// synthesizeMobileBriefParagraphs reads every paragraph in bounded chunks,
// then joins the chunks and records each paragraph's start. A failed chunk
// fails the whole recording; a partial reading is never published.
func synthesizeMobileBriefParagraphs(ctx context.Context, speaker mobileBriefSpeaker, lang, text string) ([]byte, []float64, error) {
	segments := mobileBriefSpokenSegments(text)
	parts := make([][]byte, 0, len(segments))
	for _, segment := range segments {
		chunks := []string{segment}
		limit := ttsMaxChunkRunesEn
		if dominantSpeechLang(segment) != "en" {
			limit = ttsMaxChunkRunesZh
		}
		if utf8.RuneCountInString(segment) > limit {
			chunks = nil
			for _, sentence := range splitIntoSentences(segment) {
				chunks = append(chunks, splitLongChunk(strings.TrimSpace(sentence), limit)...)
			}
		}
		if len(chunks) == 0 {
			return nil, nil, errMobileBriefWAVFormat
		}
		var readings [][]byte
		for _, chunk := range chunks {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			wav, err := speaker.synthesize(ctx, lang, chunk)
			if err != nil {
				return nil, nil, err
			}
			if len(wav) == 0 {
				return nil, nil, errMobileBriefWAVFormat
			}
			readings = append(readings, wav)
		}
		if len(readings) == 1 {
			parts = append(parts, readings[0])
		} else {
			joined, _, err := joinMobileBriefWAV(readings, 0)
			if err != nil {
				return nil, nil, err
			}
			parts = append(parts, joined)
		}
	}
	if len(parts) == 1 {
		return parts[0], []float64{0}, nil
	}
	return joinMobileBriefWAV(parts, mobileBriefParagraphGapSeconds)
}

// mobileBriefSpokenSegments splits the spoken text into the paragraphs the
// voice reads.
func mobileBriefSpokenSegments(text string) []string {
	var out []string
	for _, segment := range strings.Split(text, "\n\n") {
		if segment = strings.TrimSpace(segment); segment != "" {
			out = append(out, segment)
		}
	}
	return out
}

type mobileBriefWAVFormat struct {
	channels, bits uint16
	rate           uint32
}

func mobileBriefWAVHeader(wav []byte) (mobileBriefWAVFormat, []byte, error) {
	if len(wav) < 44 || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[36:40]) != "data" {
		return mobileBriefWAVFormat{}, nil, errMobileBriefWAVFormat
	}
	format := mobileBriefWAVFormat{
		channels: binary.LittleEndian.Uint16(wav[22:24]),
		rate:     binary.LittleEndian.Uint32(wav[24:28]),
		bits:     binary.LittleEndian.Uint16(wav[34:36]),
	}
	if format.channels == 0 || format.rate == 0 || format.bits == 0 || format.bits%8 != 0 {
		return mobileBriefWAVFormat{}, nil, errMobileBriefWAVFormat
	}
	dataLen := int(binary.LittleEndian.Uint32(wav[40:44]))
	if dataLen > len(wav)-44 {
		dataLen = len(wav) - 44
	}
	return format, wav[44 : 44+dataLen], nil
}

// joinMobileBriefWAV joins 16-bit PCM WAVs of one format with silence between
// them, returning where each one starts.
func joinMobileBriefWAV(parts [][]byte, gapSeconds float64) ([]byte, []float64, error) {
	var format mobileBriefWAVFormat
	var body bytes.Buffer
	starts := make([]float64, 0, len(parts))
	for i, part := range parts {
		current, samples, err := mobileBriefWAVHeader(part)
		if err != nil {
			return nil, nil, err
		}
		if i == 0 {
			format = current
		} else if current != format {
			return nil, nil, errMobileBriefWAVFormat
		}
		frame := int(format.channels) * int(format.bits) / 8
		bytesPerSecond := float64(int(format.rate) * frame)
		if i > 0 {
			gap := int(gapSeconds*float64(format.rate)) * frame
			body.Write(make([]byte, gap))
		}
		starts = append(starts, float64(body.Len())/bytesPerSecond)
		body.Write(samples[:len(samples)/frame*frame])
	}
	var out bytes.Buffer
	out.Grow(44 + body.Len())
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(36+body.Len()))
	out.WriteString("WAVEfmt ")
	_ = binary.Write(&out, binary.LittleEndian, uint32(16))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	_ = binary.Write(&out, binary.LittleEndian, format.channels)
	_ = binary.Write(&out, binary.LittleEndian, format.rate)
	_ = binary.Write(&out, binary.LittleEndian, format.rate*uint32(format.channels)*uint32(format.bits/8))
	_ = binary.Write(&out, binary.LittleEndian, format.channels*format.bits/8)
	_ = binary.Write(&out, binary.LittleEndian, format.bits)
	out.WriteString("data")
	_ = binary.Write(&out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes(), starts, nil
}

// mobileBriefParagraphStarts pairs the spoken segments with the written
// paragraphs they read, in order. A segment that reads no written paragraph
// (the template, a closing line) has no start; when no segment matches, the
// recording does not follow the written paragraphs and there are none.
func mobileBriefParagraphStarts(brief MobileBriefV1, lang string, segments []string, starts []float64) []MobileBriefAudioParagraph {
	if len(segments) != len(starts) || len(segments) == 0 {
		return nil
	}
	type written struct {
		source string
		index  int
		spoken string
	}
	speak := func(paragraph MobileBriefPara) string {
		fitted := normalizeBriefText(strings.TrimSpace(paragraph.Text), lang, mobileBriefTextReference(brief), false)
		return strings.TrimSpace(speechDisplayText(mobileBriefParagraphSpeech(brief, MobileBriefPara{Text: fitted, Refs: paragraph.Refs}, lang), lang))
	}
	var candidates []written
	for i, paragraph := range brief.Narrative {
		candidates = append(candidates, written{mobileBriefAudioSourceNarrative, i, speak(paragraph)})
	}
	for i, paragraph := range brief.WeeklyRecap {
		candidates = append(candidates, written{mobileBriefAudioSourceWeekly, i, speak(paragraph)})
	}
	var out []MobileBriefAudioParagraph
	cursor := 0
	for k, segment := range segments {
		for j := cursor; j < len(candidates); j++ {
			spoken := candidates[j].spoken
			// Highlights may lose trailing lines to the time budget, so a
			// reading may be the start of its paragraph.
			if spoken != "" && (spoken == segment || strings.HasPrefix(spoken, segment+"\n")) {
				out = append(out, MobileBriefAudioParagraph{Source: candidates[j].source, Index: candidates[j].index, StartSeconds: starts[k]})
				cursor = j + 1
				break
			}
		}
	}
	return out
}
