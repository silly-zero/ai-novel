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

	for _, reviewer := range []config.ReviewerConfig{
		{},
		{Model: "   "},
		{Model: "default-model"},
		{Model: " default-model "},
	} {
		if got, ok := reviewerChatConfig(chat, reviewer); ok || got.Model != "" {
			t.Fatalf("reviewerChatConfig(%#v) = %#v, %v; want reuse", reviewer, got, ok)
		}
	}

	got, ok := reviewerChatConfig(chat, config.ReviewerConfig{Model: " reviewer-model "})
	if !ok {
		t.Fatal("different reviewer model reused default adapter")
	}
	if got.APIKey != chat.APIKey || got.BaseURL != chat.BaseURL ||
		got.Model != "reviewer-model" || got.MaxTokens != chat.MaxTokens ||
		got.Timeout != chat.Timeout || got.Temperature != nil {
		t.Fatalf("reviewer config = %#v", got)
	}

	temperature := float32(0)
	got, ok = reviewerChatConfig(chat, config.ReviewerConfig{Temperature: &temperature})
	if !ok || got.Model != chat.Model || got.Temperature == nil || *got.Temperature != 0 {
		t.Fatalf("zero-temperature reviewer config = %#v, %v", got, ok)
	}
}
