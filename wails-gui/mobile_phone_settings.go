package main

import "net/http"

// The phone's settings that act on this computer beyond the pause routes
// (mobile_observing_pause.go registers those): a pause with no end (24.10),
// turning Proposals from your topics on or off (15.9.1), and reading what went
// to the person's agents (27.3). Registered here, so the routes and status
// fields stay out of mobile_bridge_api.go (mobile_bridge_extras.go).
func init() {
	mobileRegisterRoute("POST /topics-daily/enabled", []string{mobileScopeOffersDecide}, func(bridge *mobileBridge, w http.ResponseWriter, r *http.Request, _ mobileDeviceRecord) {
		bridge.serveTopicsDailyEnabled(w, r)
	})
	mobileRegisterRoute("GET /agents/handoffs", []string{mobileScopeChatRead}, func(bridge *mobileBridge, w http.ResponseWriter, r *http.Request, _ mobileDeviceRecord) {
		bridge.serveYourAgentHandoffs(w, r)
	})
	mobileRegisterStatus(func(_ *mobileBridge, status map[string]any) {
		// POST /observing/pause takes {"untilEnded": true}, a pause with no end (24.10).
		status["observingPauseUntilEnded"] = true
		// POST /topics-daily/enabled turns Proposals from your topics on or off (15.9.1).
		status["topicsDailySwitch"] = true
		// GET /agents/handoffs lists what went to the person's agents (27.3).
		status["agentHandoffs"] = true
	})
}
