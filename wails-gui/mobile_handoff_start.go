package main

import (
	"fmt"
	"net/http"
)

func (bridge *mobileBridge) serveHandoffStart(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, id string) {
	if !mobileValidID(id) {
		mobileJSON(w, http.StatusBadRequest, nil, "the handoff id is not valid")
		return
	}
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		accepted, err := bridge.startHandoff(id)
		if err != nil {
			mobileJSON(w, http.StatusConflict, nil, err.Error())
			return
		}
		mobileJSON(w, http.StatusAccepted, map[string]string{"id": accepted.HandoffID, "sessionId": accepted.SessionID}, "")
	})
}

func (bridge *mobileBridge) startHandoff(id string) (HandoffAcceptResult, error) {
	return bridge.startHandoffWithOffers(id, nil)
}

func (bridge *mobileBridge) startHandoffWithOffers(id string, refs []mobileOfferReference) (HandoffAcceptResult, error) {
	if bridge.app == nil || bridge.app.handoffStore == nil {
		return HandoffAcceptResult{}, fmt.Errorf("handoff store not ready")
	}
	entry, err := bridge.app.handoffStore.Get(id)
	if err != nil {
		return HandoffAcceptResult{}, err
	}
	if err := bridge.app.validateListHandoffStart(&entry); err != nil {
		return HandoffAcceptResult{}, err
	}
	return bridge.app.acceptHandoff(id, func(result HandoffAcceptResult) error {
		if listHandoffBackend(&entry) != "" && (result.Workdir != entry.Workdir || mobileTaskRunner(loadAIProviderConfig().Provider) != entry.ListItem.Who) {
			return fmt.Errorf("the selected runner or project is unavailable")
		}
		row, found, err := bridge.app.storeForMobile().MobileChat(bridge.ctx, result.SessionID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("handoff chat is not available")
		}
		row.WorktreePath = result.Workdir
		row.ProjectPath = ""
		_, seq, err := bridge.startChatTurn(row, result.Prompt, refs)
		if err != nil {
			return err
		}
		bridge.app.confirmHandoffRestartReplies(id, seq)
		for _, ref := range refs {
			noteProposalAnswered(judgmentOffer, ref.ID, true)
		}
		return nil
	})
}
