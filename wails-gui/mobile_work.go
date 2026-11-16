package main

import (
	"net/http"
)

// serveWorkList answers GET /work (PROTOCOL.md section 26): Your work as the
// computer shows it, from the same snapshot, scoped to what this phone may
// read. A phone without chat:read gets no topic proposals, as on /today.
func (bridge *mobileBridge) serveWorkList(w http.ResponseWriter, device mobileDeviceRecord) {
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	list, err := bridge.app.GetWorkList()
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	mobileJSON(w, http.StatusOK, mobileWorkListForDevice(list, device), "")
}

// mobileWorkListForDevice drops what the device's scopes don't cover and
// recounts the list.
func mobileWorkListForDevice(list WorkList, device mobileDeviceRecord) WorkList {
	if mobileHasScope(device.Scopes, mobileScopeChatRead) {
		return list
	}
	out := list
	out.Items, out.ToAnswer, out.BackFromAgents = []WorkItem{}, 0, 0
	for _, item := range list.Items {
		if item.Source == workSourceTopic {
			continue
		}
		item.Order = len(out.Items)
		out.Items = append(out.Items, item)
		switch item.State {
		case workStateOpen:
			out.ToAnswer++
		case workStateBack:
			out.ToAnswer++
			out.BackFromAgents++
		}
	}
	out.Version = workListVersion(out)
	return out
}
