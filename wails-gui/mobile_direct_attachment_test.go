package main

import (
	"context"
	"errors"
	"testing"
)

// An attachment reaches both relays, and one relay being down does not lose it.
func TestMobileMirroredUploaderSucceedsWhenEitherRelayTakesIt(t *testing.T) {
	down := errors.New("relay unreachable")
	cases := []struct {
		name             string
		primary, direct  error
		wantErr          bool
		onPrimary, onDir bool
	}{
		{"both", nil, nil, false, true, true},
		{"pinned relay down", down, nil, false, false, true},
		{"direct path down", nil, down, false, true, false},
		{"both down", down, down, true, false, false},
	}
	for _, tc := range cases {
		primary := &stubAttachmentUploader{err: tc.primary}
		direct := &stubAttachmentUploader{err: tc.direct}
		uploader := mobileMirroredUploader{primary: primary, direct: direct}
		err := uploader.uploadAttachment(context.Background(), "a1", []byte("sealed"))
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err = %v, want error %v", tc.name, err, tc.wantErr)
		}
		if (primary.uploaded["a1"] != nil) != tc.onPrimary || (direct.uploaded["a1"] != nil) != tc.onDir {
			t.Fatalf("%s: pinned %v direct %v", tc.name, primary.uploaded, direct.uploaded)
		}
	}
}

// Without a connected direct path, or with no phone granted on it, uploads go
// to the pinned relay alone.
func TestMobileAttachmentUploaderUsesThePinnedRelayAlone(t *testing.T) {
	bridge, err := newMobileBridge(&App{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	// The test clients never ran, so Stop must not find one to stop.
	t.Cleanup(func() { bridge.direct.Store(nil) })
	primary := &mobileRelayClient{origin: "https://relay.example"}
	if got := bridge.attachmentUploader(primary); got != mobileAttachmentUploader(primary) {
		t.Fatalf("no direct path: uploader %T", got)
	}
	direct := &mobileRelayClient{origin: "http://127.0.0.1:1", state: mobileRelayConnected}
	bridge.direct.Store(direct)
	if got := bridge.attachmentUploader(primary); got != mobileAttachmentUploader(primary) {
		t.Fatalf("no granted phone: uploader %T", got)
	}
	bridge.directMu.Lock()
	bridge.directGrants["d1"] = mobileDirectGrant{DeviceID: "d1"}
	bridge.directMu.Unlock()
	if _, ok := bridge.attachmentUploader(primary).(mobileMirroredUploader); !ok {
		t.Fatal("a connected direct path with a granted phone must get the attachment too")
	}
}
