package main

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// Routes and status fields that a piece adds from its own file.
//
// A piece registers them in an init of its own file instead of adding a case
// to the route switch or a field to mobileStatusView. Two pieces in flight
// then never edit the same lines of mobile_bridge_api.go, which is what made
// every open branch conflict each time one of them merged.

// mobileExtraRoute is one registered route: the scopes it needs, all of them,
// and what serves it.
type mobileExtraRoute struct {
	scopes []string
	serve  func(bridge *mobileBridge, w http.ResponseWriter, r *http.Request, device mobileDeviceRecord)
}

var mobileExtraRoutes = map[string]mobileExtraRoute{}

// mobileRegisterRoute adds a hub route, keyed "METHOD /path". A key is
// registered once; twice is a programming error.
func mobileRegisterRoute(key string, scopes []string, serve func(bridge *mobileBridge, w http.ResponseWriter, r *http.Request, device mobileDeviceRecord)) {
	if _, taken := mobileExtraRoutes[key]; taken {
		panic("mobile route registered twice: " + key)
	}
	mobileExtraRoutes[key] = mobileExtraRoute{scopes: scopes, serve: serve}
}

// serveExtraRoute serves a registered route and reports whether there was one.
// A device without a needed scope gets the usual refusal.
func (bridge *mobileBridge) serveExtraRoute(key string, w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) bool {
	route, ok := mobileExtraRoutes[key]
	if !ok {
		return false
	}
	for _, scope := range route.scopes {
		if !mobileRequireScope(w, device, scope) {
			return true
		}
	}
	route.serve(bridge, w, r, device)
	return true
}

var mobileStatusExtras []func(bridge *mobileBridge, status map[string]any)

// mobileRegisterStatus adds fields to GET /status. The function sets its keys
// on the status; a field that does not apply is simply not set.
func mobileRegisterStatus(add func(bridge *mobileBridge, status map[string]any)) {
	mobileStatusExtras = append(mobileStatusExtras, add)
}

// statusWithExtras is the status the phone reads: the view, and the fields
// pieces registered.
func (bridge *mobileBridge) statusWithExtras(view mobileStatusView) any {
	if len(mobileStatusExtras) == 0 {
		return view
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return view
	}
	status := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&status); err != nil {
		return view
	}
	for _, add := range mobileStatusExtras {
		add(bridge, status)
	}
	return status
}
