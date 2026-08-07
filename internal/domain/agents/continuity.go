package agents

import (
	"fmt"
	"strings"
)

func continuityPrompt(packet ContinuityPacket) string {
	if packet.IsEmpty() {
		return "（上一章接力状态：无；这是第一章或上一章尚未生成结构化接力包。）"
	}
	loops := "（无）"
	if len(packet.OpenLoops) > 0 {
		items := make([]string, 0, len(packet.OpenLoops))
		for _, loop := range packet.OpenLoops {
			if value := strings.TrimSpace(loop); value != "" {
				items = append(items, "- "+value)
			}
		}
		if len(items) > 0 {
			loops = strings.Join(items, "\n")
		}
	}
	return fmt.Sprintf("【上一章接力状态】\nLastBeat：%s\nOpenLoops：\n%s\nNextAction：%s", strings.TrimSpace(packet.LastBeat), loops, strings.TrimSpace(packet.NextAction))
}
