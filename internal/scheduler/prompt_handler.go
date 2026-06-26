package scheduler

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/flow"
	"github.com/chenxuan520/agentbot/internal/provider"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
)

type PromptHandler struct {
	cfg     config.Config
	flow    *flow.Service
	prompts *PromptFileStore
}

func NewPromptHandler(cfg config.Config, flowService *flow.Service) *PromptHandler {
	return &PromptHandler{cfg: cfg, flow: flowService, prompts: NewPromptFileStore(cfg)}
}

func (h *PromptHandler) Handle(job Job, _ time.Time) error {
	var payload struct {
		PromptText       string  `json:"promptText"`
		PromptFile       string  `json:"promptFile"`
		ConversationType string  `json:"conversationType"`
		ChatType         string  `json:"chatType"`
		ReplyMessageID   *string `json:"replyMessageID"`
		RootMessageID    string  `json:"rootMessageID"`
		ParentMessageID  string  `json:"parentMessageID"`
		ThreadID         string  `json:"threadID"`
		Title            string  `json:"title"`
	}
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return err
	}
	if payload.PromptText == "" && payload.PromptFile != "" && h.prompts != nil {
		promptText, err := h.prompts.ReadPrompt(payload.PromptFile)
		if err != nil {
			return err
		}
		payload.PromptText = promptText
	}
	if payload.PromptText == "" {
		return nil
	}

	result, err := h.flow.PromptConversation(context.Background(), job.Ref(), payload.PromptText)
	if err != nil {
		return err
	}

	client, err := provider.FromConfig(h.cfg, job.Provider)
	if err != nil {
		return err
	}
	conversationType := strings.TrimSpace(payload.ConversationType)
	if conversationType == "" {
		conversationType = payload.ChatType
	}
	conversationType = conversation.NormalizeType(conversationType)
	finalText, replyOptions, err := h.flow.FinalizeReply(context.Background(), job.Ref(), flow.TextInput{
		Provider:         job.Provider,
		ConversationID:   job.ConversationID,
		ConversationType: conversationType,
		MessageType:      "text",
		MessageID:        derefString(payload.ReplyMessageID),
		RootMessageID:    strings.TrimSpace(payload.RootMessageID),
		ParentMessageID:  strings.TrimSpace(payload.ParentMessageID),
		ThreadID:         strings.TrimSpace(payload.ThreadID),
	}, result.ReplyText, providerapi.ReplyOptions{})
	if err != nil {
		return err
	}
	if payload.ReplyMessageID != nil {
		replyMessageID := strings.TrimSpace(*payload.ReplyMessageID)
		if replyMessageID != "" {
			return client.ReplyTextToMessage(context.Background(), replyMessageID, finalText, defaultTitle(payload.Title), replyOptions)
		}
		return client.SendTextToChat(context.Background(), job.ConversationID, finalText, defaultTitle(payload.Title))
	}
	if conversationType != conversation.TypeDirect {
		return client.SendTextToChat(context.Background(), job.ConversationID, finalText, defaultTitle(payload.Title))
	}
	return client.SendTextToChat(context.Background(), job.ConversationID, finalText, defaultTitle(payload.Title))
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
