package localapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/chenxuan520/agentbot/internal/backend"
	"github.com/chenxuan520/agentbot/internal/conversation"
)

const transcriptWindowSize = 50
const transcriptSessionOptionLimit = 50

type adminTranscriptPart struct {
	Type             string `json:"type"`
	Text             string `json:"text"`
	Reason           string `json:"reason"`
	Tool             string `json:"tool"`
	ToolStatus       string `json:"toolStatus"`
	ToolInputSummary string `json:"toolInputSummary"`
}

type adminTranscriptMessage struct {
	ID        string                `json:"id"`
	Role      string                `json:"role"`
	CreatedAt int64                 `json:"createdAt"`
	Parts     []adminTranscriptPart `json:"parts"`
}

type adminTranscriptSessionOption struct {
	SessionID string `json:"sessionId"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	TopicKey  string `json:"topicKey,omitempty"`
}

type adminTranscriptResponse struct {
	SessionID         string                         `json:"sessionId"`
	Reset             bool                           `json:"reset"`
	TotalMessages     int                            `json:"totalMessages"`
	LatestMessageID   string                         `json:"latestMessageId"`
	AvailableSessions []adminTranscriptSessionOption `json:"availableSessions"`
	Messages          []adminTranscriptMessage       `json:"messages"`
}

func (s *Server) handleAdminSessionTranscript(c *gin.Context) {
	ref, ok := s.authorizeSessionRef(c)
	if !ok {
		return
	}
	current, err := s.sessions.Current(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	availableSessions, defaultSessionID, err := s.resolveTranscriptSessions(ref, current.ActiveSessionID)
	if err != nil {
		writeError(c, err)
		return
	}
	requestedSessionID := strings.TrimSpace(c.Query("sessionId"))
	afterMessageID := strings.TrimSpace(c.Query("afterMessageId"))
	targetSessionID := strings.TrimSpace(requestedSessionID)
	if targetSessionID == "" {
		targetSessionID = defaultSessionID
	}
	if targetSessionID == "" {
		c.JSON(http.StatusOK, buildTranscriptResponse("", requestedSessionID, afterMessageID, nil, availableSessions))
		return
	}
	if s.backends == nil {
		writeError(c, fmt.Errorf("backend factory is not configured"))
		return
	}
	client, err := s.backends(s.cfg, current.Workspace.Settings)
	if err != nil {
		writeError(c, err)
		return
	}
	lookup, ok := client.(backend.SessionMessageLookup)
	if !ok {
		writeStatusError(c, http.StatusNotImplemented, fmt.Errorf("backend %q does not support transcript lookup", current.AgentBackend))
		return
	}
	messages, err := lookup.GetSessionMessages(c.Request.Context(), targetSessionID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, buildTranscriptResponse(targetSessionID, requestedSessionID, afterMessageID, messages, availableSessions))
}

func (s *Server) resolveTranscriptSessions(ref conversation.Ref, activeSessionID string) ([]adminTranscriptSessionOption, string, error) {
	options := make([]adminTranscriptSessionOption, 0, transcriptSessionOptionLimit)
	seen := map[string]struct{}{}
	addOption := func(sessionID, kind, label, topicKey string) {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return
		}
		if _, ok := seen[sessionID]; ok {
			return
		}
		seen[sessionID] = struct{}{}
		options = append(options, adminTranscriptSessionOption{
			SessionID: sessionID,
			Kind:      strings.TrimSpace(kind),
			Label:     strings.TrimSpace(label),
			TopicKey:  strings.TrimSpace(topicKey),
		})
	}

	activeSessionID = strings.TrimSpace(activeSessionID)
	if activeSessionID != "" {
		addOption(activeSessionID, "main", "main active session", "")
	}
	if s.sessions != nil {
		topicSessions, err := s.sessions.ListTopicSessions(ref, transcriptSessionOptionLimit)
		if err != nil {
			return nil, "", err
		}
		for _, item := range topicSessions {
			label := "topic session"
			if topicKey := strings.TrimSpace(item.TopicKey); topicKey != "" {
				label = fmt.Sprintf("topic: %s", topicKey)
			}
			addOption(item.SessionID, "topic", label, item.TopicKey)
		}
	}

	defaultSessionID := activeSessionID
	if defaultSessionID == "" && len(options) > 0 {
		defaultSessionID = options[0].SessionID
	}
	return options, defaultSessionID, nil
}

func buildTranscriptResponse(sessionID, requestedSessionID, afterMessageID string, messages []backend.SessionMessage, availableSessions []adminTranscriptSessionOption) adminTranscriptResponse {
	allMessages := messages
	if allMessages == nil {
		allMessages = []backend.SessionMessage{}
	}
	if availableSessions == nil {
		availableSessions = []adminTranscriptSessionOption{}
	}
	latestMessageID := ""
	if len(allMessages) > 0 {
		latestMessageID = strings.TrimSpace(allMessages[len(allMessages)-1].ID)
	}
	if strings.TrimSpace(sessionID) == "" {
		return adminTranscriptResponse{SessionID: "", Reset: true, TotalMessages: 0, LatestMessageID: "", AvailableSessions: availableSessions, Messages: []adminTranscriptMessage{}}
	}
	if strings.TrimSpace(requestedSessionID) == "" || strings.TrimSpace(requestedSessionID) != strings.TrimSpace(sessionID) || strings.TrimSpace(afterMessageID) == "" {
		return adminTranscriptResponse{
			SessionID:         sessionID,
			Reset:             true,
			TotalMessages:     len(allMessages),
			LatestMessageID:   latestMessageID,
			AvailableSessions: availableSessions,
			Messages:          marshalTranscriptMessages(transcriptSnapshotMessages(allMessages, transcriptWindowSize)),
		}
	}
	afterIndex := transcriptMessageIndex(allMessages, afterMessageID)
	if afterIndex < 0 {
		return adminTranscriptResponse{
			SessionID:         sessionID,
			Reset:             true,
			TotalMessages:     len(allMessages),
			LatestMessageID:   latestMessageID,
			AvailableSessions: availableSessions,
			Messages:          marshalTranscriptMessages(transcriptSnapshotMessages(allMessages, transcriptWindowSize)),
		}
	}
	return adminTranscriptResponse{
		SessionID:         sessionID,
		Reset:             false,
		TotalMessages:     len(allMessages),
		LatestMessageID:   latestMessageID,
		AvailableSessions: availableSessions,
		Messages:          marshalTranscriptMessages(allMessages[afterIndex:]),
	}
}

func transcriptSnapshotMessages(messages []backend.SessionMessage, size int) []backend.SessionMessage {
	latestUserIndex := transcriptLatestUserIndex(messages)
	if latestUserIndex >= 0 {
		return tailTranscriptMessages(messages[latestUserIndex:], size)
	}
	return tailTranscriptMessages(messages, size)
}

func tailTranscriptMessages(messages []backend.SessionMessage, size int) []backend.SessionMessage {
	if len(messages) <= size {
		return append([]backend.SessionMessage(nil), messages...)
	}
	return append([]backend.SessionMessage(nil), messages[len(messages)-size:]...)
}

func transcriptLatestUserIndex(messages []backend.SessionMessage) int {
	for index := len(messages) - 1; index >= 0; index-- {
		if strings.EqualFold(strings.TrimSpace(messages[index].Role), "user") {
			return index
		}
	}
	return -1
}

func transcriptMessageIndex(messages []backend.SessionMessage, messageID string) int {
	target := strings.TrimSpace(messageID)
	if target == "" {
		return -1
	}
	for index, message := range messages {
		if strings.TrimSpace(message.ID) == target {
			return index
		}
	}
	return -1
}

func marshalTranscriptMessages(messages []backend.SessionMessage) []adminTranscriptMessage {
	items := make([]adminTranscriptMessage, 0, len(messages))
	for _, message := range messages {
		item := adminTranscriptMessage{
			ID:        strings.TrimSpace(message.ID),
			Role:      strings.TrimSpace(message.Role),
			CreatedAt: message.CreatedAt,
			Parts:     make([]adminTranscriptPart, 0, len(message.Parts)),
		}
		for _, part := range message.Parts {
			item.Parts = append(item.Parts, adminTranscriptPart{
				Type:             strings.TrimSpace(part.Type),
				Text:             part.Text,
				Reason:           strings.TrimSpace(part.Reason),
				Tool:             strings.TrimSpace(part.Tool),
				ToolStatus:       strings.TrimSpace(part.ToolStatus),
				ToolInputSummary: strings.TrimSpace(part.ToolInputSummary),
			})
		}
		items = append(items, item)
	}
	return items
}
