package main

// What a memory or knowledge write did, and which kind of artifact it
// wrote. The memory and knowledge generators report these.
const (
	BackgroundArtifactMemory    = "memory"
	BackgroundArtifactKnowledge = "knowledge"

	BackgroundMaterializationCreated  = "created"
	BackgroundMaterializationUpdated  = "updated"
	BackgroundMaterializationExisting = "existing"
	BackgroundMaterializationSkipped  = "skipped"
)
