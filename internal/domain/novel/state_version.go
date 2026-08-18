package novel

import (
	"fmt"
	"strings"
)

type ChapterStateRef struct {
	NovelID      string
	ChapterID    string
	ChapterIndex int
	GenerationID string
}

func (r *ChapterStateRef) Normalize() {
	r.NovelID = strings.TrimSpace(r.NovelID)
	r.ChapterID = strings.TrimSpace(r.ChapterID)
	r.GenerationID = strings.TrimSpace(r.GenerationID)
}

func (r ChapterStateRef) Validate() error {
	if strings.TrimSpace(r.NovelID) == "" {
		return fmt.Errorf("novel id is required")
	}
	if strings.TrimSpace(r.ChapterID) == "" {
		return fmt.Errorf("chapter id is required")
	}
	if r.ChapterIndex <= 0 {
		return fmt.Errorf("chapter index must be positive")
	}
	if strings.TrimSpace(r.GenerationID) == "" {
		return fmt.Errorf("generation id is required")
	}
	return nil
}
