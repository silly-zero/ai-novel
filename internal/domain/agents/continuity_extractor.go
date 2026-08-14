package agents

import (
	"context"
	"fmt"
	"strings"
)

type ContinuityExtractor struct {
	llm LLMService
}

const (
	maxContinuityTextRunes = 500
	maxContinuityOpenLoops = 3
)

func NewContinuityExtractor(llm LLMService) *ContinuityExtractor {
	return &ContinuityExtractor{llm: llm}
}

func (e *ContinuityExtractor) Extract(ctx context.Context, state *GenerationState) (*GenerationState, error) {
	if e == nil || e.llm == nil {
		return nil, fmt.Errorf("continuity extractor is not configured")
	}
	if state == nil || strings.TrimSpace(state.Draft) == "" {
		return nil, fmt.Errorf("continuity extractor requires a non-empty draft")
	}
	systemPrompt := `你是负责章节接力的小说编辑。请从最终章节正文中提取下一章必须使用的结构化接力包。
要求：
- last_beat：本章最后发生的关键动作或信息，1-2句，必须来自正文。
- open_loops：本章结束时仍未完成、可解决、可升级或可转化的问题/悬念，普通章节给1-3条；终局章节可以为空数组。
- next_action：下一章开场必须承接的具体动作，1句，必须能从本章结尾推出。
只返回合法 JSON，不要 Markdown 或解释：
{"last_beat":"...","open_loops":["..."],"next_action":"..."}`
	userPrompt := fmt.Sprintf("%s\n\n【本章大纲】\n%s\n\n【场景卡】\n%s\n\n【最终正文】\n%s",
		continuityPrompt(state.PreviousContinuity), state.Outline, state.SceneCard, state.Draft)
	packet, err := generateStructuredResponse(
		ctx, e.llm, "continuity extractor", systemPrompt, userPrompt,
		decodeContinuityPacket,
		func(packet *ContinuityPacket) error {
			return ValidateContinuityPacketAgainstDraft(packet, state.Draft)
		},
	)
	if err != nil {
		return state, fmt.Errorf("extract chapter continuity: %w", err)
	}
	state.Continuity = packet
	return state, nil
}

func decodeContinuityPacket(candidate []byte) (ContinuityPacket, error) {
	return decodeJSON[ContinuityPacket](candidate)
}

func ValidateContinuityPacket(packet *ContinuityPacket) error {
	if packet == nil {
		return fmt.Errorf("continuity packet is nil")
	}
	packet.LastBeat = strings.TrimSpace(packet.LastBeat)
	packet.NextAction = strings.TrimSpace(packet.NextAction)
	if packet.LastBeat == "" {
		return fmt.Errorf("last_beat is required")
	}
	if packet.NextAction == "" {
		return fmt.Errorf("next_action is required")
	}
	if err := validateContinuityText("last_beat", packet.LastBeat); err != nil {
		return err
	}
	if err := validateContinuityText("next_action", packet.NextAction); err != nil {
		return err
	}
	loops := make([]string, 0, len(packet.OpenLoops))
	seen := make(map[string]struct{}, len(packet.OpenLoops))
	for _, loop := range packet.OpenLoops {
		loop = strings.TrimSpace(loop)
		if loop == "" {
			continue
		}
		if err := validateContinuityText("open_loops", loop); err != nil {
			return err
		}
		if _, exists := seen[loop]; exists {
			return fmt.Errorf("open_loops must not contain duplicate items")
		}
		seen[loop] = struct{}{}
		loops = append(loops, loop)
	}
	if len(loops) > maxContinuityOpenLoops {
		return fmt.Errorf("open_loops must contain at most %d items", maxContinuityOpenLoops)
	}
	packet.OpenLoops = loops
	return nil
}

func ValidateContinuityPacketAgainstDraft(packet *ContinuityPacket, draft string) error {
	if strings.TrimSpace(draft) == "" {
		return fmt.Errorf("draft is required for continuity evidence validation")
	}
	if err := ValidateContinuityPacket(packet); err != nil {
		return err
	}
	if !strings.Contains(draft, packet.LastBeat) {
		return fmt.Errorf("last_beat must be an exact draft substring")
	}
	if !strings.Contains(draft, packet.NextAction) {
		return fmt.Errorf("next_action must be an exact draft substring")
	}
	for _, loop := range packet.OpenLoops {
		if !strings.Contains(draft, loop) {
			return fmt.Errorf("open_loops item must be an exact draft substring")
		}
	}
	return nil
}

func validateContinuityText(name, value string) error {
	if len([]rune(value)) > maxContinuityTextRunes {
		return fmt.Errorf("%s exceeds %d characters", name, maxContinuityTextRunes)
	}
	return nil
}
