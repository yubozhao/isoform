package main

const (
	morningBriefEmptyEN = "The available items did not surface a decision for this morning."
	morningBriefEmptyZH = "可用的事项中，今早没有发现需要决定的事。"
)

// Only these fixed, non-personal sentences may be delivered without source
// references. Any modification takes the ordinary ranked privacy path.
func mobileBriefStaticEmpty(brief MobileBriefV1) bool {
	return brief.rankedEdition && brief.rankedEmpty && brief.NarrativeStatus == "ok" && len(brief.Narrative) == 1 &&
		len(brief.Narrative[0].Refs) == 0 && len(brief.Cards) == 0 && len(brief.Offers) == 0 &&
		(brief.Narrative[0].Text == morningBriefEmptyEN || brief.Narrative[0].Text == morningBriefEmptyZH || brief.Narrative[0].Text == eveningEmptyEN || brief.Narrative[0].Text == eveningEmptyZH)
}
