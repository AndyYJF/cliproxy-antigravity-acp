package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicResourcesContainNoCredentials(t *testing.T) {
	for _, path := range []string{"/overview", "/app.js", "/style.css", "/logo.svg"} {
		raw, _ := json.Marshal(object{"Method": "GET", "Path": resourceBase + path})
		result, err := handleManagement(raw)
		if err != nil {
			t.Fatal(err)
		}
		r := result.(map[string]any)
		if r["StatusCode"] != 200 {
			t.Fatal(path, r)
		}
		body := string(r["Body"].([]byte))
		for _, forbidden := range []string{"refresh_token", "access_token", "bridge-token", "conleygilbert"} {
			if strings.Contains(body, forbidden) {
				t.Fatal("private data exposed", path)
			}
		}
	}
}
func TestPublicResourceCannotReadStatus(t *testing.T) {
	raw, _ := json.Marshal(object{"Method": "GET", "Path": resourceBase + "/status"})
	result, err := handleManagement(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["StatusCode"] != 404 {
		t.Fatal("public status unexpectedly exposed")
	}
}
func TestCatalogFiltersAndCopies(t *testing.T) {
	if err := rememberCatalog([]byte(`{"data":[{"id":"antigravity-acp/gemini-one"},{"id":"antigravity-acp/claude"},{"id":"antigravity-acp/gemini-one"}]}`)); err != nil {
		t.Fatal(err)
	}
	s := snapshotCatalog()
	if len(s.Models) != 1 || s.CheckedAt == "" {
		t.Fatal(s)
	}
	s.Models[0] = "mutated"
	if snapshotCatalog().Models[0] == "mutated" {
		t.Fatal("cache aliased")
	}
	if rememberCatalog([]byte("invalid")) == nil {
		t.Fatal("accepted malformed catalog")
	}
	if snapshotCatalog().Models[0] != "antigravity-acp/gemini-one" {
		t.Fatal("destroyed last successful catalog")
	}
}
