package agents

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type continuityExtractorLLM struct {
	responses []string
	calls     int
	user      string
	err       error
}

func (f *continuityExtractorLLM) Generate(_ context.Context, _, user string) (string, error) {
	f.calls++
	f.user = user
	if f.err != nil {
		return "", f.err
	}
	index := f.calls - 1
	if index >= len(f.responses) {
		index = len(f.responses) - 1
	}
	return f.responses[index], nil
}

func (*continuityExtractorLLM) StreamGenerate(context.Context, string, string, func(string) error) error {
	return nil
}

func TestValidateContinuityPacketRejectsNil(t *testing.T) {
	if err := ValidateContinuityPacket(nil); err == nil || err.Error() != "continuity packet is nil" {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateContinuityPacketNormalizesValidPacket(t *testing.T) {
	packet := ContinuityPacket{
		LastBeat:   "  结尾动作  ",
		OpenLoops:  []string{"  悬念一  ", "", "悬念二"},
		NextAction: "  下一步动作  ",
	}
	if err := ValidateContinuityPacket(&packet); err != nil {
		t.Fatal(err)
	}
	if packet.LastBeat != "结尾动作" || packet.NextAction != "下一步动作" ||
		!reflect.DeepEqual(packet.OpenLoops, []string{"悬念一", "悬念二"}) {
		t.Fatalf("packet = %#v", packet)
	}
}

func TestValidateContinuityPacketIgnoresBlankLoopsBeforeCounting(t *testing.T) {
	packet := ContinuityPacket{
		LastBeat:   "结尾动作",
		OpenLoops:  []string{"一", "二", "三", "  "},
		NextAction: "下一步动作",
	}
	if err := ValidateContinuityPacket(&packet); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(packet.OpenLoops, []string{"一", "二", "三"}) {
		t.Fatalf("packet = %#v", packet)
	}
}

func TestValidateContinuityPacketAllowsEmptyOpenLoops(t *testing.T) {
	packet := ContinuityPacket{LastBeat: "结尾动作", NextAction: "下一步动作"}
	if err := ValidateContinuityPacket(&packet); err != nil {
		t.Fatal(err)
	}
}

func TestValidateContinuityPacketRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		packet ContinuityPacket
		want   string
	}{
		{name: "missing last beat", packet: ContinuityPacket{NextAction: "下一步"}, want: "last_beat is required"},
		{name: "missing next action", packet: ContinuityPacket{LastBeat: "结尾"}, want: "next_action is required"},
		{name: "long last beat", packet: ContinuityPacket{LastBeat: strings.Repeat("字", maxContinuityTextRunes+1), NextAction: "下一步"}, want: "last_beat exceeds"},
		{name: "long next action", packet: ContinuityPacket{LastBeat: "结尾", NextAction: strings.Repeat("字", maxContinuityTextRunes+1)}, want: "next_action exceeds"},
		{name: "long loop", packet: ContinuityPacket{LastBeat: "结尾", OpenLoops: []string{strings.Repeat("字", maxContinuityTextRunes+1)}, NextAction: "下一步"}, want: "open_loops exceeds"},
		{name: "too many loops", packet: ContinuityPacket{LastBeat: "结尾", OpenLoops: []string{"一", "二", "三", "四"}, NextAction: "下一步"}, want: "at most 3"},
		{name: "duplicate loops", packet: ContinuityPacket{LastBeat: "结尾", OpenLoops: []string{"悬念", " 悬念 "}, NextAction: "下一步"}, want: "duplicate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateContinuityPacket(&test.packet); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestContinuityExtractorExtractsFinalDraftPacket(t *testing.T) {
	llm := &continuityExtractorLLM{responses: []string{`{"last_beat":" 主角推开密门。 ","open_loops":[" 门后是谁 ",""],"next_action":" 主角跨入密门。 "}`}}
	state := &GenerationState{
		Draft:     "主角推开密门。门后是谁。主角跨入密门。",
		Outline:   "本章大纲",
		SceneCard: "场景卡",
		PreviousContinuity: ContinuityPacket{
			NextAction: "检查密门",
		},
	}

	got, err := NewContinuityExtractor(llm).Extract(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if got.Continuity.LastBeat != "主角推开密门。" || got.Continuity.NextAction != "主角跨入密门。" || len(got.Continuity.OpenLoops) != 1 {
		t.Fatalf("continuity = %#v", got.Continuity)
	}
	if !strings.Contains(llm.user, "最终正文") || !strings.Contains(llm.user, "检查密门") {
		t.Fatalf("extractor prompt = %s", llm.user)
	}
}

func TestContinuityExtractorRepairsInvalidPacketOnce(t *testing.T) {
	llm := &continuityExtractorLLM{responses: []string{
		`{"last_beat":"","open_loops":[],"next_action":""}`,
		`{"last_beat":"结尾动作","open_loops":[],"next_action":"下一步动作"}`,
	}}

	if _, err := NewContinuityExtractor(llm).Extract(context.Background(), &GenerationState{Draft: "结尾动作。下一步动作"}); err != nil {
		t.Fatal(err)
	}
	if llm.calls != 2 {
		t.Fatalf("calls = %d, want 2", llm.calls)
	}
}

func TestValidateContinuityPacketAgainstDraftRejectsUnsupportedEvidence(t *testing.T) {
	draft := "主角推开密门。门后是谁。主角跨入密门。"
	base := ContinuityPacket{
		LastBeat:   "主角推开密门。",
		OpenLoops:  []string{"门后是谁。"},
		NextAction: "主角跨入密门。",
	}
	for _, test := range []struct {
		name   string
		packet ContinuityPacket
		want   string
	}{
		{name: "last beat", packet: ContinuityPacket{LastBeat: "不存在的结尾", OpenLoops: base.OpenLoops, NextAction: base.NextAction}, want: "last_beat"},
		{name: "next action", packet: ContinuityPacket{LastBeat: base.LastBeat, OpenLoops: base.OpenLoops, NextAction: "不存在的动作"}, want: "next_action"},
		{name: "open loop", packet: ContinuityPacket{LastBeat: base.LastBeat, OpenLoops: []string{"不存在的悬念"}, NextAction: base.NextAction}, want: "open_loops"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateContinuityPacketAgainstDraft(&test.packet, draft); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateContinuityPacketAgainstDraftAllowsExactEvidence(t *testing.T) {
	packet := ContinuityPacket{
		LastBeat:   "主角推开密门。",
		OpenLoops:  []string{"门后是谁。"},
		NextAction: "主角跨入密门。",
	}
	if err := ValidateContinuityPacketAgainstDraft(&packet, "主角推开密门。门后是谁。主角跨入密门。"); err != nil {
		t.Fatal(err)
	}
}

func TestContinuityExtractorRepairsUnsupportedEvidenceOnce(t *testing.T) {
	llm := &continuityExtractorLLM{responses: []string{
		`{"last_beat":"不存在的结尾","open_loops":[],"next_action":"不存在的动作"}`,
		`{"last_beat":"主角推开密门。","open_loops":["门后是谁。"],"next_action":"主角跨入密门。"}`,
	}}
	state := &GenerationState{Draft: "主角推开密门。门后是谁。主角跨入密门。"}
	got, err := NewContinuityExtractor(llm).Extract(context.Background(), state)
	if err != nil || llm.calls != 2 {
		t.Fatalf("state = %#v, err = %v, calls = %d", got, err, llm.calls)
	}
	if got.Continuity.LastBeat != "主角推开密门。" {
		t.Fatalf("continuity = %#v", got.Continuity)
	}
}

func TestContinuityExtractorInvalidEvidencePreservesPacket(t *testing.T) {
	llm := &continuityExtractorLLM{responses: []string{
		`{"last_beat":"不存在的结尾","open_loops":[],"next_action":"不存在的动作"}`,
		`{"last_beat":"仍不存在","open_loops":[],"next_action":"仍不存在"}`,
	}}
	state := &GenerationState{
		Draft:      "正文",
		Continuity: ContinuityPacket{LastBeat: "旧结尾", NextAction: "旧动作"},
	}
	got, err := NewContinuityExtractor(llm).Extract(context.Background(), state)
	if err == nil || llm.calls != 2 {
		t.Fatalf("state = %#v, err = %v, calls = %d", got, err, llm.calls)
	}
	if got.Continuity.LastBeat != "旧结尾" || got.Continuity.NextAction != "旧动作" {
		t.Fatalf("continuity changed: %#v", got.Continuity)
	}
}
func TestContinuityExtractorDoesNotReplacePacketOnFailure(t *testing.T) {
	providerErr := errors.New("provider failed")
	state := &GenerationState{
		Draft:      "正文",
		Continuity: ContinuityPacket{LastBeat: "旧结尾", NextAction: "旧动作"},
	}
	got, err := NewContinuityExtractor(&continuityExtractorLLM{err: providerErr}).Extract(context.Background(), state)
	if !errors.Is(err, providerErr) {
		t.Fatalf("error = %v", err)
	}
	if got.Continuity.LastBeat != "旧结尾" || got.Continuity.NextAction != "旧动作" {
		t.Fatalf("continuity changed: %#v", got.Continuity)
	}
}
