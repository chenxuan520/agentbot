package opencode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/chenxuan520/agentbot/internal/backend"
)

const v2SystemKeyPrefix = "agentbot-sys-"

type v2PromptFile struct {
	URI  string `json:"uri"`
	Name string `json:"name,omitempty"`
}

type v2PromptBody struct {
	ID       string         `json:"id"`
	Text     string         `json:"text"`
	Files    []v2PromptFile `json:"files,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Delivery string         `json:"delivery"`
	Resume   bool           `json:"resume"`
}

type v2InboxItem struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Type      string `json:"type"`
	Payload   struct {
		Metadata struct {
			AgentbotNoReply bool `json:"agentbotNoReply"`
		} `json:"metadata"`
	} `json:"payload"`
}

func (c *Client) v2Prompt(ctx context.Context, workspacePath, sessionID, text string, attachments []backend.Attachment, options backend.PromptOptions) (backend.PromptResult, error) {
	if strings.TrimSpace(sessionID) == "" {
		return backend.PromptResult{}, fmt.Errorf("opencode V2 prompt requires session id")
	}
	ctx, cancel := c.v2OperationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return backend.PromptResult{}, err
	}
	messageID, err := newPromptMessageID()
	if err != nil {
		return backend.PromptResult{}, err
	}
	if options.OnPromptMessage != nil {
		if err := options.OnPromptMessage(messageID); err != nil {
			return backend.PromptResult{}, err
		}
	}
	if err := c.v2WaitSession(ctx, sessionID); err != nil {
		return backend.PromptResult{}, err
	}
	body := v2PromptBody{ID: messageID, Text: text, Delivery: "queue", Resume: !options.NoReply, Metadata: map[string]any{}}
	systemKeys := make([]string, 0)
	if options.NoReply {
		body.Delivery = "steer"
		body.Metadata["agentbotNoReply"] = true
	} else {
		pending, err := c.v2PendingNoReplyIDs(ctx, sessionID)
		if err != nil {
			return backend.PromptResult{}, err
		}
		if len(pending) > 0 || len(options.SystemMessageIDs) > 0 {
			body.Delivery = "steer"
			for _, id := range pending {
				systemKeys = append(systemKeys, v2SystemKey(id))
			}
			for _, id := range options.SystemMessageIDs {
				systemKeys = append(systemKeys, v2SystemKey(id))
			}
		}
	}
	body.Metadata["agentbotDelivery"] = body.Delivery
	path := "/api/session/" + url.PathEscape(sessionID)
	if strings.TrimSpace(options.Model) != "" {
		model, err := v2ParseModelRef(options.Model)
		if err != nil {
			return backend.PromptResult{}, err
		}
		if err := c.v2Request(ctx, http.MethodPost, path+"/model", nil, map[string]any{"model": model}, nil); err != nil {
			return backend.PromptResult{}, err
		}
	}
	currentSystemKey := ""
	if strings.TrimSpace(options.System) != "" {
		key := v2SystemKey(messageID)
		if err := c.v2Request(ctx, http.MethodPut, v2InstructionPath(sessionID)+"/"+url.PathEscape(key), nil, map[string]string{"value": options.System}, nil); err != nil {
			return backend.PromptResult{}, err
		}
		currentSystemKey = key
		systemKeys = append(systemKeys, key)
	}
	for _, attachment := range attachments {
		body.Files = append(body.Files, v2PromptFile{URI: attachment.URL, Name: attachment.Filename})
	}
	var admitted struct {
		Data v2InboxItem `json:"data"`
	}
	if err := c.v2Request(ctx, http.MethodPost, path+"/prompt", nil, body, &admitted); err != nil {
		return backend.PromptResult{}, c.v2ResolveFailedAdmission(ctx, err, sessionID, messageID, currentSystemKey)
	}
	if admitted.Data.ID != messageID || admitted.Data.SessionID != sessionID || admitted.Data.Type != "user" {
		err := fmt.Errorf("opencode V2 prompt returned an invalid admission")
		return backend.PromptResult{}, c.v2ResolveFailedAdmission(ctx, err, sessionID, messageID, currentSystemKey)
	}
	if options.NoReply {
		return backend.PromptResult{SessionID: sessionID, MessageID: messageID, Native: true, SourceMessageIDs: []string{messageID}}, nil
	}
	if err := c.v2WaitSession(ctx, sessionID); err != nil {
		return backend.PromptResult{}, err
	}
	messages, err := c.v2GetSessionMessages(ctx, workspacePath, sessionID)
	if err != nil {
		return backend.PromptResult{}, err
	}
	result, replyErr := v2PromptReply(sessionID, messageID, messages)
	delivered := replyErr == nil
	for _, message := range messages {
		if message.Native && message.Role == "user" && message.ID == messageID {
			delivered = true
			break
		}
	}
	if delivered {
		replyErr = errors.Join(replyErr, c.v2DeleteSystemKeys(ctx, sessionID, systemKeys))
	}
	return result, replyErr
}

func (c *Client) v2ResolveFailedAdmission(parent context.Context, admissionErr error, sessionID, messageID, currentSystemKey string) error {
	if currentSystemKey == "" {
		return admissionErr
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), abortTimeout)
	defer cancel()
	var responseErr *v2HTTPError
	if errors.As(admissionErr, &responseErr) {
		switch responseErr.Status {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
			return errors.Join(admissionErr, c.v2DeleteSystemKeys(ctx, sessionID, []string{currentSystemKey}))
		}
	}
	path := "/api/session/" + url.PathEscape(sessionID)
	var inbox struct {
		Data []v2InboxItem `json:"data"`
	}
	inboxErr := c.v2Request(ctx, http.MethodGet, path+"/inbox", nil, nil, &inbox)
	if inboxErr == nil {
		for _, item := range inbox.Data {
			if item.ID == messageID {
				return admissionErr
			}
		}
	}
	var message struct {
		Data struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"data"`
	}
	messageErr := c.v2Request(ctx, http.MethodGet, path+"/message/"+url.PathEscape(messageID), nil, nil, &message)
	if messageErr == nil && message.Data.ID == messageID && message.Data.Type == "user" {
		return admissionErr
	}
	if inboxErr == nil && inbox.Data != nil && errors.As(messageErr, &responseErr) && responseErr.Status == http.StatusNotFound {
		return errors.Join(admissionErr, c.v2DeleteSystemKeys(ctx, sessionID, []string{currentSystemKey}))
	}
	return errors.Join(admissionErr, inboxErr, messageErr)
}

func v2PromptReply(sessionID, inputID string, messages []backend.SessionMessage) (backend.PromptResult, error) {
	var result backend.PromptResult
	var sequence int64
	found := false
	for _, message := range messages {
		if !message.Native || message.ID == "" || message.SessionID != sessionID {
			continue
		}
		ownsReply := false
		for _, id := range message.SourceMessageIDs {
			if id == inputID {
				ownsReply = true
				break
			}
		}
		if !ownsReply {
			continue
		}
		text, ok := backend.FinalReplyText(message)
		if !ok || (found && message.Sequence >= sequence) {
			continue
		}
		sequence = message.Sequence
		found = true
		result = backend.PromptResult{
			SessionID: sessionID, MessageID: message.ID, ReplyText: text, Native: true,
			SourceMessageIDs: append([]string(nil), message.SourceMessageIDs...),
		}
	}
	if !found {
		return backend.PromptResult{}, fmt.Errorf("opencode V2 session became idle without a final reply for the admitted input")
	}
	return result, nil
}

func (c *Client) v2PendingNoReplyIDs(ctx context.Context, sessionID string) ([]string, error) {
	var response struct {
		Data []v2InboxItem `json:"data"`
	}
	if err := c.v2Request(ctx, http.MethodGet, "/api/session/"+url.PathEscape(sessionID)+"/inbox", nil, nil, &response); err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for _, item := range response.Data {
		if item.Type == "user" && item.SessionID == sessionID && strings.HasPrefix(item.ID, "msg_") && item.Payload.Metadata.AgentbotNoReply {
			ids = append(ids, item.ID)
		}
	}
	return ids, nil
}

func v2SystemKey(messageID string) string {
	return v2SystemKeyPrefix + strings.ToLower(messageID)
}

func v2InstructionPath(sessionID string) string {
	return "/api/experimental/session/" + url.PathEscape(sessionID) + "/instructions/entries"
}

func (c *Client) v2DeleteSystemKeys(ctx context.Context, sessionID string, keys []string) error {
	var result error
	deleted := make(map[string]bool, len(keys))
	for _, key := range keys {
		if deleted[key] {
			continue
		}
		deleted[key] = true
		result = errors.Join(result, c.v2Request(ctx, http.MethodDelete, v2InstructionPath(sessionID)+"/"+url.PathEscape(key), nil, nil, nil))
		if ctx.Err() != nil {
			break
		}
	}
	return result
}

func (c *Client) v2CleanupAbortedSystem(ctx context.Context, sessionID string, interrupted bool, pending []string) error {
	var entries struct {
		Data []struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := c.v2Request(ctx, http.MethodGet, v2InstructionPath(sessionID), nil, nil, &entries); err != nil {
		return err
	}
	hasOwnKeys := false
	for _, entry := range entries.Data {
		if strings.HasPrefix(entry.Key, v2SystemKeyPrefix+"msg_") {
			hasOwnKeys = true
			break
		}
	}
	if !hasOwnKeys {
		return nil
	}
	candidates := make(map[string]bool)
	for _, id := range pending {
		candidates[v2SystemKey(id)] = true
	}
	if interrupted {
		var markers struct {
			Data []struct {
				ID      string `json:"id"`
				Outcome string `json:"outcome"`
			} `json:"data"`
		}
		query := url.Values{"type": {"idle"}, "order": {"desc"}, "limit": {"2"}}
		if err := c.v2Request(ctx, http.MethodGet, "/api/session/"+url.PathEscape(sessionID)+"/message", query, nil, &markers); err != nil {
			return err
		}
		if len(markers.Data) > 0 && markers.Data[0].Outcome == "interrupted" {
			messages, err := c.v2GetSessionMessages(ctx, "", sessionID)
			if err != nil {
				return err
			}
			start, end := 0, -1
			if len(markers.Data) > 1 {
				start = -1
			}
			for i, message := range messages {
				if message.ID == markers.Data[0].ID {
					end = i
				}
				if len(markers.Data) > 1 && message.ID == markers.Data[1].ID {
					start = i + 1
				}
			}
			if start >= 0 && end >= start {
				for _, message := range messages[start:end] {
					if message.Role == "user" {
						candidates[v2SystemKey(message.ID)] = true
					}
				}
			}
		}
	}
	keys := make([]string, 0)
	for _, entry := range entries.Data {
		if strings.HasPrefix(entry.Key, v2SystemKeyPrefix+"msg_") && candidates[entry.Key] {
			keys = append(keys, entry.Key)
		}
	}
	return c.v2DeleteSystemKeys(ctx, sessionID, keys)
}
