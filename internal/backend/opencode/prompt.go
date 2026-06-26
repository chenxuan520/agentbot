package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/chenxuan520/agentbot/internal/backend"
)

const emptyOutputContinuePrompt = "继续"
const errorContinuePrompt = "继续"
const incompleteReplyContinuePrompt = "继续"
const abortTimeout = 15 * time.Second
const retryDelay = 2 * time.Second

// abortedReplyText is sent when opencode reports the in-flight generation was
// aborted (e.g. via the /abort command). The prompt loop returns it instead of
// auto-continuing, otherwise the abort could never actually stop the run.
const abortedReplyText = "已中断当前任务。"

// errPromptAborted marks an opencode MessageAbortedError so the retry loop can
// stop the run instead of treating the abort as a retryable transient failure.
var errPromptAborted = errors.New("opencode prompt aborted")

var retryStatusOnlyPattern = regexp.MustCompile(`^((已)?从失败处重试(?:并(?:核实|确认))?(?:成功|完成)|(已)?重试(?:成功|完成)(?:并(?:核实|确认)完成)?)$`)

type promptOutcome struct {
	result         backend.PromptResult
	continuePrompt string
}

func (c *Client) Prompt(ctx context.Context, workspacePath, sessionID, text string, attachments []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error) {
	promptText := text
	promptAttachments := attachments
	currentSessionID := sessionID
	for {
		outcome, err := c.promptOnce(ctx, workspacePath, currentSessionID, promptText, promptAttachments, options)
		if err != nil {
			// An explicit abort must end the run; auto-continuing here would make
			// /abort a no-op because the loop would immediately re-prompt "继续".
			if errors.Is(err, errPromptAborted) {
				return backend.PromptResult{SessionID: currentSessionID, ReplyText: abortedReplyText}, nil
			}
			if strings.TrimSpace(currentSessionID) != "" {
				abortCtx, cancel := context.WithTimeout(context.Background(), abortTimeout)
				_ = c.AbortSession(abortCtx, currentSessionID)
				cancel()
			}
			if !c.webAvailable(context.Background()) {
				return backend.PromptResult{}, err
			}
			promptText = errorContinuePrompt
			promptAttachments = nil
			if err := sleepWithContext(ctx, retryDelay); err != nil {
				return backend.PromptResult{}, err
			}
			continue
		}
		if !options.NoReply && isRetryStatusOnly(outcome.result.ReplyText) {
			currentSessionID = outcome.result.SessionID
			if currentSessionID == "" {
				currentSessionID = sessionID
			}
			promptText = incompleteReplyContinuePrompt
			promptAttachments = nil
			if err := sleepWithContext(ctx, retryDelay); err != nil {
				return backend.PromptResult{}, err
			}
			continue
		}
		if options.NoReply {
			return outcome.result, nil
		}
		if strings.TrimSpace(outcome.continuePrompt) == "" {
			return outcome.result, nil
		}
		currentSessionID = outcome.result.SessionID
		if currentSessionID == "" {
			currentSessionID = sessionID
		}
		promptText = outcome.continuePrompt
		promptAttachments = nil
		if err := sleepWithContext(ctx, retryDelay); err != nil {
			return backend.PromptResult{}, err
		}
	}
}

func (c *Client) promptOnce(ctx context.Context, workspacePath, sessionID, text string, attachments []backend.Attachment, options backend.PromptOptions) (promptOutcome, error) {
	query := url.Values{}
	query.Set("directory", workspacePath)

	parts := make([]map[string]string, 0, 1+len(attachments))
	parts = append(parts, map[string]string{
		"type": "text",
		"text": text,
	})
	for _, attachment := range attachments {
		parts = append(parts, map[string]string{
			"type":     "file",
			"mime":     attachment.Mime,
			"filename": attachment.Filename,
			"url":      attachment.URL,
		})
	}

	body := map[string]any{
		"parts": parts,
	}
	if options.NoReply {
		body["noReply"] = true
	}
	if strings.TrimSpace(options.System) != "" {
		body["system"] = options.System
	}

	data, err := json.Marshal(body)
	if err != nil {
		return promptOutcome{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/session/"+sessionID+"/message?"+query.Encode(), strings.NewReader(string(data)))
	if err != nil {
		return promptOutcome{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return promptOutcome{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyText, _ := readAll(resp.Body)
		return promptOutcome{}, fmt.Errorf("opencode prompt status: %s body=%s", resp.Status, strings.TrimSpace(bodyText))
	}

	responseBody, err := readAll(resp.Body)
	if err != nil {
		return promptOutcome{}, err
	}

	var payload struct {
		Info struct {
			SessionID string `json:"sessionID"`
			Role      string `json:"role"`
			Error     *struct {
				Name string `json:"name"`
				Data struct {
					Message string `json:"message"`
				} `json:"data"`
			} `json:"error"`
		} `json:"info"`
		Parts []map[string]any `json:"parts"`
	}
	if err := json.Unmarshal([]byte(responseBody), &payload); err != nil {
		return promptOutcome{}, fmt.Errorf("decode opencode prompt: %w body=%s", err, strings.TrimSpace(responseBody))
	}
	if payload.Info.Error != nil {
		message := strings.TrimSpace(payload.Info.Error.Data.Message)
		if message == "" {
			message = payload.Info.Error.Name
		}
		if isAbortError(payload.Info.Error.Name, message) {
			return promptOutcome{}, errPromptAborted
		}
		return promptOutcome{}, fmt.Errorf("opencode prompt error: %s", message)
	}

	reply := extractResponseText(payload.Parts)
	finishReason := extractFinishReason(payload.Parts)
	if options.NoReply {
		resolvedSessionID := payload.Info.SessionID
		if resolvedSessionID == "" {
			resolvedSessionID = sessionID
		}
		return promptOutcome{result: backend.PromptResult{SessionID: resolvedSessionID}}, nil
	}

	resolvedSessionID := payload.Info.SessionID
	if resolvedSessionID == "" {
		resolvedSessionID = sessionID
	}
	result := backend.PromptResult{SessionID: resolvedSessionID, ReplyText: reply}
	return promptOutcome{result: result, continuePrompt: continuePromptFor(finishReason, reply)}, nil
}

func readAll(reader io.Reader) (string, error) {
	data, err := io.ReadAll(reader)
	return string(data), err
}

func extractResponseText(parts []map[string]any) string {
	for i := len(parts) - 1; i >= 0; i-- {
		if strings.TrimSpace(asString(parts[i]["type"])) != "text" {
			continue
		}
		text := strings.TrimSpace(asString(parts[i]["text"]))
		if text != "" {
			return text
		}
	}
	return ""
}

func extractFinishReason(parts []map[string]any) string {
	for i := len(parts) - 1; i >= 0; i-- {
		if strings.TrimSpace(asString(parts[i]["type"])) != "step-finish" {
			continue
		}
		reason := strings.TrimSpace(asString(parts[i]["reason"]))
		if reason != "" {
			return reason
		}
	}
	return ""
}

func continuePromptFor(finishReason, reply string) string {
	switch strings.TrimSpace(finishReason) {
	case "tool-calls", "other":
		return incompleteReplyContinuePrompt
	}
	if strings.TrimSpace(reply) == "" {
		return emptyOutputContinuePrompt
	}
	return ""
}

// isAbortError reports whether an opencode prompt error describes an aborted
// generation (observed as {"name":"MessageAbortedError","data":{"message":"Aborted"}}).
// Matching on both name and message keeps it resilient across opencode versions.
func isAbortError(name, message string) bool {
	if strings.Contains(strings.ToLower(strings.TrimSpace(name)), "abort") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(message), "aborted")
}

func isRetryStatusOnly(reply string) bool {
	text := strings.TrimSpace(reply)
	if text == "" {
		return false
	}
	normalized := strings.TrimSpace(strings.TrimRight(text, "。.!！?？:：,，；;"))
	return retryStatusOnlyPattern.MatchString(normalized)
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func asString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
