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

type AnthropicProvider struct {
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
}

func (p *AnthropicProvider) GenerateCommitMessage(ctx context.Context, req CommitRequest) (string, error) {
	if p.Model == "" {
		return "", fmt.Errorf("anthropic model is empty")
	}
	body := map[string]any{
		"model":       p.Model,
		"max_tokens":  120,
		"temperature": 0.2,
		"system":      "You generate concise git commit messages and return JSON only.",
		"messages": []map[string]string{
			{"role": "user", "content": BuildPrompt(req)},
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(p.BaseURL, "/") + "/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("x-api-key", p.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("content-type", "application/json")

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
		return "", fmt.Errorf("anthropic API returned %s: %s", resp.Status, strings.TrimSpace(string(respData)))
	}

	// Check for error-style responses (e.g., Zhipu/GLM API)
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
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respData, &parsed); err != nil {
		return "", err
	}
	var text strings.Builder
	for _, block := range parsed.Content {
		if block.Text != "" {
			text.WriteString(block.Text)
			text.WriteString("\n")
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", fmt.Errorf("anthropic API returned no text")
	}
	return ExtractCommitMessage(text.String())
}

func (p *AnthropicProvider) GenerateMergeResolution(ctx context.Context, req MergeResolutionRequest) (MergeResolutionResult, error) {
	if p.Model == "" {
		return MergeResolutionResult{}, fmt.Errorf("anthropic model is empty")
	}
	body := map[string]any{
		"model":       p.Model,
		"max_tokens":  8000,
		"temperature": 0.1,
		"system":      "You resolve git merge conflicts and return JSON only.",
		"messages": []map[string]string{
			{"role": "user", "content": BuildMergeResolutionPrompt(req)},
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return MergeResolutionResult{}, err
	}
	endpoint := strings.TrimRight(p.BaseURL, "/") + "/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return MergeResolutionResult{}, err
	}
	httpReq.Header.Set("x-api-key", p.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("content-type", "application/json")

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
		return MergeResolutionResult{}, fmt.Errorf("anthropic API returned %s: %s", resp.Status, strings.TrimSpace(string(respData)))
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
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respData, &parsed); err != nil {
		return MergeResolutionResult{}, err
	}
	var text strings.Builder
	for _, block := range parsed.Content {
		if block.Text != "" {
			text.WriteString(block.Text)
			text.WriteString("\n")
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return MergeResolutionResult{}, fmt.Errorf("anthropic API returned no text")
	}
	return ExtractMergeResolution(text.String())
}

func (p *AnthropicProvider) GenerateRepairAction(ctx context.Context, req RepairAgentRequest) (RepairAgentAction, error) {
	if p.Model == "" {
		return RepairAgentAction{}, fmt.Errorf("anthropic model is empty")
	}
	body := map[string]any{
		"model":       p.Model,
		"max_tokens":  8000,
		"temperature": 0.1,
		"system":      "You control a safe repository repair agent and return one JSON action only.",
		"messages": []map[string]string{
			{"role": "user", "content": BuildRepairAgentPrompt(req)},
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return RepairAgentAction{}, err
	}
	endpoint := strings.TrimRight(p.BaseURL, "/") + "/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return RepairAgentAction{}, err
	}
	httpReq.Header.Set("x-api-key", p.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("content-type", "application/json")

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
		return RepairAgentAction{}, fmt.Errorf("anthropic API returned %s: %s", resp.Status, strings.TrimSpace(string(respData)))
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
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respData, &parsed); err != nil {
		return RepairAgentAction{}, err
	}
	var text strings.Builder
	for _, block := range parsed.Content {
		if block.Text != "" {
			text.WriteString(block.Text)
			text.WriteString("\n")
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return RepairAgentAction{}, fmt.Errorf("anthropic API returned no text")
	}
	return ExtractRepairAgentAction(text.String())
}
