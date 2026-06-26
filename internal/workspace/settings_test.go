package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeSettingsFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, SettingsFileName), []byte(body), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func TestLoadSettingsReadsAcceptOtherBotMessages(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeSettingsFile(t, dir, `{"settings":{"acceptOtherBotMessages":true}}`)
	settings, err := LoadSettings(dir)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if !settings.Settings.AcceptOtherBotMessages {
		t.Fatal("expected acceptOtherBotMessages=true to load")
	}
}

func TestSaveSettingsWritesAcceptOtherBotMessages(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	settings := DefaultSettings()
	settings.Settings.AcceptOtherBotMessages = true
	if err := SaveSettings(dir, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, SettingsFileName))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw settings: %v", err)
	}
	settingsRaw, ok := raw["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings payload = %#v", raw["settings"])
	}
	if value, ok := settingsRaw["acceptOtherBotMessages"].(bool); !ok || !value {
		t.Fatalf("expected acceptOtherBotMessages=true in saved file: %s", data)
	}
}
