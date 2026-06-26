package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/chenxuan520/agentbot/internal/backend"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const apiBaseURL = "https://open.feishu.cn/open-apis"
const mentionPlaceholderPrefix = "__agent_bot_mention_"
const mentionPlaceholderSuffix = "__"
const markdownMentionToken = "<at user_id=\""

type Client struct {
	appID                       string
	appSecret                   string
	handlingReactionEmoji       string
	remoteHandlingReactionEmoji string
	httpClient                  *http.Client
	apiClient                   *lark.Client
	botInfoMu                   sync.Mutex
	botInfo                     BotIdentity
}

type BotIdentity struct {
	OpenID  string
	AppName string
}

const blockedReactionEmoji = "SHHH"

func New(appID, appSecret, handlingReactionEmoji, remoteHandlingReactionEmoji string) *Client {
	handlingReactionEmoji = strings.TrimSpace(handlingReactionEmoji)
	if handlingReactionEmoji == "" {
		handlingReactionEmoji = "OnIt"
	}
	// When the remote-agent forward emoji is unset, reuse the handling emoji so
	// the default behavior matches the regular ack icon.
	remoteHandlingReactionEmoji = strings.TrimSpace(remoteHandlingReactionEmoji)
	if remoteHandlingReactionEmoji == "" {
		remoteHandlingReactionEmoji = handlingReactionEmoji
	}
	return &Client{
		appID:                       strings.TrimSpace(appID),
		appSecret:                   strings.TrimSpace(appSecret),
		handlingReactionEmoji:       handlingReactionEmoji,
		remoteHandlingReactionEmoji: remoteHandlingReactionEmoji,
		httpClient:                  &http.Client{},
		apiClient:                   lark.NewClient(strings.TrimSpace(appID), strings.TrimSpace(appSecret)),
	}
}

func (c *Client) Name() string {
	return "feishu"
}

func (c *Client) Health(ctx context.Context) error {
	_, err := c.tenantAccessToken(ctx)
	return err
}

func (c *Client) AddHandlingReaction(ctx context.Context, messageID string) (string, error) {
	return c.addReaction(ctx, messageID, c.handlingReactionEmoji)
}

func (c *Client) AddRemoteHandlingReaction(ctx context.Context, messageID string) (string, error) {
	return c.addReaction(ctx, messageID, c.remoteHandlingReactionEmoji)
}

func (c *Client) AddBlockedReaction(ctx context.Context, messageID string) error {
	_, err := c.addReaction(ctx, messageID, blockedReactionEmoji)
	return err
}

func (c *Client) AddReaction(ctx context.Context, messageID, emojiType string) error {
	_, err := c.addReaction(ctx, messageID, strings.TrimSpace(emojiType))
	return err
}

func (c *Client) addReaction(ctx context.Context, messageID, emojiType string) (string, error) {
	request := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(
			larkim.NewCreateMessageReactionReqBodyBuilder().
				ReactionType(larkim.NewEmojiBuilder().EmojiType(emojiType).Build()).
				Build(),
		).
		Build()
	response, err := c.apiClient.Im.MessageReaction.Create(ctx, request)
	if err != nil {
		return "", err
	}
	if !response.Success() {
		return "", fmt.Errorf("create reaction failed: code=%d msg=%s", response.Code, response.Msg)
	}
	if response.Data == nil || response.Data.ReactionId == nil {
		return "", fmt.Errorf("create reaction returned empty reaction id")
	}
	return *response.Data.ReactionId, nil
}

func (c *Client) DeleteReaction(ctx context.Context, messageID, reactionID string) error {
	if reactionID == "" {
		return nil
	}
	request := larkim.NewDeleteMessageReactionReqBuilder().
		MessageId(messageID).
		ReactionId(reactionID).
		Build()
	response, err := c.apiClient.Im.MessageReaction.Delete(ctx, request)
	if err != nil {
		return err
	}
	if !response.Success() {
		return fmt.Errorf("delete reaction failed: code=%d msg=%s", response.Code, response.Msg)
	}
	return nil
}

func (c *Client) RecallMessage(ctx context.Context, messageID string) error {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return fmt.Errorf("missing message id")
	}
	request := larkim.NewDeleteMessageReqBuilder().
		MessageId(messageID).
		Build()
	response, err := c.apiClient.Im.Message.Delete(ctx, request)
	if err != nil {
		return err
	}
	if !response.Success() {
		return fmt.Errorf("recall message failed: code=%d msg=%s", response.Code, response.Msg)
	}
	return nil
}

func (c *Client) SendTextToChat(ctx context.Context, chatID, text, title string) error {
	token, err := c.tenantAccessToken(ctx)
	if err != nil {
		return err
	}

	body := map[string]any{
		"receive_id": chatID,
		"msg_type":   "post",
		"content":    buildPostContent(text, title, ""),
	}
	return c.postJSON(ctx, token, apiBaseURL+"/im/v1/messages?receive_id_type=chat_id", body, nil)
}

func (c *Client) ReplyTextToMessage(ctx context.Context, messageID, text, title string, options providerapi.ReplyOptions) error {
	token, err := c.tenantAccessToken(ctx)
	if err != nil {
		return err
	}

	body := map[string]any{
		"msg_type":        "post",
		"reply_in_thread": options.InThread,
		"content":         buildPostContent(text, title, mentionUserOpenIDs(options)...),
	}
	return c.postJSON(ctx, token, apiBaseURL+"/im/v1/messages/"+messageID+"/reply", body, nil)
}

func (c *Client) PrepareImageAttachments(ctx context.Context, messageID string, imageKeys []string) ([]backend.Attachment, func(), error) {
	if len(imageKeys) == 0 {
		return nil, func() {}, nil
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return nil, nil, fmt.Errorf("missing message id for image attachments")
	}

	targetDir, err := os.MkdirTemp("", "agent-bot-feishu-image-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(targetDir) }

	attachments := make([]backend.Attachment, 0, len(imageKeys))
	for index, imageKey := range imageKeys {
		request := larkim.NewGetMessageResourceReqBuilder().MessageId(messageID).FileKey(imageKey).Type("image").Build()
		response, err := c.apiClient.Im.MessageResource.Get(ctx, request)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		if !response.Success() || response.File == nil {
			cleanup()
			return nil, nil, fmt.Errorf("download image failed: message_id=%s key=%s code=%d msg=%s", messageID, imageKey, response.Code, response.Msg)
		}

		filename := response.FileName
		if filename == "" {
			filename = fmt.Sprintf("image-%d.png", index+1)
		}
		filePath := filepath.Join(targetDir, filename)
		file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		if _, err := io.Copy(file, response.File); err != nil {
			_ = file.Close()
			cleanup()
			return nil, nil, err
		}
		_ = file.Close()

		mimeType := detectImageMIME(filename)
		attachments = append(attachments, backend.Attachment{
			Mime:     mimeType,
			Filename: filename,
			URL:      (&url.URL{Scheme: "file", Path: filePath}).String(),
		})
	}

	return attachments, cleanup, nil
}

func (c *Client) BotIdentity(ctx context.Context) (BotIdentity, error) {
	c.botInfoMu.Lock()
	defer c.botInfoMu.Unlock()
	if c.botInfo.OpenID != "" || c.botInfo.AppName != "" {
		return c.botInfo, nil
	}

	token, err := c.tenantAccessToken(ctx)
	if err != nil {
		return BotIdentity{}, err
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenID  string `json:"open_id"`
			AppName string `json:"app_name"`
		} `json:"bot"`
	}
	if err := c.getJSON(ctx, token, apiBaseURL+"/bot/v3/info", &payload); err != nil {
		return BotIdentity{}, err
	}
	if payload.Code != 0 {
		return BotIdentity{}, fmt.Errorf("feishu bot info failed: code=%d msg=%s", payload.Code, payload.Msg)
	}
	c.botInfo = BotIdentity{OpenID: strings.TrimSpace(payload.Bot.OpenID), AppName: strings.TrimSpace(payload.Bot.AppName)}
	return c.botInfo, nil
}

func (c *Client) GetChatDisplayInfo(ctx context.Context, chatID string) (providerapi.ChatDisplayInfo, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return providerapi.ChatDisplayInfo{}, fmt.Errorf("missing chat id")
	}

	req := larkim.NewGetChatReqBuilder().ChatId(chatID).UserIdType("open_id").Build()
	resp, err := c.apiClient.Im.Chat.Get(ctx, req)
	if err != nil {
		return providerapi.ChatDisplayInfo{}, err
	}
	if !resp.Success() {
		return providerapi.ChatDisplayInfo{}, fmt.Errorf("feishu get chat failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil {
		return providerapi.ChatDisplayInfo{}, nil
	}

	info := providerapi.ChatDisplayInfo{}
	if resp.Data.ChatMode != nil {
		info.ChatMode = strings.TrimSpace(*resp.Data.ChatMode)
	}
	name := ""
	if resp.Data.Name != nil {
		name = strings.TrimSpace(*resp.Data.Name)
	}
	if info.ChatMode == "p2p" || name == "" {
		memberNames, err := c.chatMemberNames(ctx, chatID)
		if err == nil && len(memberNames) > 0 {
			switch info.ChatMode {
			case "p2p":
				name = memberNames[0]
			default:
				if name == "" {
					if len(memberNames) > 3 {
						memberNames = memberNames[:3]
					}
					name = strings.Join(memberNames, "、")
				}
			}
		}
	}
	info.DisplayName = strings.TrimSpace(name)
	return info, nil
}

func (c *Client) ListChatMembers(ctx context.Context, chatID string) ([]providerapi.ChatMember, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return nil, fmt.Errorf("missing chat id")
	}

	token, err := c.tenantAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	type listMembersResponse struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			HasMore   bool   `json:"has_more"`
			PageToken string `json:"page_token"`
			Items     []struct {
				MemberID     string `json:"member_id"`
				MemberIDType string `json:"member_id_type"`
				Name         string `json:"name"`
				TenantKey    string `json:"tenant_key"`
			} `json:"items"`
		} `json:"data"`
	}

	requestURL := apiBaseURL + "/im/v1/chats/" + url.PathEscape(chatID) + "/members?member_id_type=open_id&page_size=100"
	result := make([]providerapi.ChatMember, 0, 16)
	for {
		var payload listMembersResponse
		if err := c.getJSON(ctx, token, requestURL, &payload); err != nil {
			return nil, err
		}
		if payload.Code != 0 {
			return nil, fmt.Errorf("feishu list chat members failed: code=%d msg=%s", payload.Code, payload.Msg)
		}
		for _, item := range payload.Data.Items {
			result = append(result, providerapi.ChatMember{
				MemberID:     strings.TrimSpace(item.MemberID),
				MemberIDType: strings.TrimSpace(item.MemberIDType),
				Name:         strings.TrimSpace(item.Name),
				TenantKey:    strings.TrimSpace(item.TenantKey),
			})
		}
		if !payload.Data.HasMore || strings.TrimSpace(payload.Data.PageToken) == "" {
			return result, nil
		}
		requestURL = apiBaseURL + "/im/v1/chats/" + url.PathEscape(chatID) + "/members?member_id_type=open_id&page_size=100&page_token=" + url.QueryEscape(payload.Data.PageToken)
	}
}

func (c *Client) chatMemberNames(ctx context.Context, chatID string) ([]string, error) {
	members, err := c.ListChatMembers(ctx, chatID)
	if err != nil {
		return nil, err
	}
	botIdentity, botErr := c.BotIdentity(ctx)
	botOpenID := ""
	if botErr == nil {
		botOpenID = strings.TrimSpace(botIdentity.OpenID)
	}
	seen := map[string]struct{}{}
	result := make([]string, 0, len(members))
	for _, member := range members {
		memberID := strings.TrimSpace(member.MemberID)
		name := strings.TrimSpace(member.Name)
		if name == "" {
			continue
		}
		if botOpenID != "" && memberID == botOpenID {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	return result, nil
}

func (c *Client) ListChatMessages(ctx context.Context, chatID string, options providerapi.ChatMessageListOptions) (providerapi.ChatMessageListResult, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return providerapi.ChatMessageListResult{}, fmt.Errorf("missing chat id")
	}

	token, err := c.tenantAccessToken(ctx)
	if err != nil {
		return providerapi.ChatMessageListResult{}, err
	}

	query := url.Values{}
	query.Set("container_id_type", "chat")
	query.Set("container_id", chatID)
	if value := strings.TrimSpace(options.StartTime); value != "" {
		query.Set("start_time", value)
	}
	if value := strings.TrimSpace(options.EndTime); value != "" {
		query.Set("end_time", value)
	}
	if value := strings.TrimSpace(options.SortType); value != "" {
		query.Set("sort_type", value)
	}
	if options.PageSize > 0 {
		query.Set("page_size", strconv.Itoa(options.PageSize))
	}
	if value := strings.TrimSpace(options.PageToken); value != "" {
		query.Set("page_token", value)
	}
	if value := strings.TrimSpace(options.CardMsgContentType); value != "" {
		query.Set("card_msg_content_type", value)
	}

	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			HasMore   bool   `json:"has_more"`
			PageToken string `json:"page_token"`
			Items     []struct {
				MessageID  string `json:"message_id"`
				RootID     string `json:"root_id"`
				ParentID   string `json:"parent_id"`
				ThreadID   string `json:"thread_id"`
				MsgType    string `json:"msg_type"`
				CreateTime string `json:"create_time"`
				UpdateTime string `json:"update_time"`
				Deleted    bool   `json:"deleted"`
				Updated    bool   `json:"updated"`
				ChatID     string `json:"chat_id"`
				Sender     struct {
					ID         string `json:"id"`
					IDType     string `json:"id_type"`
					SenderType string `json:"sender_type"`
					TenantKey  string `json:"tenant_key"`
				} `json:"sender"`
				Body struct {
					Content string `json:"content"`
				} `json:"body"`
				Mentions []struct {
					Key       string `json:"key"`
					ID        string `json:"id"`
					IDType    string `json:"id_type"`
					Name      string `json:"name"`
					TenantKey string `json:"tenant_key"`
				} `json:"mentions"`
			} `json:"items"`
		} `json:"data"`
	}
	requestURL := apiBaseURL + "/im/v1/messages?" + query.Encode()
	if err := c.getJSON(ctx, token, requestURL, &payload); err != nil {
		return providerapi.ChatMessageListResult{}, err
	}
	if payload.Code != 0 {
		return providerapi.ChatMessageListResult{}, fmt.Errorf("feishu list chat messages failed: code=%d msg=%s", payload.Code, payload.Msg)
	}

	result := providerapi.ChatMessageListResult{
		HasMore:   payload.Data.HasMore,
		PageToken: strings.TrimSpace(payload.Data.PageToken),
		Items:     make([]providerapi.ChatMessage, 0, len(payload.Data.Items)),
	}
	for _, item := range payload.Data.Items {
		message := providerapi.ChatMessage{
			MessageID:  strings.TrimSpace(item.MessageID),
			RootID:     strings.TrimSpace(item.RootID),
			ParentID:   strings.TrimSpace(item.ParentID),
			ThreadID:   strings.TrimSpace(item.ThreadID),
			MsgType:    strings.TrimSpace(item.MsgType),
			CreateTime: strings.TrimSpace(item.CreateTime),
			UpdateTime: strings.TrimSpace(item.UpdateTime),
			Deleted:    item.Deleted,
			Updated:    item.Updated,
			ChatID:     strings.TrimSpace(item.ChatID),
			Sender: providerapi.ChatMessageSender{
				ID:         strings.TrimSpace(item.Sender.ID),
				IDType:     strings.TrimSpace(item.Sender.IDType),
				SenderType: strings.TrimSpace(item.Sender.SenderType),
				TenantKey:  strings.TrimSpace(item.Sender.TenantKey),
			},
			Body: providerapi.ChatMessageBody{Content: item.Body.Content},
		}
		if len(item.Mentions) > 0 {
			message.Mentions = make([]providerapi.ChatMessageAt, 0, len(item.Mentions))
			for _, mention := range item.Mentions {
				message.Mentions = append(message.Mentions, providerapi.ChatMessageAt{
					Key:       strings.TrimSpace(mention.Key),
					ID:        strings.TrimSpace(mention.ID),
					IDType:    strings.TrimSpace(mention.IDType),
					Name:      strings.TrimSpace(mention.Name),
					TenantKey: strings.TrimSpace(mention.TenantKey),
				})
			}
		}
		result.Items = append(result.Items, message)
	}
	return result, nil
}

func (c *Client) GetMessage(ctx context.Context, messageID string) (providerapi.ChatMessage, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return providerapi.ChatMessage{}, fmt.Errorf("missing message id")
	}

	token, err := c.tenantAccessToken(ctx)
	if err != nil {
		return providerapi.ChatMessage{}, err
	}

	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []struct {
				MessageID  string `json:"message_id"`
				RootID     string `json:"root_id"`
				ParentID   string `json:"parent_id"`
				ThreadID   string `json:"thread_id"`
				MsgType    string `json:"msg_type"`
				CreateTime string `json:"create_time"`
				UpdateTime string `json:"update_time"`
				Deleted    bool   `json:"deleted"`
				Updated    bool   `json:"updated"`
				ChatID     string `json:"chat_id"`
				Sender     struct {
					ID         string `json:"id"`
					IDType     string `json:"id_type"`
					SenderType string `json:"sender_type"`
					TenantKey  string `json:"tenant_key"`
				} `json:"sender"`
				Body struct {
					Content string `json:"content"`
				} `json:"body"`
				Mentions []struct {
					Key       string `json:"key"`
					ID        string `json:"id"`
					IDType    string `json:"id_type"`
					Name      string `json:"name"`
					TenantKey string `json:"tenant_key"`
				} `json:"mentions"`
			} `json:"items"`
		} `json:"data"`
	}
	requestURL := apiBaseURL + "/im/v1/messages/" + url.PathEscape(messageID)
	if err := c.getJSON(ctx, token, requestURL, &payload); err != nil {
		return providerapi.ChatMessage{}, err
	}
	if payload.Code != 0 {
		return providerapi.ChatMessage{}, fmt.Errorf("feishu get message failed: code=%d msg=%s", payload.Code, payload.Msg)
	}
	if len(payload.Data.Items) == 0 {
		return providerapi.ChatMessage{}, fmt.Errorf("feishu get message returned no items")
	}
	item := payload.Data.Items[0]
	message := providerapi.ChatMessage{
		MessageID:  strings.TrimSpace(item.MessageID),
		RootID:     strings.TrimSpace(item.RootID),
		ParentID:   strings.TrimSpace(item.ParentID),
		ThreadID:   strings.TrimSpace(item.ThreadID),
		MsgType:    strings.TrimSpace(item.MsgType),
		CreateTime: strings.TrimSpace(item.CreateTime),
		UpdateTime: strings.TrimSpace(item.UpdateTime),
		Deleted:    item.Deleted,
		Updated:    item.Updated,
		ChatID:     strings.TrimSpace(item.ChatID),
		Sender: providerapi.ChatMessageSender{
			ID:         strings.TrimSpace(item.Sender.ID),
			IDType:     strings.TrimSpace(item.Sender.IDType),
			SenderType: strings.TrimSpace(item.Sender.SenderType),
			TenantKey:  strings.TrimSpace(item.Sender.TenantKey),
		},
		Body: providerapi.ChatMessageBody{Content: item.Body.Content},
	}
	if len(item.Mentions) > 0 {
		message.Mentions = make([]providerapi.ChatMessageAt, 0, len(item.Mentions))
		for _, mention := range item.Mentions {
			message.Mentions = append(message.Mentions, providerapi.ChatMessageAt{
				Key:       strings.TrimSpace(mention.Key),
				ID:        strings.TrimSpace(mention.ID),
				IDType:    strings.TrimSpace(mention.IDType),
				Name:      strings.TrimSpace(mention.Name),
				TenantKey: strings.TrimSpace(mention.TenantKey),
			})
		}
	}
	return message, nil
}

func (c *Client) tenantAccessToken(ctx context.Context) (string, error) {
	if c.appID == "" || c.appSecret == "" {
		return "", fmt.Errorf("missing feishu app credentials")
	}

	body := map[string]string{
		"app_id":     c.appID,
		"app_secret": c.appSecret,
	}
	var response struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	if err := c.postJSON(ctx, "", apiBaseURL+"/auth/v3/tenant_access_token/internal", body, &response); err != nil {
		return "", err
	}
	if response.Code != 0 || response.TenantAccessToken == "" {
		return "", fmt.Errorf("feishu auth failed: code=%d msg=%s", response.Code, response.Msg)
	}
	return response.TenantAccessToken, nil
}

func (c *Client) postJSON(ctx context.Context, bearerToken, url string, requestBody any, responseBody any) error {
	data, err := json.Marshal(requestBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("feishu request failed: %s", resp.Status)
	}
	if responseBody == nil {
		var payload struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return err
		}
		if payload.Code != 0 {
			return fmt.Errorf("feishu request failed: code=%d msg=%s", payload.Code, payload.Msg)
		}
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(responseBody)
}

func (c *Client) getJSON(ctx context.Context, bearerToken, url string, responseBody any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("feishu request failed: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(responseBody)
}

func buildPostContent(text, title string, mentionUserOpenIDs ...string) string {
	if strings.TrimSpace(title) == "" {
		title = " "
	}
	if text == "" {
		text = " "
	}
	lines := splitLines(text)
	mentionUserOpenIDs = normalizeMentionUserOpenIDs(mentionUserOpenIDs)
	if containsMarkdownMentions(lines) {
		mentionUserOpenIDs = nil
	}
	if containsMentionPlaceholder(lines) {
		return buildPostContentWithInlineMentions(lines, title, mentionUserOpenIDs)
	}
	bodyLines := lines
	contentLines := [][]map[string]any{}
	if len(mentionUserOpenIDs) > 0 {
		firstLine := make([]map[string]any, 0, len(mentionUserOpenIDs)*2)
		for index, mentionUserOpenID := range mentionUserOpenIDs {
			if index > 0 {
				firstLine = append(firstLine, map[string]any{"tag": "text", "text": " "})
			}
			firstLine = append(firstLine, map[string]any{
				"tag":     "at",
				"user_id": mentionUserOpenID,
			})
		}
		if _, _, _, ok := parseStandaloneMarkdownLink(firstLineFromLines(bodyLines)); !ok {
			inlineText := strings.TrimSpace(firstLineFromLines(bodyLines))
			bodyLines = remainingLinesFromLines(bodyLines)
			if inlineText != "" {
				firstLine = append(firstLine, map[string]any{"tag": "text", "text": " " + inlineText})
			}
		}
		contentLines = append(contentLines, firstLine)
	}
	contentLines = append(contentLines, buildPostBodyContentLines(bodyLines)...)
	if len(contentLines) == 0 {
		contentLines = append(contentLines, []map[string]any{{"tag": "text", "text": " "}})
	}
	content := map[string]any{
		"zh_cn": map[string]any{
			"title":   title,
			"content": contentLines,
		},
	}
	data, _ := json.Marshal(content)
	return string(data)
}

func buildPostBodyContentLines(lines []string) [][]map[string]any {
	contentLines := make([][]map[string]any, 0, len(lines))
	mdLines := make([]string, 0, len(lines))
	flushMarkdown := func() {
		if len(mdLines) == 0 {
			return
		}
		mdText := strings.Join(mdLines, "\n")
		if strings.TrimSpace(mdText) != "" {
			contentLines = append(contentLines, []map[string]any{{"tag": "md", "text": mdText}})
		}
		mdLines = mdLines[:0]
	}
	for _, line := range lines {
		if text, href, style, ok := parseStandaloneMarkdownLink(line); ok {
			flushMarkdown()
			linkBlock := map[string]any{"tag": "a", "text": text, "href": href}
			if len(style) > 0 {
				linkBlock["style"] = style
			}
			contentLines = append(contentLines, []map[string]any{linkBlock})
			continue
		}
		mdLines = append(mdLines, line)
	}
	flushMarkdown()
	return contentLines
}

func parseStandaloneMarkdownLink(line string) (text string, href string, style []string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, ")") {
		return "", "", nil, false
	}
	separator := strings.Index(trimmed, "](")
	if separator <= 1 || separator+2 >= len(trimmed)-1 {
		return "", "", nil, false
	}
	text = strings.TrimSpace(trimmed[1:separator])
	href = strings.TrimSpace(trimmed[separator+2 : len(trimmed)-1])
	if text == "" || href == "" {
		return "", "", nil, false
	}
	if strings.Contains(text, "[") || strings.Contains(text, "]") || strings.Contains(href, " ") {
		return "", "", nil, false
	}
	if !strings.HasPrefix(href, "http://") && !strings.HasPrefix(href, "https://") {
		return "", "", nil, false
	}
	if strings.HasPrefix(text, "**") && strings.HasSuffix(text, "**") && len(text) > 4 {
		text = strings.TrimSpace(text[2 : len(text)-2])
		if text == "" {
			return "", "", nil, false
		}
		style = []string{"bold"}
	}
	return text, href, style, true
}

func buildPostContentWithInlineMentions(lines []string, title string, mentionUserOpenIDs []string) string {
	contentLines := make([][]map[string]any, 0, len(lines))
	mdLines := make([]string, 0, len(lines))
	flushMarkdown := func() {
		if len(mdLines) == 0 {
			return
		}
		mdText := strings.Join(mdLines, "\n")
		if strings.TrimSpace(mdText) != "" {
			contentLines = append(contentLines, []map[string]any{{"tag": "md", "text": mdText}})
		}
		mdLines = mdLines[:0]
	}
	for _, line := range lines {
		if !strings.Contains(line, mentionPlaceholderPrefix) {
			mdLines = append(mdLines, line)
			continue
		}
		flushMarkdown()
		row := buildInlineMentionRow(line, mentionUserOpenIDs)
		if len(row) == 0 {
			mdLines = append(mdLines, line)
			continue
		}
		contentLines = append(contentLines, row)
	}
	flushMarkdown()
	if len(contentLines) == 0 {
		contentLines = append(contentLines, []map[string]any{{"tag": "text", "text": " "}})
	}
	content := map[string]any{
		"zh_cn": map[string]any{
			"title":   title,
			"content": contentLines,
		},
	}
	data, _ := json.Marshal(content)
	return string(data)
}

func mentionUserOpenIDs(options providerapi.ReplyOptions) []string {
	ids := append([]string(nil), options.MentionUserIDs...)
	if len(ids) == 0 && strings.TrimSpace(options.MentionUserID) != "" {
		ids = append(ids, options.MentionUserID)
	}
	return normalizeMentionUserOpenIDs(ids)
}

func containsMentionPlaceholder(lines []string) bool {
	for _, line := range lines {
		if strings.Contains(line, mentionPlaceholderPrefix) {
			return true
		}
	}
	return false
}

func containsMarkdownMentions(lines []string) bool {
	for _, line := range lines {
		if strings.Contains(line, markdownMentionToken) {
			return true
		}
	}
	return false
}

func buildInlineMentionRow(line string, mentionUserOpenIDs []string) []map[string]any {
	row := make([]map[string]any, 0, 4)
	remaining := line
	for {
		index := strings.Index(remaining, mentionPlaceholderPrefix)
		if index < 0 {
			break
		}
		if index > 0 {
			row = append(row, map[string]any{"tag": "text", "text": remaining[:index]})
		}
		remaining = remaining[index+len(mentionPlaceholderPrefix):]
		suffixIndex := strings.Index(remaining, mentionPlaceholderSuffix)
		if suffixIndex < 0 {
			return nil
		}
		placeholderIndexText := remaining[:suffixIndex]
		remaining = remaining[suffixIndex+len(mentionPlaceholderSuffix):]
		placeholderIndex, err := strconv.Atoi(placeholderIndexText)
		if err != nil || placeholderIndex < 0 || placeholderIndex >= len(mentionUserOpenIDs) {
			return nil
		}
		row = append(row, map[string]any{"tag": "at", "user_id": mentionUserOpenIDs[placeholderIndex]})
	}
	if remaining != "" {
		row = append(row, map[string]any{"tag": "text", "text": remaining})
	}
	if len(row) == 0 {
		return nil
	}
	return row
}

func normalizeMentionUserOpenIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func splitLines(text string) []string {
	parts := strings.Split(text, "\n")
	if len(parts) == 0 {
		return []string{" "}
	}
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			result = append(result, " ")
			continue
		}
		result = append(result, part)
	}
	return result
}

func firstLineFromLines(lines []string) string {
	if len(lines) == 0 {
		return " "
	}
	return lines[0]
}

func remainingLinesFromLines(lines []string) []string {
	if len(lines) <= 1 {
		return nil
	}
	return lines[1:]
}

func detectImageMIME(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return "image/png"
	}
}
