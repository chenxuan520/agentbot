package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/chenxuan520/agentbot/internal/backend"
)

const v2UnknownSourcePrefix = "opencode:v2:unknown:"

const v2ContinueAfterIncompleteStream = "The previous response was interrupted. Continue from where you left off without repeating completed content."
const v2ContinueAfterOutputLimit = "Your last response hit the output token limit (stop reason: length). Do not apologize, recap, or repeat yourself. Break the remaining work into smaller pieces."

type v2NativeMessage struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Text     string            `json:"text"`
	Metadata map[string]any    `json:"metadata"`
	Files    []json.RawMessage `json:"files"`
	Skills   []struct {
		Text string `json:"text"`
	} `json:"skills"`
	Agent string `json:"agent"`
	Model struct {
		ProviderID string `json:"providerID"`
		ID         string `json:"id"`
		Variant    string `json:"variant"`
	} `json:"model"`
	Content []v2NativePart  `json:"content"`
	Finish  string          `json:"finish"`
	Error   json.RawMessage `json:"error"`
	Retry   json.RawMessage `json:"retry"`
	Summary json.RawMessage `json:"summary"`
	Outcome string          `json:"outcome"`
	Time    struct {
		Created   int64 `json:"created"`
		Streamed  int64 `json:"streamed"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Tokens struct {
		Input     float64 `json:"input"`
		Output    float64 `json:"output"`
		Reasoning float64 `json:"reasoning"`
		Cache     struct {
			Read  float64 `json:"read"`
			Write float64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
}

type v2NativePart struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Executed  bool   `json:"executed"`
	Synthetic bool   `json:"synthetic"`
	Ignored   bool   `json:"ignored"`
	State     struct {
		Status   string          `json:"status"`
		Input    json.RawMessage `json:"input"`
		Metadata map[string]any  `json:"metadata"`
		Error    json.RawMessage `json:"error"`
		Content  []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			URI  string `json:"uri"`
			Mime string `json:"mime"`
			Name string `json:"name"`
		} `json:"content"`
	} `json:"state"`
	Time struct {
		Created   int64 `json:"created"`
		Ran       int64 `json:"ran"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

func (c *Client) v2GetSessionMessages(ctx context.Context, workspacePath, sessionID string) ([]backend.SessionMessage, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("opencode V2 get session messages requires session id")
	}
	query := url.Values{"limit": {"100"}, "order": {"asc"}}
	var items []v2NativeMessage
	cursors := make(map[string]bool)
	ids := make(map[string]bool)
	for {
		var response struct {
			Data   []v2NativeMessage `json:"data"`
			Cursor struct {
				Previous string `json:"previous"`
				Next     string `json:"next"`
			} `json:"cursor"`
		}
		if err := c.v2Request(ctx, http.MethodGet, "/api/session/"+url.PathEscape(sessionID)+"/message", query, nil, &response); err != nil {
			return nil, err
		}
		if response.Data == nil {
			return nil, fmt.Errorf("opencode V2 message history missing data")
		}
		for _, item := range response.Data {
			if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Type) == "" || ids[item.ID] {
				return nil, fmt.Errorf("opencode V2 message history has missing or duplicate identity")
			}
			ids[item.ID] = true
			items = append(items, item)
		}
		cursor := response.Cursor.Next
		if cursor == "" {
			break
		}
		if cursors[cursor] {
			return nil, fmt.Errorf("opencode V2 message history repeated cursor")
		}
		cursors[cursor] = true
		query = url.Values{"cursor": {cursor}}
	}
	return v2RebuildSources(items, sessionID), nil
}

func (c *Client) v2GetSessionMessage(ctx context.Context, workspacePath, sessionID, messageID string) (backend.SessionMessage, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(messageID) == "" {
		return backend.SessionMessage{}, fmt.Errorf("opencode V2 get session message requires session and message ids")
	}
	var response struct {
		Data v2NativeMessage `json:"data"`
	}
	if err := c.v2Request(ctx, http.MethodGet, "/api/session/"+url.PathEscape(sessionID)+"/message/"+url.PathEscape(messageID), nil, nil, &response); err != nil {
		return backend.SessionMessage{}, err
	}
	if response.Data.ID != messageID {
		return backend.SessionMessage{}, fmt.Errorf("opencode V2 get session message returned a different identity")
	}
	messages, err := c.v2GetSessionMessages(ctx, workspacePath, sessionID)
	if err != nil {
		return backend.SessionMessage{}, err
	}
	for _, historical := range messages {
		if historical.ID == messageID {
			message := response.Data.message(sessionID, historical.Sequence)
			message.ReplySuppressed = message.ReplySuppressed || historical.ReplySuppressed
			message.SourceMessageIDs = append([]string(nil), historical.SourceMessageIDs...)
			return message, nil
		}
	}
	return backend.SessionMessage{}, fmt.Errorf("opencode V2 get session message missing from complete history")
}

func (item v2NativeMessage) message(sessionID string, sequence int64) backend.SessionMessage {
	message := backend.SessionMessage{
		ID: item.ID, SessionID: sessionID, Native: true, Sequence: sequence,
		CreatedAt: item.Time.Created, CompletedAt: item.Time.Completed,
		Finish: item.Finish, HasError: v2HasValue(item.Error),
		ReplySuppressed: v2HasValue(item.Retry),
		Summary:         item.Type == "compaction" || item.Type == "assistant" && strings.TrimSpace(string(item.Summary)) == "true",
		Tokens: backend.TokenUsage{
			Total: int(item.Tokens.Input + item.Tokens.Output + item.Tokens.Reasoning + item.Tokens.Cache.Read + item.Tokens.Cache.Write),
			Input: int(item.Tokens.Input), Output: int(item.Tokens.Output), Reasoning: int(item.Tokens.Reasoning),
			CacheRead: int(item.Tokens.Cache.Read), CacheWrite: int(item.Tokens.Cache.Write),
		},
	}
	switch item.Type {
	case "assistant":
		message.Role = "assistant"
		message.Parts = make([]backend.SessionMessagePart, 0, len(item.Content))
		for _, part := range item.Content {
			value := backend.SessionMessagePart{Type: part.Type, Text: part.Text, Synthetic: part.Synthetic, Ignored: part.Ignored}
			if part.Type == "tool" {
				var input map[string]any
				_ = json.Unmarshal(part.State.Input, &input)
				value.Tool = part.Name
				value.ToolStatus = part.State.Status
				value.ToolInputSummary = summarizeToolInput(part.Name, input)
				value.ProviderExecuted = part.Executed
				if part.Name == "subagent" {
					value.ToolSessionID, _ = part.State.Metadata["sessionID"].(string)
					value.ToolBackground = part.State.Metadata["status"] == "running" || input["background"] == true
					value.ToolCancelled = part.State.Metadata["status"] == "cancelled"
				}
			}
			if part.Type != "text" && part.Type != "reasoning" && part.Type != "tool" {
				message.ReplySuppressed = true
			}
			message.Parts = append(message.Parts, value)
		}
		for i := len(message.Parts) - 1; i >= 0; i-- {
			part := message.Parts[i]
			if part.Type == "text" && !part.Synthetic && !part.Ignored && strings.TrimSpace(part.Text) != "" {
				message.ReplySuppressed = message.ReplySuppressed || isRetryStatusOnly(part.Text)
				break
			}
		}
	case "user":
		if item.projectedInput() {
			message.Role = "user"
		}
		if item.Text != "" {
			message.Parts = []backend.SessionMessagePart{{Type: "text", Text: item.Text}}
		}
	case "synthetic":
		message.Role = "user"
		message.Parts = []backend.SessionMessagePart{{Type: "text", Text: item.Text, Synthetic: true}}
	case "system":
		message.Role = "system"
		message.Parts = []backend.SessionMessagePart{{Type: "text", Text: item.Text, Synthetic: true}}
	}
	return message
}

func v2HasValue(value json.RawMessage) bool {
	return len(value) != 0 && strings.TrimSpace(string(value)) != "null"
}

func (item v2NativeMessage) projectedInput() bool {
	switch item.Type {
	case "user":
		if item.Text != "" || len(item.Files) != 0 {
			return true
		}
		for _, skill := range item.Skills {
			if skill.Text != "" {
				return true
			}
		}
		return false
	case "synthetic", "skill", "location-switched":
		return true
	case "shell":
		return item.Metadata["background"] != true
	case "assistant", "idle", "compaction", "system", "agent-switched", "model-switched":
		return false
	default:
		return true
	}
}

type v2ChildSources struct {
	Sources []string
	Calls   map[string]bool
	Closed  bool
}

func v2RebuildSources(items []v2NativeMessage, sessionID string) []backend.SessionMessage {
	messages := make([]backend.SessionMessage, 0, len(items))
	children := make(map[string]*v2ChildSources)
	noReplyIDs := make(map[string]bool)
	var pending, continuation []string
	previousAssistant := -1
	for i, item := range items {
		message := item.message(sessionID, int64(i+1))
		switch {
		case item.Type == "idle":
			if item.Outcome == "succeeded" || item.Outcome == "failed" || item.Outcome == "interrupted" {
				previousAssistant = -1
				if !v2OnlyNoReplySources(pending, noReplyIDs) {
					pending = nil
				}
				if !v2OnlyNoReplySources(continuation, noReplyIDs) {
					continuation = nil
				}
			}
		case item.Type == "assistant":
			previousAssistant = i
			message.SourceMessageIDs = v2MergeSources(continuation, pending...)
			if len(message.SourceMessageIDs) == 0 {
				message.SourceMessageIDs = []string{v2UnknownSourcePrefix + item.ID}
			}
			message.ReplySuppressed = message.ReplySuppressed || v2OnlyNoReplySources(message.SourceMessageIDs, noReplyIDs)
			pending = nil
			continuation = message.SourceMessageIDs
			for _, part := range item.Content {
				childID, _ := part.State.Metadata["sessionID"].(string)
				if part.Type != "tool" || part.Name != "subagent" || part.Executed || childID == "" {
					continue
				}
				child := children[childID]
				call := item.ID + "\x00" + part.ID
				if child != nil && child.Calls[call] {
					continue
				}
				if child == nil {
					child = &v2ChildSources{Calls: make(map[string]bool), Closed: part.State.Metadata["status"] != "running"}
					children[childID] = child
				} else if child.Closed {
					if !v2SameSources(child.Sources, message.SourceMessageIDs) {
						child.Sources = v2MergeSources(child.Sources, v2UnknownSourcePrefix+item.ID)
					}
					child.Closed = part.State.Metadata["status"] != "running"
				}
				child.Calls[call] = true
				child.Sources = v2MergeSources(child.Sources, message.SourceMessageIDs...)
			}
			if v2SafeSourceBoundary(items, i, message) {
				continuation = nil
			}
		case item.projectedInput():
			if item.Type == "user" {
				noReplyIDs[item.ID] = item.Metadata["agentbotNoReply"] == true
				if item.Metadata["agentbotDelivery"] == "queue" {
					continuation = nil
					previousAssistant = -1
				}
			}
			message.SourceMessageIDs = []string{item.ID}
			if item.Type == "synthetic" && item.Metadata["source"] == "subagent" {
				childID, _ := item.Metadata["childID"].(string)
				state, _ := item.Metadata["state"].(string)
				child := children[childID]
				message.SourceMessageIDs = []string{v2UnknownSourcePrefix + item.ID}
				if state == "completed" || state == "error" || state == "cancelled" {
					if child != nil && !child.Closed {
						message.SourceMessageIDs = append([]string(nil), child.Sources...)
						child.Closed = true
					}
				}
			} else if previousAssistant >= 0 && len(continuation) != 0 && v2BuiltinContinuation(item, items[previousAssistant]) {
				message.SourceMessageIDs = append([]string(nil), continuation...)
				previousAssistant = -1
			}
			pending = v2MergeSources(pending, message.SourceMessageIDs...)
		}
		messages = append(messages, message)
	}
	return messages
}

func v2BuiltinContinuation(item, previous v2NativeMessage) bool {
	if item.Type != "synthetic" || item.Metadata != nil || previous.Type != "assistant" || previous.Time.Completed <= 0 {
		return false
	}
	switch item.Text {
	case v2ContinueAfterOutputLimit:
		if previous.Finish != "length" || v2HasValue(previous.Error) || v2HasValue(previous.Retry) {
			return false
		}
		for _, part := range previous.Content {
			if part.Type == "tool" && !part.Executed {
				return false
			}
		}
		return true
	case v2ContinueAfterIncompleteStream:
		if previous.Finish != "error" || len(previous.Content) == 0 {
			return false
		}
		var failure struct {
			Type string `json:"type"`
		}
		var retry struct {
			Attempt int `json:"attempt"`
			Error   struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		if json.Unmarshal(previous.Error, &failure) != nil || (failure.Type != "provider.transport" && failure.Type != "provider.invalid-output") {
			return false
		}
		if v2HasValue(previous.Retry) {
			if json.Unmarshal(previous.Retry, &retry) != nil || retry.Attempt <= 0 || retry.Error.Type != failure.Type {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func v2OnlyNoReplySources(sources []string, noReplyIDs map[string]bool) bool {
	if len(sources) == 0 {
		return false
	}
	for _, id := range sources {
		if !noReplyIDs[id] {
			return false
		}
	}
	return true
}

func v2MergeSources(current []string, candidates ...string) []string {
	result := append([]string(nil), current...)
	for _, candidate := range candidates {
		found := false
		for _, existing := range result {
			if candidate == existing {
				found = true
				break
			}
		}
		if !found {
			result = append(result, candidate)
		}
	}
	return result
}

func v2SameSources(a, b []string) bool {
	return len(a) == len(b) && len(v2MergeSources(a, b...)) == len(a)
}

func v2SafeSourceBoundary(items []v2NativeMessage, index int, message backend.SessionMessage) bool {
	if message.CompletedAt <= 0 || message.Finish != "stop" || message.HasError || message.ReplySuppressed || message.Summary {
		return false
	}
	for _, part := range message.Parts {
		if part.Type == "tool" && !part.ProviderExecuted {
			return false
		}
	}
	for _, next := range items[index+1:] {
		if next.Type == "assistant" || next.Type == "idle" {
			break
		}
		if next.projectedInput() && next.Time.Created <= message.CompletedAt {
			return false
		}
	}
	return true
}
