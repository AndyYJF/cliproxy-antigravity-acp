package main

import (
	"bytes"
	"cliproxy-antigravity-acp/internal/cpacloak"
	cpaconfig "cliproxy-antigravity-acp/internal/cpacloak/config"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"regexp"
)

func normalizePromptRules(r cpaPromptRules) cpaPromptRules {
	if r.Words == nil {
		r.Words = []string{}
	}
	if r.Replacements == nil {
		r.Replacements = map[string]string{}
	}
	if r.Patterns == nil {
		r.Patterns = []cpaconfig.SensitivePatternRule{}
	}
	return r
}
func rulesRevision(r cpaPromptRules) string {
	raw, _ := json.Marshal(normalizePromptRules(r))
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func promptRulesResponse() (any, error) {
	path := settings().CPAConfigFile
	if path == "" {
		return object{"configured": false, "rules": normalizePromptRules(cpaPromptRules{})}, nil
	}
	promptMatcherCache.Lock()
	defer promptMatcherCache.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("无法读取 CPA 共享配置")
	}
	rules, err := readCPAPromptRules(raw)
	if err != nil {
		return nil, errors.New("无法解析 CPA 共享配置")
	}
	return object{"configured": true, "rules": normalizePromptRules(rules), "revision": rulesRevision(rules)}, nil
}
func ruleMapping(parent *yaml.Node, key string) (*yaml.Node, error) {
	if parent.Kind != yaml.MappingNode {
		return nil, errors.New("CPA 配置节点不是映射")
	}
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			node := parent.Content[i+1]
			if node.Tag == "!!null" {
				*node = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			if node.Kind != yaml.MappingNode {
				return nil, errors.New("CPA 配置节点不是映射")
			}
			return node, nil
		}
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, node)
	return node, nil
}
func setRuleField(parent *yaml.Node, key string, value any) error {
	node := new(yaml.Node)
	if err := node.Encode(value); err != nil {
		return err
	}
	for i := 0; i < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			old := parent.Content[i+1]
			node.HeadComment = old.HeadComment
			node.LineComment = old.LineComment
			node.FootComment = old.FootComment
			parent.Content[i+1] = node
			return nil
		}
	}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, node)
	return nil
}
func savePromptRules(raw []byte) (any, error) {
	if len(raw) > 64<<10 {
		return nil, &failure{"request_error", "规则内容过大", 413}
	}
	var body struct {
		Rules    *cpaPromptRules `json:"rules"`
		Revision string          `json:"revision"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || body.Rules == nil || body.Revision == "" {
		return nil, &failure{"request_error", "规则格式或版本标识无效", 400}
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, &failure{"request_error", "规则请求包含多余内容", 400}
	}
	rules := normalizePromptRules(*body.Rules)
	for i, p := range rules.Patterns {
		if _, err := regexp.Compile(p.Pattern); err != nil {
			return nil, &failure{"request_error", fmt.Sprintf("第 %d 条正则无效：%v", i+1, err), 400}
		}
	}
	path := settings().CPAConfigFile
	if path == "" {
		return nil, &failure{"request_error", "请先配置 cpa_config_file 以共用 CPA 规则", 400}
	}
	promptMatcherCache.Lock()
	defer promptMatcherCache.Unlock()
	original, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("无法读取 CPA 共享配置")
	}
	existing, err := readCPAPromptRules(original)
	if err != nil {
		return nil, errors.New("无法解析 CPA 共享配置")
	}
	if rulesRevision(existing) != body.Revision {
		return nil, &failure{"conflict", "共享规则已被修改，请重新加载规则后再保存；当前输入已保留", 409}
	}
	var doc yaml.Node
	if yaml.Unmarshal(original, &doc) != nil || len(doc.Content) != 1 {
		return nil, errors.New("无法解析 CPA 共享配置")
	}
	node := doc.Content[0]
	for _, key := range []string{"upstream", "providers", "antigravity"} {
		node, err = ruleMapping(node, key)
		if err != nil {
			return nil, err
		}
	}
	for _, field := range []struct {
		key   string
		value any
	}{{"sensitive-words", rules.Words}, {"sensitive-words-replace", rules.Replacements}, {"sensitive-patterns-replace", rules.Patterns}} {
		if err = setRuleField(node, field.key, field.value); err != nil {
			return nil, err
		}
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err = encoder.Encode(&doc); err != nil {
		return nil, err
	}
	_ = encoder.Close()
	// Preserve the inode because CPA commonly bind-mounts the config file itself.
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return nil, errors.New("无法写入 CPA 共享配置")
	}
	defer file.Close()
	if _, err = file.Write(out.Bytes()); err == nil {
		err = file.Truncate(int64(out.Len()))
	}
	if err == nil {
		err = file.Sync()
	}
	if err != nil {
		return nil, errors.New("保存 CPA 共享配置失败")
	}
	promptMatcherCache.path = ""
	return object{"configured": true, "rules": rules, "revision": rulesRevision(rules)}, nil
}

type updateEvent struct {
	Status          string `json:"status"`
	CheckedAt       string `json:"checked_at"`
	Version         string `json:"version,omitempty"`
	PreviousVersion string `json:"previous_version,omitempty"`
	Available       string `json:"available,omitempty"`
	FailedVersion   string `json:"failed_version,omitempty"`
	CPA             string `json:"cpa,omitempty"`
	Error           string `json:"error,omitempty"`
}
type updaterSnapshot struct {
	updateEvent
	Enabled    *bool        `json:"enabled,omitempty"`
	LastUpdate *updateEvent `json:"last_update,omitempty"`
}

func promptStatus() object {
	result := object{"cpa_version": cpacloak.UpstreamTag, "cpa_commit": cpacloak.UpstreamCommit}
	path := settings().UpdateStatusFile
	if path == "" {
		result["updater"] = object{"available": false, "message": "未接入自动更新状态"}
		return result
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		result["updater"] = object{"available": false, "message": "等待更新器首次记录状态"}
		return result
	}
	var data updaterSnapshot
	if err != nil || json.Unmarshal(raw, &data) != nil {
		result["updater"] = object{"available": false, "message": "自动更新状态暂时无法读取"}
		return result
	}
	result["updater"] = object{"available": true, "snapshot": data}
	return result
}
