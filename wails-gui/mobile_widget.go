package main

import (
	"net/http"
)

// mobileWidgetV1 is the phone's widget snapshot (PROTOCOL.md 8.3, phone
// redesign B11): the latest brief's kind, time and first line, and the count
// to answer, ranked like the app. The watch line is the phone's own.
type mobileWidgetV1 struct {
	V        int                `json:"v"`
	AsOf     string             `json:"asOf"`
	Brief    *mobileWidgetBrief `json:"brief,omitempty"`
	ToAnswer int                `json:"toAnswer"`
}

type mobileWidgetBrief struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
	MadeAt  string `json:"madeAt"`
	Lead    string `json:"lead"`
}

// mobileWidgetFromToday derives the widget from a device's Today snapshot,
// the same rule the phone applies to a today_changed snapshot.
func mobileWidgetFromToday(view MobileTodayV1) mobileWidgetV1 {
	widget := mobileWidgetV1{V: 1, AsOf: view.GeneratedAt, ToAnswer: view.NeedsYouCount}
	if view.BriefStatus == nil || view.BriefStatus.Current == nil {
		return widget
	}
	current := view.BriefStatus.Current
	widget.Brief = &mobileWidgetBrief{ID: current.ID, Version: current.Version, Kind: current.Kind, MadeAt: current.MadeAt}
	for _, row := range view.Briefs {
		if row.ID == current.ID {
			widget.Brief.Lead = row.Lead
			break
		}
	}
	return widget
}

// serveWidget answers GET /widget from Today's snapshot; it builds nothing.
func (bridge *mobileBridge) serveWidget(w http.ResponseWriter, device mobileDeviceRecord) {
	bridge.today.mu.RLock()
	if bridge.today.value == nil {
		bridge.today.mu.RUnlock()
		bridge.noteTodayChanged()
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	snapshot := *bridge.today.value
	bridge.today.mu.RUnlock()
	mobileJSON(w, http.StatusOK, mobileWidgetFromToday(mobileTodayForDevice(snapshot, device)), "")
}
