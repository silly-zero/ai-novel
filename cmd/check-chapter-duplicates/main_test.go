package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ai-novel/studio/internal/infrastructure/database"
)

func TestWriteDiagnosticFormatsStableJSON(t *testing.T) {
	var output bytes.Buffer
	writeDiagnostic(&output, "json", diagnosticOutput{
		Check: "duplicate_chapter_order", Status: "violation", GroupCount: 1,
		Groups: []database.DuplicateChapterOrder{{NovelID: 7, Order: 3, ChapterIDs: []int{11, 12}}},
	})
	var decoded diagnosticOutput
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Status != "violation" || decoded.GroupCount != 1 || decoded.Groups[0].ChapterIDs[1] != 12 {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestWriteDiagnosticFormatsText(t *testing.T) {
	var output bytes.Buffer
	writeDiagnostic(&output, "text", diagnosticOutput{Check: "duplicate_chapter_order", Status: "ok", Groups: []database.DuplicateChapterOrder{}})
	if got := output.String(); !strings.Contains(got, "status: ok") || !strings.Contains(got, "groups: 0") {
		t.Fatalf("output = %s", got)
	}
}

func TestRunRequiresDSN(t *testing.T) {
	var output bytes.Buffer
	if code := run([]string{"--format=json", "--dsn="}, &output, &output); code != exitError {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(output.String(), `"status":"error"`) || strings.Contains(output.String(), "postgres") {
		t.Fatalf("output = %s", output.String())
	}
}

func TestRunHelpDoesNotLeakEnvironmentDSN(t *testing.T) {
	const secretDSN = "postgres://user:secret@host/db"
	t.Setenv("AI_NOVEL_POSTGRES_DSN", secretDSN)
	var output bytes.Buffer
	if code := run([]string{"--help"}, &output, &output); code != exitOK {
		t.Fatalf("code = %d", code)
	}
	if strings.Contains(output.String(), secretDSN) || strings.Contains(output.String(), "secret") {
		t.Fatalf("help leaked DSN: %s", output.String())
	}
}

func TestRunRejectsPositionalArgumentsBeforeDatabaseAccess(t *testing.T) {
	var output bytes.Buffer
	if code := run([]string{"--dsn=postgres://127.0.0.1:1/db", "extra"}, &output, &output); code != exitError {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(output.String(), "unexpected positional arguments") {
		t.Fatalf("output = %s", output.String())
	}
}

func TestRunRejectsUnknownFormatBeforeDatabaseAccess(t *testing.T) {
	var output bytes.Buffer
	if code := run([]string{"--format=xml", "--dsn=postgres://secret"}, &output, &output); code != exitError {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(output.String(), "format must be text or json") || strings.Contains(output.String(), "secret") {
		t.Fatalf("output = %s", output.String())
	}
}
