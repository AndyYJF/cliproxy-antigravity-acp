package main

import (
	"encoding/json"
	"testing"
)

func TestIgnoredGenerationParameters(t *testing.T) {
	fields := object{
		"temperature": 0.8, "top_p": 0.9, "max_tokens": 100, "max_completion_tokens": 100,
		"max_output_tokens": 100, "stop": []string{"END"}, "seed": 42, "logprobs": true,
		"top_logprobs": 5, "presence_penalty": 0.2, "frequency_penalty": 0.2,
		"reasoning_effort": "high", "reasoning": object{"effort": "high"},
		"response_format": object{"type": "json_object"}, "text": object{"format": object{"type": "json_schema"}},
		"prediction": object{"type": "content", "content": "ignored"}, "logit_bias": object{"1": 20}, "n": 2, "truncation": "auto",
	}
	chat := object{"model": "antigravity-acp/gemini-4-hydrogen", "messages": []object{{"role": "user", "content": "hello"}}}
	raw, _ := json.Marshal(chat)
	_, basePrompt, err := promptBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	basePlan, err := planChatAgent(raw)
	if err != nil {
		t.Fatal(err)
	}
	responses := object{"model": chat["model"], "input": "hello"}
	baseRaw, _ := json.Marshal(responses)
	baseResponse, err := planResponse(baseRaw)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		t.Run(key, func(t *testing.T) {
			chat[key] = value
			raw, _ := json.Marshal(chat)
			_, prompt, err := promptBody(raw)
			if err != nil || prompt != basePrompt {
				t.Fatal("plain chat changed", err)
			}
			plan, err := planChatAgent(raw)
			if err != nil || plan.Prompt != basePlan.Prompt {
				t.Fatal("agent chat changed", err)
			}
			delete(chat, key)
			responses[key] = value
			raw, _ = json.Marshal(responses)
			plan, err = planResponse(raw)
			if err != nil || plan.Prompt != baseResponse.Prompt {
				t.Fatal("responses changed", err)
			}
			delete(responses, key)
		})
	}
}
