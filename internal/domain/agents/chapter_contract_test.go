package agents

import (
	"strings"
	"testing"
)

func validChapterContract() ChapterContract {
	return ChapterContract{
		Goal:          "主角确认密门与身世有关",
		MustHappen:    []string{"主角进入密门", "主角发现旧王朝血书"},
		MustNotHappen: []string{"揭晓最终反派身份"},
		EndState:      "主角决定追踪血书指向的地下祭坛",
	}
}

func TestValidateChapterContractNormalizesFields(t *testing.T) {
	contract := ChapterContract{
		Goal:          "  查明密门来源  ",
		MustHappen:    []string{" 进入密门 ", " 发现血书 "},
		MustNotHappen: []string{" 揭晓最终真相 "},
		EndState:      " 前往地下祭坛 ",
	}

	if err := validateChapterContract(&contract); err != nil {
		t.Fatal(err)
	}
	if contract.Goal != "查明密门来源" ||
		contract.EndState != "前往地下祭坛" ||
		contract.MustHappen[0] != "进入密门" ||
		contract.MustNotHappen[0] != "揭晓最终真相" {
		t.Fatalf("normalized contract = %#v", contract)
	}
}

func TestValidateChapterContractRejectsInvalidFields(t *testing.T) {
	tests := []ChapterContract{
		{MustHappen: []string{"事件"}, EndState: "结尾"},
		{Goal: "目标", EndState: "结尾"},
		{Goal: "目标", MustHappen: []string{" "}, EndState: "结尾"},
		{Goal: "目标", MustHappen: []string{"事件"}, MustNotHappen: []string{" "}, EndState: "结尾"},
		{Goal: "目标", MustHappen: []string{"1", "2", "3", "4", "5", "6"}, EndState: "结尾"},
		{Goal: "目标", MustHappen: []string{"事件"}, EndState: ""},
		{Goal: strings.Repeat("字", maxChapterContractItemRunes+1), MustHappen: []string{"事件"}, EndState: "结尾"},
	}
	for _, contract := range tests {
		if err := validateChapterContract(&contract); err == nil {
			t.Fatalf("contract was accepted: %#v", contract)
		}
	}
}

func TestFormatChapterContractIsStable(t *testing.T) {
	got := chapterContractPrompt(validChapterContract())
	for _, value := range []string{
		"【本章契约】",
		"章节目标：主角确认密门与身世有关",
		"- 主角进入密门",
		"- 主角发现旧王朝血书",
		"- 揭晓最终反派身份",
		"章尾状态：主角决定追踪血书指向的地下祭坛",
	} {
		if !strings.Contains(got, value) {
			t.Fatalf("contract prompt missing %q: %s", value, got)
		}
	}
}
