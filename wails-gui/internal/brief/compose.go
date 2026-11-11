package brief

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Section keys. They are the names MobileBriefSections uses on the wire.
const (
	SectionNoticed       = "noticed"
	SectionMoved         = "moved"
	SectionDoing         = "doing"
	SectionWatching      = "watching"
	SectionMeetings      = "meetings"
	SectionPeople        = "people"
	SectionOpenLoops     = "openLoops"
	SectionLearned       = "learned"
	SectionDeliverables  = "deliverables"
	SectionSchedules     = "schedules"
	SectionWorkingToward = "workingToward"
	SectionCommitments   = "commitments"
	SectionWaitingOnYou  = "waitingOnYou"
)

// Wire limits, the same numbers the app's builder enforces.
const (
	ItemsPerSection = 8
	EvidenceLimit   = 60
	TextLimit       = 320
	labelLimit      = 200
	kindLimit       = 64
)

// sectionSpec is one composed section and its item cap.
type sectionSpec struct {
	name  string
	limit int
}

// composedOrder is the order the sections spend the evidence budget in. The
// reserved sections come first, as in the app's builder, so a crowded list
// cannot starve them. openLoops comes after moved so it can drop what moved
// already shows. Chief sections (decide, plan, highlights, day ahead), day
// shape, attendance, cards and the weekly recap are not composed in this step.
var composedOrder = []sectionSpec{
	{SectionWaitingOnYou, 3},
	{SectionWorkingToward, 5},
	{SectionCommitments, ItemsPerSection},
	{SectionMoved, ItemsPerSection},
	{SectionDoing, ItemsPerSection},
	{SectionWatching, ItemsPerSection},
	{SectionMeetings, ItemsPerSection},
	{SectionPeople, ItemsPerSection},
	{SectionOpenLoops, ItemsPerSection},
	{SectionLearned, ItemsPerSection},
	{SectionDeliverables, ItemsPerSection},
	{SectionSchedules, ItemsPerSection},
	{SectionNoticed, 2},
}

// ComposedSectionNames lists the sections Compose fills, in budget order.
func ComposedSectionNames() []string {
	names := make([]string, 0, len(composedOrder))
	for _, spec := range composedOrder {
		names = append(names, spec.name)
	}
	return names
}

// Placed is one item in a composed section (the wire MobileBriefItem, without
// the parts this step does not compose).
type Placed struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Detail string   `json:"detail,omitempty"`
	Status string   `json:"status,omitempty"`
	Kind   string   `json:"kind,omitempty"`
	At     string   `json:"at,omitempty"`
	Refs   []string `json:"refs"`
}

// Composed is what compose and the narrative step produced for one window. It
// is never sent to the phone. Narrative is empty unless the judge passed it.
type Composed struct {
	Sections  map[string][]Placed `json:"sections"`
	Evidence  []Evidence          `json:"evidence"`
	Narrative []Paragraph         `json:"narrative"`
	Truncated []string            `json:"truncated,omitempty"`
}

// Compose builds the deterministic sections from in. Given the same snapshot it
// always returns the same result. It reads no store and calls no model.
//
// Every item keeps its snapshot order. Each distinct source gets one evidence
// ref, shared by every item that cites it. When the evidence budget is spent,
// further items are dropped and their section is recorded as truncated.
func Compose(in Inputs) Composed {
	c := Composed{Sections: map[string][]Placed{}, Evidence: []Evidence{}}
	bySection := map[string][]Item{}
	for _, item := range in.Snapshot.Items {
		bySection[item.Section] = append(bySection[item.Section], item)
	}
	refs := map[string]string{}
	movedKeys := map[string]bool{}
	for _, spec := range composedOrder {
		shown := c.build(spec, bySection[spec.name], refs)
		if spec.name == SectionMoved {
			for _, item := range shown {
				movedKeys[item.key] = true
			}
		}
		if spec.name == SectionOpenLoops {
			// A source moved already shows needs one row, as in the app.
			kept := shown[:0]
			for _, item := range shown {
				if !movedKeys[item.key] {
					kept = append(kept, item)
				}
			}
			shown = kept
		}
		if len(shown) > 0 {
			placed := make([]Placed, 0, len(shown))
			for _, item := range shown {
				placed = append(placed, item.placed)
			}
			c.Sections[spec.name] = placed
		}
	}
	return c
}

// shownItem is a composed item with its source key, so the open-loop dedupe
// can read it after the section is built.
type shownItem struct {
	key    string
	placed Placed
}

// build places the items of one section, up to its cap, and assigns evidence.
func (c *Composed) build(spec sectionSpec, items []Item, refs map[string]string) []shownItem {
	var shown []shownItem
	for _, item := range items {
		if len(shown) >= spec.limit {
			break
		}
		title := clip(strings.TrimSpace(item.Title), TextLimit)
		if title == "" || strings.TrimSpace(item.SourceID) == "" {
			continue
		}
		key := item.SourceKind + "\x00" + item.SourceID
		ref, ok := refs[key]
		if !ok {
			if len(c.Evidence) >= EvidenceLimit {
				c.markTruncated(spec.name)
				continue
			}
			ref = fmt.Sprintf("e%d", len(c.Evidence)+1)
			refs[key] = ref
			c.Evidence = append(c.Evidence, Evidence{
				Ref:        ref,
				SourceKind: clip(item.SourceKind, kindLimit),
				SourceID:   clip(item.SourceID, 128),
				Label:      clip(title, labelLimit),
				ObservedAt: formatAt(item.At),
			})
		}
		shown = append(shown, shownItem{key: key, placed: Placed{
			ID:     fmt.Sprintf("%s-%d", spec.name, len(shown)+1),
			Title:  title,
			Detail: clip(strings.TrimSpace(item.Detail), TextLimit),
			Status: clip(item.Status, kindLimit),
			Kind:   clip(item.Kind, kindLimit),
			At:     formatAt(item.At),
			Refs:   []string{ref},
		}})
	}
	return shown
}

func (c *Composed) markTruncated(name string) {
	for _, existing := range c.Truncated {
		if existing == name {
			return
		}
	}
	c.Truncated = append(c.Truncated, name)
}

// Counts reads the counts a sealed copy and a shadow copy are compared by.
// Cards are not composed in this step, so the card count is always zero.
func (c Composed) Counts() ShadowCounts {
	return ShadowCounts{Narrative: len(c.Narrative), Cards: 0, Evidence: len(c.Evidence)}
}

// SectionCounts returns the item count of each composed section, zero included.
func (c Composed) SectionCounts() map[string]int {
	out := make(map[string]int, len(composedOrder))
	for _, spec := range composedOrder {
		out[spec.name] = len(c.Sections[spec.name])
	}
	return out
}

// ShadowCounts are the counts a comparison reads: narrative paragraphs, cards
// and evidence refs.
type ShadowCounts struct {
	Narrative int `json:"narrative"`
	Cards     int `json:"cards"`
	Evidence  int `json:"evidence"`
}

// clip cuts s to at most max bytes at a rune boundary.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func formatAt(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}
