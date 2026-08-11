package db

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNotificationSettingsSecretRoundTripIsBackendOnly(t *testing.T) {
	var x NotificationSettings
	if err := json.Unmarshal([]byte(`{"user_id":7,"pushover_app_token":"secret-app-token","pushover_user_key":"user-key","notify_auto_updates":1,"notify_manual_available":1,"notify_manual_updates":1,"updated_at":"now"}`), &x); err != nil {
		t.Fatal(err)
	}
	if !bool(x.NotifyManualUpdates) {
		t.Fatal("manual update notification preference was not loaded")
	}
	if x.PushoverAppToken != "secret-app-token" {
		t.Fatalf("app token was not loaded for backend use: %q", x.PushoverAppToken)
	}
	b, err := json.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret-app-token") || strings.Contains(string(b), "pushover_app_token") {
		t.Fatalf("app token leaked through JSON: %s", b)
	}
}
