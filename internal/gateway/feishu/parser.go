package feishu

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/chenxuan520/agentbot/internal/conversation"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

var mentionPlaceholderPattern = regexp.MustCompile(`@_user_\d+`)

type IncomingMessage struct {
	ChatID           string
	ConversationType string
	MessageType      string
	MessageID        string
	RootMessageID    string
	ParentMessageID  string
	ThreadID         string
	CreateTimeMS     int64
	SenderType       string
	SenderID         string
	EventAction      string
	ReactionEmoji    string
	Mentioned        bool
	MentionOpenIDs   []string
	MentionNames     []string
	ImageKeys        []string
	Text             string
}

const senderTypeUser = "user"

func parseIncoming(event *larkim.P2MessageReceiveV1) *IncomingMessage {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Sender == nil {
		return nil
	}
	senderType := normalizeSenderType(value(event.Event.Sender.SenderType))
	if senderType == "" {
		return nil
	}

	message := event.Event.Message
	chatID := value(message.ChatId)
	messageID := value(message.MessageId)
	messageType := value(message.MessageType)
	chatType := value(message.ChatType)
	if chatID == "" || messageID == "" {
		return nil
	}

	text, imageKeys, ok := parseContent(messageType, value(message.Content))
	if !ok {
		return nil
	}
	senderOpenID := ""
	if event.Event.Sender.SenderId != nil {
		senderOpenID = value(event.Event.Sender.SenderId.OpenId)
	}

	mentioned := len(message.Mentions) > 0
	mentionOpenIDs, mentionNames := extractMentionTargets(message.Mentions)
	if chatType == "group" || chatType == "topic_group" {
		if mentioned {
			text = stripMentionPlaceholders(text)
		}
	}

	text = strings.TrimSpace(text)
	if text == "" && len(imageKeys) > 0 {
		text = imageOnlyPrompt
	}
	if text == "" && len(imageKeys) == 0 {
		return nil
	}

	return &IncomingMessage{
		ChatID:           chatID,
		ConversationType: normalizeConversationType(chatType),
		MessageType:      messageType,
		MessageID:        messageID,
		RootMessageID:    value(message.RootId),
		ParentMessageID:  value(message.ParentId),
		ThreadID:         value(message.ThreadId),
		CreateTimeMS:     parseTimestamp(value(message.CreateTime)),
		SenderType:       senderType,
		SenderID:         senderOpenID,
		Mentioned:        mentioned,
		MentionOpenIDs:   mentionOpenIDs,
		MentionNames:     mentionNames,
		ImageKeys:        imageKeys,
		Text:             text,
	}
}

func (m IncomingMessage) IsUserMessage() bool {
	return m.SenderType == senderTypeUser
}

func (m IncomingMessage) IsInteractiveMessage() bool {
	return strings.EqualFold(strings.TrimSpace(m.MessageType), "interactive")
}

func parseReactionCreated(event *larkim.P2MessageReactionCreatedV1) *IncomingMessage {
	if event == nil || event.Event == nil {
		return nil
	}
	messageID := value(event.Event.MessageId)
	if messageID == "" {
		return nil
	}
	emojiType := ""
	if event.Event.ReactionType != nil {
		emojiType = strings.TrimSpace(value(event.Event.ReactionType.EmojiType))
	}
	senderType := normalizeSenderType(value(event.Event.OperatorType))
	senderID := ""
	if senderType == senderTypeUser && event.Event.UserId != nil {
		senderID = value(event.Event.UserId.OpenId)
	} else if senderType == "app" {
		senderID = value(event.Event.AppId)
	}
	return &IncomingMessage{
		ConversationType: conversation.TypeGroup,
		MessageType:      "reaction",
		MessageID:        messageID,
		CreateTimeMS:     parseTimestamp(value(event.Event.ActionTime)),
		SenderType:       senderType,
		SenderID:         senderID,
		EventAction:      "created",
		ReactionEmoji:    emojiType,
	}
}

func parseReactionDeleted(event *larkim.P2MessageReactionDeletedV1) *IncomingMessage {
	if event == nil || event.Event == nil {
		return nil
	}
	messageID := value(event.Event.MessageId)
	if messageID == "" {
		return nil
	}
	emojiType := ""
	if event.Event.ReactionType != nil {
		emojiType = strings.TrimSpace(value(event.Event.ReactionType.EmojiType))
	}
	senderType := normalizeSenderType(value(event.Event.OperatorType))
	senderID := ""
	if senderType == senderTypeUser && event.Event.UserId != nil {
		senderID = value(event.Event.UserId.OpenId)
	} else if senderType == "app" {
		senderID = value(event.Event.AppId)
	}
	return &IncomingMessage{
		ConversationType: conversation.TypeGroup,
		MessageType:      "reaction",
		MessageID:        messageID,
		CreateTimeMS:     parseTimestamp(value(event.Event.ActionTime)),
		SenderType:       senderType,
		SenderID:         senderID,
		EventAction:      "deleted",
		ReactionEmoji:    emojiType,
	}
}

const imageOnlyPrompt = "请先查看我附带的图片。如果我没有提供额外文字，请先描述图片中的关键信息。"

func parseContent(messageType, raw string) (string, []string, bool) {
	switch messageType {
	case "text":
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return "", nil, false
		}
		return payload.Text, nil, strings.TrimSpace(payload.Text) != ""
	case "image":
		keys := parseImageKeys(raw)
		return "", keys, len(keys) > 0
	case "interactive":
		return raw, nil, strings.TrimSpace(raw) != ""
	case "post":
		return parsePost(raw)
	default:
		return "", nil, false
	}
}

func parseImageKeys(raw string) []string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil
	}
	if imageKey, ok := payload["image_key"].(string); ok && imageKey != "" {
		return []string{imageKey}
	}
	return nil
}

func parsePost(raw string) (string, []string, bool) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", nil, false
	}
	rows := extractPostRows(payload)
	if len(rows) == 0 {
		return "", nil, false
	}

	lines := make([]string, 0, len(rows))
	imageKeys := make([]string, 0, 1)
	for _, row := range rows {
		parts := make([]string, 0, len(row))
		for _, block := range row {
			tag, _ := block["tag"].(string)
			switch tag {
			case "text", "md":
				if text, ok := block["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			case "a":
				if text, ok := block["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			case "at":
				if userID, ok := block["user_id"].(string); ok && userID != "" {
					parts = append(parts, userID)
				}
			case "img":
				if imageKey, ok := block["image_key"].(string); ok && imageKey != "" {
					imageKeys = append(imageKeys, imageKey)
				} else if imageKey, ok := block["img_key"].(string); ok && imageKey != "" {
					imageKeys = append(imageKeys, imageKey)
				}
			}
		}
		line := strings.TrimSpace(strings.Join(parts, ""))
		if line != "" {
			lines = append(lines, line)
		}
	}

	text := strings.TrimSpace(strings.Join(lines, "\n"))
	return text, uniqueStrings(imageKeys), text != "" || len(imageKeys) > 0
}

func extractPostRows(value any) [][]map[string]any {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if content, ok := obj["content"].([]any); ok {
		rows := make([][]map[string]any, 0, len(content))
		for _, rowValue := range content {
			rowItems, ok := rowValue.([]any)
			if !ok {
				continue
			}
			row := make([]map[string]any, 0, len(rowItems))
			for _, item := range rowItems {
				block, ok := item.(map[string]any)
				if ok {
					row = append(row, block)
				}
			}
			if len(row) > 0 {
				rows = append(rows, row)
			}
		}
		if len(rows) > 0 {
			return rows
		}
	}
	for _, next := range obj {
		if rows := extractPostRows(next); len(rows) > 0 {
			return rows
		}
	}
	return nil
}

func stripMentionPlaceholders(text string) string {
	cleaned := mentionPlaceholderPattern.ReplaceAllString(text, "")
	return strings.Join(strings.Fields(cleaned), " ")
}

func normalizeConversationType(chatType string) string {
	return conversation.NormalizeType(chatType)
}

func normalizeSenderType(senderType string) string {
	return strings.ToLower(strings.TrimSpace(senderType))
}

func value(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

func parseTimestamp(value string) int64 {
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func extractMentionTargets(mentions []*larkim.MentionEvent) ([]string, []string) {
	if len(mentions) == 0 {
		return nil, nil
	}
	openIDs := make([]string, 0, len(mentions))
	names := make([]string, 0, len(mentions))
	for _, mention := range mentions {
		if mention == nil {
			continue
		}
		if mention.Id != nil {
			if openID := value(mention.Id.OpenId); openID != "" {
				openIDs = append(openIDs, openID)
			}
		}
		if name := strings.TrimSpace(value(mention.Name)); name != "" {
			names = append(names, name)
		}
	}
	return uniqueStrings(openIDs), uniqueStrings(names)
}
