package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ai-novel/studio/ent"
	"github.com/ai-novel/studio/internal/infrastructure/database"
)

const (
	exitOK        = 0
	exitViolation = 2
	exitError     = 1
)

type diagnosticOutput struct {
	Check      string                           `json:"check"`
	Status     string                           `json:"status"`
	GroupCount int                              `json:"group_count"`
	Groups     []database.DuplicateChapterOrder `json:"groups"`
	Error      string                           `json:"error,omitempty"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("check-chapter-duplicates", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dsn := flags.String("dsn", "", "PostgreSQL DSN for the existing database (or AI_NOVEL_POSTGRES_DSN)")
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitError
	}
	if flags.NArg() > 0 {
		writeDiagnosticError(stdout, *format, "unexpected positional arguments")
		return exitError
	}
	resolvedDSN := strings.TrimSpace(*dsn)
	if resolvedDSN == "" {
		resolvedDSN = strings.TrimSpace(os.Getenv("AI_NOVEL_POSTGRES_DSN"))
	}
	if resolvedDSN == "" {
		writeDiagnosticError(stdout, *format, "database DSN is required")
		return exitError
	}
	if *format != "text" && *format != "json" {
		writeDiagnosticError(stdout, "text", "format must be text or json")
		return exitError
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := ent.Open("postgres", resolvedDSN)
	if err == nil {
		defer client.Close()
	}
	if err != nil {
		writeDiagnosticError(stdout, *format, "database unavailable")
		return exitError
	}
	groups, err := database.FindDuplicateChapterOrders(ctx, client)
	if err != nil {
		writeDiagnosticError(stdout, *format, "chapter order check failed")
		return exitError
	}
	status := "ok"
	code := exitOK
	if len(groups) > 0 {
		status = "violation"
		code = exitViolation
	}
	writeDiagnostic(stdout, *format, diagnosticOutput{Check: "duplicate_chapter_order", Status: status, GroupCount: len(groups), Groups: groups})
	return code
}

func writeDiagnosticError(stdout io.Writer, format, message string) {
	writeDiagnostic(stdout, format, diagnosticOutput{Check: "duplicate_chapter_order", Status: "error", Groups: []database.DuplicateChapterOrder{}, Error: message})
}

func writeDiagnostic(stdout io.Writer, format string, output diagnosticOutput) {
	if format == "json" {
		data, _ := json.Marshal(output)
		_, _ = stdout.Write(append(data, '\n'))
		return
	}
	if output.Status == "error" {
		_, _ = fmt.Fprintf(stdout, "check: %s\nstatus: error\nerror: %s\n", output.Check, output.Error)
		return
	}
	_, _ = fmt.Fprintf(stdout, "check: %s\nstatus: %s\n", output.Check, output.Status)
	for _, group := range output.Groups {
		ids := make([]string, len(group.ChapterIDs))
		for index, id := range group.ChapterIDs {
			ids[index] = fmt.Sprintf("%d", id)
		}
		_, _ = fmt.Fprintf(stdout, "novel_id=%d order=%d chapter_ids=%s\n", group.NovelID, group.Order, strings.Join(ids, ","))
	}
	_, _ = fmt.Fprintf(stdout, "groups: %d\n", output.GroupCount)
}
