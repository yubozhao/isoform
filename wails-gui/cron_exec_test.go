//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import "testing"

func TestIsNoDeliverableResult(t *testing.T) {
	tests := []struct {
		name           string
		summary        string
		filePaths      []string
		deliveryTarget *DeliveryTarget
		want           bool
	}{
		{
			name:    "empty summary, no files, no target",
			summary: "",
			want:    true,
		},
		{
			name:    "whitespace-only summary, no files, no target",
			summary: "   \n\t  ",
			want:    true,
		},
		{
			name:    "has summary text",
			summary: "Drafted pitch email for Zoom.",
			want:    false,
		},
		{
			name:      "empty summary but has deliverable file",
			summary:   "",
			filePaths: []string{"/tmp/out.md"},
			want:      false,
		},
		{
			name:           "empty summary but has delivery target",
			summary:        "",
			deliveryTarget: &DeliveryTarget{Platform: "slack", Channel: "marketing"},
			want:           false,
		},
		{
			name:           "all three signals present",
			summary:        "Done.",
			filePaths:      []string{"/tmp/a.md"},
			deliveryTarget: &DeliveryTarget{Platform: "discord", Channel: "general"},
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNoDeliverableResult(tt.summary, tt.filePaths, tt.deliveryTarget)
			if got != tt.want {
				t.Errorf("isNoDeliverableResult(%q, %v, %v) = %v, want %v",
					tt.summary, tt.filePaths, tt.deliveryTarget, got, tt.want)
			}
		})
	}
}

func TestParseDeliveryTarget(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantPlat   string
		wantServer string
		wantChan   string
		wantNil    bool
	}{
		{
			name:     "multiline format",
			input:    "some text\n<delivery_target>\nplatform: feishu\nchannel: test\n</delivery_target>\nmore text",
			wantPlat: "feishu",
			wantChan: "test",
		},
		{
			name:     "single-line format (agent bug)",
			input:    "some text\n<delivery_target> platform: feishu channel: test </delivery_target>",
			wantPlat: "feishu",
			wantChan: "test",
		},
		{
			name:     "slack with hash prefix",
			input:    "<delivery_target>\nplatform: slack\nchannel: #marketing\n</delivery_target>",
			wantPlat: "slack",
			wantChan: "marketing",
		},
		{
			name:    "no block",
			input:   "just regular text",
			wantNil: true,
		},
		{
			name:     "single-line slack",
			input:    "<delivery_target> platform: slack channel: general </delivery_target>",
			wantPlat: "slack",
			wantChan: "general",
		},
		{
			name:     "telegram with @username (@ preserved for API)",
			input:    "<delivery_target>\nplatform: telegram\nchannel: @mychannel\n</delivery_target>",
			wantPlat: "telegram",
			wantChan: "@mychannel",
		},
		{
			name:       "discord with server",
			input:      "<delivery_target>\nplatform: discord\nserver: isoformDev\nchannel: general\n</delivery_target>",
			wantPlat:   "discord",
			wantServer: "isoformDev",
			wantChan:   "general",
		},
		{
			name:       "discord single-line with server",
			input:      "<delivery_target> platform: discord server: isoformDev channel: general </delivery_target>",
			wantPlat:   "discord",
			wantServer: "isoformDev",
			wantChan:   "general",
		},
		{
			name:       "discord with guild keyword",
			input:      "<delivery_target>\nplatform: discord\nguild: MyGuild\nchannel: chat\n</delivery_target>",
			wantPlat:   "discord",
			wantServer: "MyGuild",
			wantChan:   "chat",
		},
		{
			name:       "discord server name with spaces",
			input:      "<delivery_target>\nplatform: discord\nserver: Marketing Team\nchannel: daily-updates\n</delivery_target>",
			wantPlat:   "discord",
			wantServer: "Marketing Team",
			wantChan:   "daily-updates",
		},
		{
			name:     "feishu channel name with spaces",
			input:    "<delivery_target>\nplatform: feishu\nchannel: Product Discussion Group\n</delivery_target>",
			wantPlat: "feishu",
			wantChan: "Product Discussion Group",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, target := parseDeliveryTarget(tt.input)
			if tt.wantNil {
				if target != nil {
					t.Fatalf("expected nil target, got %+v", target)
				}
				return
			}
			if target == nil {
				t.Fatal("expected non-nil target, got nil")
			}
			if target.Platform != tt.wantPlat {
				t.Errorf("platform: got %q, want %q", target.Platform, tt.wantPlat)
			}
			if target.Server != tt.wantServer {
				t.Errorf("server: got %q, want %q", target.Server, tt.wantServer)
			}
			if target.Channel != tt.wantChan {
				t.Errorf("channel: got %q, want %q", target.Channel, tt.wantChan)
			}
		})
	}
}
