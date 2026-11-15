package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCPAPromptRulesScopeAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	old := settings()
	mu.Lock()
	current.CPAConfigFile = path
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); current = old; mu.Unlock() })
	write := func(raw string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("upstream:\n  providers:\n    antigravity:\n      sensitive-words: [API, proxy]\n      sensitive-words-replace:\n        'You are Codex, an agent based on GPT-5.': 'You are a helpful coding assistant.'\n      sensitive-patterns-replace:\n        - pattern: '(?m)^RFC 2119[^\\n]*\\n?'\n          replacement: ''\n")
	source := "You are Codex, an agent based on GPT-5.\nRFC 2119 remove this line\nUse API and proxy."
	for _, responses := range []bool{false, true} {
		system := object{"role": "system", "content": source}
		developer := object{"role": "developer", "content": []any{object{"type": "input_text", "text": "API proxy"}}}
		user := object{"role": "user", "content": source}
		tool := object{"role": "tool", "content": source, "name": "API_proxy"}
		body := object{"messages": []any{system, developer, user, tool}}
		if responses {
			body = object{"input": []any{system, developer, user, tool}, "instructions": source}
		}
		if err := obfuscateClientSystem(body, responses); err != nil {
			t.Fatal(err)
		}
		want := "You are a helpful coding assistant.\nUse A\u034fPI and p\u034froxy."
		if system["content"] != want {
			t.Fatalf("wrong system: %q", system["content"])
		}
		if developer["content"].([]any)[0].(object)["text"] != "A\u034fPI p\u034froxy" {
			t.Fatal("developer not processed")
		}
		if user["content"] != source || tool["content"] != source || tool["name"] != "API_proxy" {
			t.Fatal("non-system data changed")
		}
		if responses && body["instructions"] != want {
			t.Fatal("instructions not processed")
		}
	}
	write("upstream:\n  providers:\n    antigravity:\n      sensitive-words-replace: {API: shared-change}\n")
	body := object{"messages": []any{object{"role": "system", "content": "API proxy"}}}
	if err := obfuscateClientSystem(body, false); err != nil {
		t.Fatal(err)
	}
	if body["messages"].([]any)[0].(object)["content"] != "shared-change proxy" {
		t.Fatal("shared rules did not reload")
	}
}

func TestCPAPromptRulesV8Precedence(t *testing.T) {
	rules, err := readCPAPromptRules([]byte("antigravity:\n  sensitive-words: [API]\n  sensitive-words-replace: {old: legacy}\nupstream:\n  providers:\n    antigravity:\n      sensitive-words: []\n      sensitive-words-replace: {new: current}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rules.Words) != 0 || len(rules.Replacements) != 1 || rules.Replacements["new"] != "current" {
		t.Fatalf("%+v", rules)
	}
}

func TestCPAPromptRulesEmptyDoesNotUseOldIdentityRewrite(t *testing.T) {
	old := settings()
	mu.Lock()
	current.CPAConfigFile = ""
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); current = old; mu.Unlock() })
	body := object{"instructions": "You are Codex, an agent based on GPT-5."}
	if err := obfuscateClientSystem(body, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body["instructions"].(string), "Codex") {
		t.Fatal("old identity rewrite remains")
	}
}
