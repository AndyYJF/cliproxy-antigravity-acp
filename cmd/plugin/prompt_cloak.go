package main

import (
	"errors"
	"os"
	"sync"
	"time"

	cpaconfig "cliproxy-antigravity-acp/internal/cpacloak/config"
	"cliproxy-antigravity-acp/internal/cpacloak/helps"
	"gopkg.in/yaml.v3"
)

type cpaPromptRules struct {
	Words        []string
	Replacements map[string]string
	Patterns     []cpaconfig.SensitivePatternRule
}

var promptMatcherCache struct {
	sync.Mutex
	path     string
	modified time.Time
	size     int64
	matcher  *helps.SensitiveWordMatcher
}

// Read the same three fields as CPA. A present v8 field replaces its legacy value.
func readCPAPromptRules(data []byte) (cpaPromptRules, error) {
	var document yaml.Node
	var rules cpaPromptRules
	if err := yaml.Unmarshal(data, &document); err != nil {
		return rules, err
	}
	find := func(node *yaml.Node, keys ...string) *yaml.Node {
		if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
			node = node.Content[0]
		}
		for _, key := range keys {
			var next *yaml.Node
			if node.Kind == yaml.MappingNode {
				for i := 0; i < len(node.Content); i += 2 {
					if node.Content[i].Value == key {
						next = node.Content[i+1]
						break
					}
				}
			}
			if next == nil {
				return nil
			}
			node = next
		}
		return node
	}
	for _, node := range []*yaml.Node{find(&document, "antigravity"), find(&document, "upstream", "providers", "antigravity")} {
		if node == nil || node.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i < len(node.Content); i += 2 {
			var target any
			switch node.Content[i].Value {
			case "sensitive-words":
				rules.Words = nil
				target = &rules.Words
			case "sensitive-words-replace":
				rules.Replacements = nil
				target = &rules.Replacements
			case "sensitive-patterns-replace":
				rules.Patterns = nil
				target = &rules.Patterns
			default:
				continue
			}
			if err := node.Content[i+1].Decode(target); err != nil {
				return rules, err
			}
		}
	}
	return rules, nil
}

func currentPromptMatcher() (*helps.SensitiveWordMatcher, error) {
	path := settings().CPAConfigFile
	if path == "" {
		return nil, nil
	}
	promptMatcherCache.Lock()
	defer promptMatcherCache.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		return nil, errors.New("Unable to read CPA prompt rules configuration")
	}
	if promptMatcherCache.path == path && promptMatcherCache.modified.Equal(info.ModTime()) && promptMatcherCache.size == info.Size() {
		return promptMatcherCache.matcher, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("Unable to read CPA prompt rules configuration")
	}
	rules, err := readCPAPromptRules(raw)
	if err != nil {
		return nil, errors.New("Invalid CPA prompt rules configuration")
	}
	matcher := helps.BuildAntigravitySensitiveWordMatcherWithPatterns(rules.Words, rules.Replacements, rules.Patterns)
	promptMatcherCache.path, promptMatcherCache.modified, promptMatcherCache.size = path, info.ModTime(), info.Size()
	promptMatcherCache.matcher = matcher
	return matcher, nil
}

func obfuscateSystemContent(content any, matcher *helps.SensitiveWordMatcher) any {
	switch value := content.(type) {
	case string:
		return matcher.ObfuscateText(value)
	case []any:
		for _, raw := range value {
			part, ok := raw.(map[string]any)
			if !ok || (part["type"] != "text" && part["type"] != "input_text") {
				continue
			}
			if text, ok := part["text"].(string); ok {
				part["text"] = matcher.ObfuscateText(text)
			}
		}
	}
	return content
}

// Apply CPA's AGV matcher before history fingerprints are computed. Only the
// client's system/developer text and Responses instructions are system inputs.
func obfuscateClientSystem(body object, responses bool) error {
	matcher, err := currentPromptMatcher()
	if err != nil || matcher == nil {
		return err
	}
	field := "messages"
	if responses {
		field = "input"
		if instructions, ok := body["instructions"].(string); ok {
			body["instructions"] = matcher.ObfuscateText(instructions)
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
			message["content"] = obfuscateSystemContent(content, matcher)
		}
	}
	return nil
}
