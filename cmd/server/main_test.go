package main

import (
	"testing"
	"time"

	"github.com/ai-novel/studio/internal/infrastructure/config"
)

func TestReviewerChatConfig(t *testing.T) {
	chat := config.ChatConfig{
		APIKey:    "secret",
		BaseURL:   "https://example.test/v1",
		Model:     "default-model",
		MaxTokens: 4096,
		Timeout:   2 * time.Minute,
	}

	for _, reviewerModel := range []string{"", "   ", "default-model", " default-model "} {
		if got, ok := reviewerChatConfig(chat, reviewerModel); ok || got.Model != "" {
			t.Fatalf("reviewerChatConfig(%q) = %#v, %v; want reuse", reviewerModel, got, ok)
		}
	}

	got, ok := reviewerChatConfig(chat, " reviewer-model ")
	if !ok {
		t.Fatal("different reviewer model reused default adapter")
	}
	if got.APIKey != chat.APIKey || got.BaseURL != chat.BaseURL ||
		got.Model != "reviewer-model" || got.MaxTokens != chat.MaxTokens ||
		got.Timeout != chat.Timeout {
		t.Fatalf("reviewer config = %#v", got)
	}
}
