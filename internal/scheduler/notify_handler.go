package scheduler

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/flow"
	"github.com/chenxuan520/agentbot/internal/provider"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
)

type NotifyHandler struct {
	cfg  config.Config
	flow *flow.Service
}

func NewNotifyHandler(cfg config.Config, flowService *flow.Service) *NotifyHandler {
	return &NotifyHandler{cfg: cfg, flow: flowService}
}

func (h *NotifyHandler) Handle(job Job, _ time.Time) error {
	var payload struct {
		NotifyText       string  `json:"notifyText"`
		Title            string  `json:"title"`
		ReplyMessageID   *string `json:"replyMessageID"`
		ConversationType string  `json:"conversationType"`
		ChatType         string  `json:"chatType"`
		RootMessageID    string  `json:"rootMessageID"`
		ParentMessageID  string  `json:"parentMessageID"`
		ThreadID         string  `json:"threadID"`
	}
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return err
	}
	if payload.NotifyText == "" {
		return nil
	}

	client, err := provider.FromConfig(h.cfg, job.Provider)
	if err != nil {
		return err
	}
	conversationType := strings.TrimSpace(payload.ConversationType)
	if conversationType == "" {
		conversationType = payload.ChatType
	}
	finalText := payload.NotifyText
	replyOptions := providerapi.ReplyOptions{}
	if h.flow != nil {
		finalText, replyOptions, err = h.flow.FinalizeReply(context.Background(), job.Ref(), flow.TextInput{
			Provider:         job.Provider,
			ConversationID:   job.ConversationID,
			ConversationType: conversationType,
			MessageType:      "text",
			MessageID:        derefString(payload.ReplyMessageID),
			RootMessageID:    strings.TrimSpace(payload.RootMessageID),
			ParentMessageID:  strings.TrimSpace(payload.ParentMessageID),
			ThreadID:         strings.TrimSpace(payload.ThreadID),
		}, payload.NotifyText, providerapi.ReplyOptions{})
		if err != nil {
			return err
		}
	}
	if payload.ReplyMessageID != nil {
		replyMessageID := strings.TrimSpace(*payload.ReplyMessageID)
		if replyMessageID != "" {
			return client.ReplyTextToMessage(context.Background(), replyMessageID, finalText, defaultTitle(payload.Title), replyOptions)
		}
	}
	return client.SendTextToChat(context.Background(), job.ConversationID, finalText, defaultTitle(payload.Title))
}

func defaultTitle(value string) string {
	if value == "" {
		return "Scheduled Task"
	}
	return value
}
