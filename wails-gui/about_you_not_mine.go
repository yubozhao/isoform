package main

import "log"

// NotMineMemoryEntry is Not mine on an About you claim (design answer A6;
// redesign-you v1.0.0 rule 7): the claim is about someone else, so it goes
// through the wrong-subject path (RejectMemoryAttributionWithPreview: the
// forget cascade with its suppression, so it isn't learned again), and the
// answer is kept in the ledger as not_mine. The moments it came from stay in
// Activity: no backend path takes them off the person yet.
func (a *App) NotMineMemoryEntry(id string) error {
	preview, err := a.GetMemoryForgetPreview(id)
	if err != nil {
		return err
	}
	entry, err := a.GetMemoryEntry(id)
	if err != nil {
		return err
	}
	title := ""
	if points := parseMemoryPoints(entry.Content); len(points) > 0 {
		title = points[0]
	}
	if err := a.RejectMemoryAttributionWithPreview(id, "not_mine", preview.ConfirmationToken); err != nil {
		return err
	}
	if _, err := a.recordAnswer(AnswerRecord{ItemID: "belief:" + id, Kind: answerKindBelief, Source: answerSourceAboutYou, Title: title,
		Action: workActionNotMine, Surface: workSurfaceComputer}); err != nil {
		log.Printf("[answers] a belief's Not mine was not kept: %v", err)
	}
	return nil
}
