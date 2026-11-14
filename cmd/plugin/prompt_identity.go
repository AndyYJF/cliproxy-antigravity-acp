package main

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var codexIdentity = regexp.MustCompile(`(?i)^You are Codex, an agent based on GPT-[0-9]+(?:\.[0-9]+)*(?:-[a-z0-9]+)*\.`)

// neutralIdentity only rewrites a recognized opening sentence, not arbitrary
// mentions of a client, model, file path, or quoted instructions later in text.
func neutralIdentity(text string) string {
	leading := len(text) - len(strings.TrimLeftFunc(text, unicode.IsSpace))
	rest := text[leading:]
	matched, replacement := "", ""
	switch {
	case strings.HasPrefix(rest, "You are a coding assistant running in Codex CLI."):
		matched = "You are a coding assistant running in Codex CLI."
		replacement = "You are a coding assistant."
	case strings.HasPrefix(rest, "You are an expert coding assistant operating inside pi, a coding agent harness."):
		matched = "You are an expert coding assistant operating inside pi, a coding agent harness."
		replacement = "You are an expert coding assistant."
	default:
		matched = codexIdentity.FindString(rest)
		replacement = "You are a coding assistant."
	}
	if matched == "" {
		return text
	}
	tail := rest[len(matched):]
	if tail != "" {
		next, _ := utf8.DecodeRuneInString(tail)
		if !unicode.IsSpace(next) {
			return text
		}
	}
	return text[:leading] + replacement + tail
}

func neutralSystemContent(content any) any {
	switch value := content.(type) {
	case string:
		return neutralIdentity(value)
	case []any:
		for _, raw := range value {
			part, ok := raw.(map[string]any)
			if !ok || (part["type"] != "text" && part["type"] != "input_text") {
				continue
			}
			if text, ok := part["text"].(string); ok {
				part["text"] = neutralIdentity(text)
			}
		}
	}
	return content
}

// Normalize before the bridge computes history and session fingerprints, so
// first turns, continuations, and streaming requests use the same identity.
func neutralizeClientIdentity(body object, responses bool) {
	field := "messages"
	if responses {
		field = "input"
		if instructions, ok := body["instructions"].(string); ok {
			body["instructions"] = neutralIdentity(instructions)
		}
	}
	messages, _ := body[field].([]any)
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok || (message["role"] != "system" && message["role"] != "developer") {
			continue
		}
		if responses && message["type"] != nil && message["type"] != "" && message["type"] != "message" {
			continue
		}
		if content, exists := message["content"]; exists {
			message["content"] = neutralSystemContent(content)
		}
	}
}
