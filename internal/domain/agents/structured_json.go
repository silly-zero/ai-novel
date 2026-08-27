package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	structuredResponsePreviewRunes = 512
	structuredJSONRecoveryLimit    = 32
)

var fencedJSONPattern = regexp.MustCompile("(?is)```[ \\t]*(?:json)?[ \\t]*\\r?\\n(.*?)```")

type structuredResponseError struct {
	agent    string
	attempts int
	cause    error
	preview  string
}

func (e *structuredResponseError) Error() string {
	return fmt.Sprintf(
		"%s structured response invalid after %d attempts: %v; response preview: %q",
		e.agent,
		e.attempts,
		e.cause,
		e.preview,
	)
}

func (e *structuredResponseError) SafeDiagnosticCode() string {
	return "structured_response_invalid"
}

func (e *structuredResponseError) Unwrap() error {
	return e.cause
}

type structuredDecoder[T any] func([]byte) (T, error)
type structuredValidator[T any] func(*T) error

func generateStructuredResponse[T any](
	ctx context.Context,
	llm LLMService,
	agentName string,
	systemPrompt string,
	userPrompt string,
	decode structuredDecoder[T],
	validate structuredValidator[T],
) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}

	response, err := llm.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return zero, fmt.Errorf("%s structured request: %w", agentName, err)
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}

	value, parseErr := parseStructuredResponse(response, decode, validate)
	if parseErr == nil {
		return value, nil
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}

	repairSystemPrompt := systemPrompt + `

你正在修复上一次输出的格式或校验错误。请严格遵守上述全部业务规则，只返回一个符合格式且通过校验的 JSON 对象或数组，不要输出解释、Markdown 代码围栏、注释或其他文字。`
	repairUserPrompt := fmt.Sprintf(
		"%s\n\n上一次响应无法解析或校验。请修复格式或校验错误，并仅返回符合全部规则的完整 JSON。原因：%s\n<previous_response>\n%s\n</previous_response>",
		userPrompt,
		boundedText(parseErr.Error(), structuredResponsePreviewRunes),
		boundedText(response, structuredResponsePreviewRunes),
	)

	repairedResponse, err := llm.Generate(ctx, repairSystemPrompt, repairUserPrompt)
	if err != nil {
		return zero, fmt.Errorf("%s structured repair request: %w", agentName, err)
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}

	value, parseErr = parseStructuredResponse(repairedResponse, decode, validate)
	if parseErr == nil {
		return value, nil
	}
	return zero, &structuredResponseError{
		agent:    agentName,
		attempts: 2,
		cause:    parseErr,
		preview:  boundedText(repairedResponse, structuredResponsePreviewRunes),
	}
}

func decodeJSON[T any](candidate []byte) (T, error) {
	var value T
	if bytes.Equal(bytes.TrimSpace(candidate), []byte("null")) {
		return value, errors.New("JSON value must not be null")
	}
	if err := json.Unmarshal(candidate, &value); err != nil {
		return value, err
	}
	return value, nil
}

func parseStructuredResponse[T any](
	response string,
	decode structuredDecoder[T],
	validate structuredValidator[T],
) (T, error) {
	var zero T
	candidates := structuredJSONCandidates(response)
	if len(candidates) == 0 {
		return zero, errors.New("response does not contain a JSON object or array")
	}

	var lastErr error
	for _, candidate := range candidates {
		value, err := decode([]byte(candidate))
		if err != nil {
			lastErr = fmt.Errorf("decode JSON: %w", err)
			continue
		}
		if validate != nil {
			if err := validate(&value); err != nil {
				lastErr = fmt.Errorf("validate JSON: %w", err)
				continue
			}
		}
		return value, nil
	}
	if lastErr == nil {
		lastErr = errors.New("response does not contain valid JSON")
	}
	return zero, lastErr
}

func structuredJSONCandidates(response string) []string {
	trimmed := strings.TrimSpace(response)
	if trimmed == "" {
		return nil
	}

	candidates := make([]string, 0, 4)
	seen := make(map[string]struct{})
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		if _, ok := seen[candidate]; ok {
			return
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}

	add(trimmed)
	for _, match := range fencedJSONPattern.FindAllStringSubmatch(response, -1) {
		add(match[1])
	}
	for _, candidate := range balancedJSONCandidates(response) {
		add(candidate)
	}
	return candidates
}

func balancedJSONCandidates(response string) []string {
	candidates := make([]string, 0, 2)
	start := -1
	stack := make([]jsonDelimiter, 0, 4)
	inString := false
	escaped := false
	recoveries := make([]jsonCandidateSpan, 0, 4)

	for index := 0; index < len(response); index++ {
		current := response[index]
		if start < 0 {
			if current == '{' || current == '[' {
				start = index
				stack = append(stack[:0], jsonDelimiter{value: current, start: index})
			}
			continue
		}

		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch current {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch current {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, jsonDelimiter{value: current, start: index})
		case '}', ']':
			if len(stack) == 0 || !matchingJSONDelimiters(stack[len(stack)-1].value, current) {
				candidates = appendRecoveryCandidates(candidates, response, recoveries)
				start = -1
				stack = stack[:0]
				inString = false
				escaped = false
				recoveries = recoveries[:0]
				continue
			}
			opening := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				candidates = append(candidates, response[start:index+1])
				start = -1
				recoveries = recoveries[:0]
			} else if len(recoveries) < structuredJSONRecoveryLimit {
				recoveries = append(recoveries, jsonCandidateSpan{start: opening.start, end: index})
			}
		}
	}
	if start >= 0 {
		candidates = appendRecoveryCandidates(candidates, response, recoveries)
	}
	return candidates
}

type jsonDelimiter struct {
	value byte
	start int
}

type jsonCandidateSpan struct {
	start int
	end   int
}

func appendRecoveryCandidates(
	candidates []string,
	response string,
	recoveries []jsonCandidateSpan,
) []string {
	for index := len(recoveries) - 1; index >= 0; index-- {
		span := recoveries[index]
		candidates = append(candidates, response[span.start:span.end+1])
	}
	return candidates
}

func matchingJSONDelimiters(opening byte, closing byte) bool {
	return opening == '{' && closing == '}' || opening == '[' && closing == ']'
}

func boundedText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
