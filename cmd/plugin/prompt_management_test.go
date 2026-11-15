package main

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"testing"
)

func TestPromptRulesManagementSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("# preserved comment\nother-provider:\n  key: fixture-secret\nupstream:\n  providers:\n    antigravity:\n      connection-pool: {enabled: true}\n      sensitive-words: [API]\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	old := settings()
	mu.Lock()
	current.CPAConfigFile = path
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); current = old; mu.Unlock() })
	snapshot, err := promptRulesResponse()
	if err != nil {
		t.Fatal(err)
	}
	revision := snapshot.(object)["revision"]
	bad := object{"revision": revision, "rules": object{"patterns": []object{{"pattern": "[", "replacement": ""}}}}
	raw, _ := json.Marshal(bad)
	if _, err = savePromptRules(raw); err == nil {
		t.Fatal("invalid regex accepted")
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != string(original) {
		t.Fatal("invalid input changed config")
	}
	body := object{"revision": revision, "rules": object{"words": []string{"proxy"}, "replacements": object{"old": "new"}, "patterns": []object{{"pattern": "(?m)^remove.*$", "replacement": ""}}}}
	raw, _ = json.Marshal(body)
	before, _ := os.Stat(path)
	saved, err := savePromptRules(raw)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("bind-mounted inode changed")
	}
	content, _ := os.ReadFile(path)
	var data map[string]any
	if err = yaml.Unmarshal(content, &data); err != nil {
		t.Fatal(err)
	}
	if data["other-provider"].(map[string]any)["key"] != "fixture-secret" {
		t.Fatal("unrelated config changed")
	}
	agv := data["upstream"].(map[string]any)["providers"].(map[string]any)["antigravity"].(map[string]any)
	if agv["connection-pool"].(map[string]any)["enabled"] != true {
		t.Fatal("other AGV setting changed")
	}
	if saved.(object)["revision"] == revision {
		t.Fatal("revision unchanged")
	}
	if _, err = savePromptRules(raw); err == nil {
		t.Fatal("stale rule revision accepted")
	}
}
