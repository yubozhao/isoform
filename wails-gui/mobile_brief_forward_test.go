package main

import (
	"testing"
)

func TestNarrativePartsCountsFollowTheFinalNarrative(t *testing.T) {
	paragraphs := []MobileBriefPara{
		{Text: "a", Part: briefPartDone},
		{Text: "b", Part: briefPartNext},
		{Text: "c", Part: briefPartNext},
	}
	parts := mobileBriefNarrativePartsOf(paragraphs)
	if len(parts) != 2 || parts[0].Count != 1 || parts[1].Count != 2 {
		t.Fatalf("counts = %+v", parts)
	}
	if mobileBriefNarrativePartsOf([]MobileBriefPara{{Text: "a", Part: briefPartNext}, {Text: "b", Part: briefPartDone}}) != nil {
		t.Fatal("out-of-order labels produced parts")
	}
	if mobileBriefNarrativePartsOf([]MobileBriefPara{{Text: "a"}, {Text: "b", Part: briefPartNext}}) != nil {
		t.Fatal("an unlabelled paragraph produced parts")
	}
}
