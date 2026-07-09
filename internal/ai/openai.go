package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ChatCompletionsProvider struct {
	Name    string
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
}

func (p *ChatCompletionsProvider) GenerateCommitMessage(ctx context.Context, req CommitRequest) (string, error) {
	if p.Model == "" {
		return "", fmt.Errorf("%s model is empty", p.Name)
	}
	body := map[string]any{
		"model": p.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "You generate concise git commit messages and return JSON only."},
			{"role": "user", "content": BuildPrompt(req)},
		},
		"temperature": 0.2,
		"max_tokens":  120,
		"response_format": map[string]string{
			"type": "json_object",
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	endpoint := strings.TrimRight(p.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respData, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s API returned %s: %s", p.Name, resp.Status, strings.TrimSpace(string(respData)))
	}

	// Check for error-style responses from compatible APIs (e.g., Zhipu/GLM, Baidu Qianfan)
	var errorResp struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respData, &errorResp); err == nil && errorResp.Type == "error" {
		return "", fmt.Errorf("API error: %s (code: %s)", errorResp.Error.Message, errorResp.Error.Code)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respData, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("%s API returned no message", p.Name)
	}
	return ExtractCommitMessage(parsed.Choices[0].Message.Content)
}

func (p *ChatCompletionsProvider) GenerateMergeResolution(ctx context.Context, req MergeResolutionRequest) (MergeResolutionResult, error) {
	if p.Model == "" {
		return MergeResolutionResult{}, fmt.Errorf("%s model is empty", p.Name)
	}
	body := map[string]any{
		"model": p.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "You resolve git merge conflicts and return JSON only."},
			{"role": "user", "content": BuildMergeResolutionPrompt(req)},
		},
		"temperature": 0.1,
		"max_tokens":  8000,
		"response_format": map[string]string{
			"type": "json_object",
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return MergeResolutionResult{}, err
	}

	endpoint := strings.TrimRight(p.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return MergeResolutionResult{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return MergeResolutionResult{}, err
	}
	defer resp.Body.Close()
	respData, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return MergeResolutionResult{}, fmt.Errorf("%s API returned %s: %s", p.Name, resp.Status, strings.TrimSpace(string(respData)))
	}

	var errorResp struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respData, &errorResp); err == nil && errorResp.Type == "error" {
		return MergeResolutionResult{}, fmt.Errorf("API error: %s (code: %s)", errorResp.Error.Message, errorResp.Error.Code)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respData, &parsed); err != nil {
		return MergeResolutionResult{}, err
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return MergeResolutionResult{}, fmt.Errorf("%s API returned no message", p.Name)
	}
	return ExtractMergeResolution(parsed.Choices[0].Message.Content)
}

func (p *ChatCompletionsProvider) GenerateRepairAction(ctx context.Context, req RepairAgentRequest) (RepairAgentAction, error) {
	if p.Model == "" {
		return RepairAgentAction{}, fmt.Errorf("%s model is empty", p.Name)
	}
	body := map[string]any{
		"model": p.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "You control a safe repository repair agent and return one JSON action only."},
			{"role": "user", "content": BuildRepairAgentPrompt(req)},
		},
		"temperature": 0.1,
		"max_tokens":  8000,
		"response_format": map[string]string{
			"type": "json_object",
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return RepairAgentAction{}, err
	}

	endpoint := strings.TrimRight(p.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return RepairAgentAction{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return RepairAgentAction{}, err
	}
	defer resp.Body.Close()
	respData, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RepairAgentAction{}, fmt.Errorf("%s API returned %s: %s", p.Name, resp.Status, strings.TrimSpace(string(respData)))
	}

	var errorResp struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respData, &errorResp); err == nil && errorResp.Type == "error" {
		return RepairAgentAction{}, fmt.Errorf("API error: %s (code: %s)", errorResp.Error.Message, errorResp.Error.Code)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respData, &parsed); err != nil {
		return RepairAgentAction{}, err
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return RepairAgentAction{}, fmt.Errorf("%s API returned no message", p.Name)
	}
	return ExtractRepairAgentAction(parsed.Choices[0].Message.Content)
}
