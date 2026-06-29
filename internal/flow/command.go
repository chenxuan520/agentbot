package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/chenxuan520/agentbot/internal/backend"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/session"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

const helpText = "**可用命令**\n1. `/help` 查看帮助\n2. `/info` 查看当前会话信息\n3. `/peek` 查看当前 session 的最新输出快照\n4. `/roles` 查看或切换当前会话 role\n5. `/compress` 压缩当前 session 的上下文（opencode compact）\n6. `/new` 重置当前 session，下一条普通消息新建\n7. `/attach <session-id>` 绑定同一 workspace path 下的 opencode session\n8. `/clear` 清空当前会话绑定的 session\n9. `/abort` 中断当前会话里正在跑的 agent\n10. `/unblock` 解除当前会话的 refuse 屏蔽\n11. `/btw <text>` 使用你在当前对话里的独立 btw session 处理这条消息\n12. `/btw-clear` 清空你在当前对话里的 btw session\n13. `/connect-local` 把当前会话切到已连接的本地 agent\n14. `/connect-bot` 切回 bot（本地 agent 仍保持连接）\n15. `/disconnect-local` 强制断开本地 agent 连接\n\n_topic-session 模式：针对 session 的命令（`/peek` `/clear` `/new` `/compress` `/abort` `/attach`）作用于命令所在话题。在话题外执行 `/peek` `/compress` `/abort` `/attach` 会提示先进入话题；`/clear` `/new` 在话题外则重置整个会话的所有话题 session。_"

type commandResult struct {
	handled   bool
	replyText string
	viaAgent  bool
}

// topicCommandTopLevelHint is returned when a session-scoped command is issued
// at top level in topic-session reply mode, where there is no thread context to
// pick a topic from.
const topicCommandTopLevelHint = "topic-session 模式下，请在具体话题（消息所在 thread）里执行 `%s`。"

// commandSession describes which backend session a session-scoped command
// (/peek, /compress, /abort, /attach) should act on, accounting for the
// topic-session reply mode where each topic owns an isolated session.
type commandSession struct {
	topicMode bool   // conversation runs in topic-session reply mode
	topLevel  bool   // topicMode AND no thread context: command must refuse
	topicKey  string // resolved topic key when issued inside a thread
	sessionID string // target session id (main session, or the topic session)
}

// resolveCommandSession maps a command to its target session. In non-topic
// modes it keeps the historical behavior of acting on the main active session.
// In topic-session mode it binds to the topic the command was issued in, and
// flags top-level commands so callers can refuse with guidance.
func (s *Service) resolveCommandSession(ref conversation.Ref, input TextInput, current *session.CurrentSession) (commandSession, error) {
	replyMode := current.Workspace.Settings.Settings.ReplyMode
	if !workspace.IsTopicSessionReplyMode(replyMode) {
		return commandSession{sessionID: current.ActiveSessionID}, nil
	}
	topicKey := topicKeyForCommand(input, replyMode)
	if topicKey == "" {
		return commandSession{topicMode: true, topLevel: true}, nil
	}
	sessionID, err := s.sessions.TopicSessionID(ref, topicKey)
	if err != nil {
		return commandSession{}, err
	}
	return commandSession{topicMode: true, topicKey: topicKey, sessionID: sessionID}, nil
}

func (s *Service) handleCommand(ctx context.Context, ref conversation.Ref, input TextInput) (commandResult, error) {
	command, args := parseCommand(input.Text)
	if command == "" {
		return commandResult{}, nil
	}

	switch command {
	case "/help":
		return commandResult{handled: true, replyText: helpText}, nil
	case "/abort":
		current, err := s.sessions.Current(ref)
		if err != nil {
			return commandResult{}, err
		}
		target, err := s.resolveCommandSession(ref, input, current)
		if err != nil {
			return commandResult{}, err
		}
		if target.topLevel {
			return commandResult{handled: true, replyText: fmt.Sprintf(topicCommandTopLevelHint, "/abort")}, nil
		}
		if strings.TrimSpace(target.sessionID) == "" {
			return commandResult{handled: true, replyText: "当前对话没有可中断的 active session。"}, nil
		}
		client, err := s.backends(s.cfg, current.Workspace.Settings)
		if err != nil {
			return commandResult{}, err
		}
		if err := client.AbortSession(ctx, target.sessionID); err != nil {
			return commandResult{}, err
		}
		return commandResult{handled: true, replyText: "已向当前会话发送中断请求。"}, nil
	case "/unblock":
		cancelled, err := s.cancelActiveRefuse(ref)
		if err != nil {
			return commandResult{}, err
		}
		if cancelled == 0 {
			return commandResult{handled: true, replyText: "当前会话没有生效中的 refuse 屏蔽。"}, nil
		}
		return commandResult{handled: true, replyText: fmt.Sprintf("已解除当前会话的 refuse 屏蔽，共取消 %d 条规则。", cancelled)}, nil
	case "/info":
		current, err := s.sessions.CurrentForSender(ref, input.SenderID)
		if err != nil {
			return commandResult{}, err
		}
		topicKey := topicKeyForCommand(input, current.Workspace.Settings.Settings.ReplyMode)
		topicSessionID := ""
		if topicKey != "" {
			topicSessionID, err = s.sessions.TopicSessionID(ref, topicKey)
			if err != nil {
				return commandResult{}, err
			}
		}
		sessionToken := "-"
		if s.access != nil {
			token, err := s.access.EnsureSessionToken(ref)
			if err != nil {
				sessionToken = fmt.Sprintf("<unavailable: %s>", err)
			} else {
				sessionToken = token
			}
		}
		remoteEnabled := current.Workspace.Settings.Settings.RemoteEnabled
		remoteRoute := remoteRouteStatus(s.remote, ref, remoteEnabled)
		contextTokens := s.sessionContextTokens(ctx, current, topicSessionID)
		model := s.sessionModel(ctx, current)
		return commandResult{handled: true, replyText: buildInfoText(ref, current, sessionToken, topicKey, topicSessionID, s.cfg.WebBaseURL, remoteEnabled, remoteRoute, contextTokens, model)}, nil
	case "/peek":
		current, err := s.sessions.Current(ref)
		if err != nil {
			return commandResult{}, err
		}
		target, err := s.resolveCommandSession(ref, input, current)
		if err != nil {
			return commandResult{}, err
		}
		if target.topLevel {
			return commandResult{handled: true, replyText: fmt.Sprintf(topicCommandTopLevelHint, "/peek")}, nil
		}
		if strings.TrimSpace(target.sessionID) == "" {
			return commandResult{handled: true, replyText: noSessionHint(target, "/peek")}, nil
		}
		client, err := s.backends(s.cfg, current.Workspace.Settings)
		if err != nil {
			return commandResult{}, err
		}
		lookup, ok := client.(backend.SessionMessageLookup)
		if !ok {
			return commandResult{handled: true, replyText: fmt.Sprintf("当前 backend `%s` 不支持 `/peek`。", current.AgentBackend)}, nil
		}
		messages, err := lookup.GetSessionMessages(ctx, target.sessionID)
		if err != nil {
			return commandResult{handled: true, replyText: fmt.Sprintf("读取当前会话输出快照失败：%s", err)}, nil
		}
		transcriptURL := ""
		if s.access != nil {
			if token, err := s.access.EnsureSessionToken(ref); err == nil {
				transcriptURL = buildConsoleURLWithTab(s.cfg.WebBaseURL, token, "transcript")
			}
		}
		return commandResult{handled: true, replyText: buildPeekText(target.sessionID, messages, transcriptURL)}, nil
	case "/roles":
		if len(args) == 0 {
			current, err := s.sessions.Current(ref)
			if err != nil {
				return commandResult{}, err
			}
			roles, err := s.sessions.ListTemplates()
			if err != nil {
				return commandResult{}, err
			}
			return commandResult{handled: true, replyText: buildRolesText(current.Workspace.Settings.Template, roles)}, nil
		}
		if len(args) > 1 {
			return commandResult{handled: true, replyText: "用法：`/roles` 或 `/roles <template-name>`。"}, nil
		}
		ws, err := s.sessions.SwitchTemplate(ref, args[0])
		if err != nil {
			return commandResult{}, err
		}
		return commandResult{handled: true, replyText: fmt.Sprintf("已切换当前会话 role 到 `%s`。memory 与 hooks 已保留；下一条普通消息会按新 role 创建 session。", ws.Settings.Template)}, nil
	case "/compress":
		current, err := s.sessions.Current(ref)
		if err != nil {
			return commandResult{}, err
		}
		target, err := s.resolveCommandSession(ref, input, current)
		if err != nil {
			return commandResult{}, err
		}
		if target.topLevel {
			return commandResult{handled: true, replyText: fmt.Sprintf(topicCommandTopLevelHint, "/compress")}, nil
		}
		if strings.TrimSpace(target.sessionID) == "" {
			return commandResult{handled: true, replyText: noSessionHint(target, "/compress")}, nil
		}
		client, err := s.backends(s.cfg, current.Workspace.Settings)
		if err != nil {
			return commandResult{}, err
		}
		compactor, ok := client.(backend.SessionCompactor)
		if !ok {
			return commandResult{handled: true, replyText: fmt.Sprintf("当前 backend `%s` 不支持 `/compress`。", current.AgentBackend)}, nil
		}
		if err := compactor.CompactSession(ctx, current.Workspace.Path, target.sessionID); err != nil {
			return commandResult{}, err
		}
		return commandResult{handled: true, replyText: "已压缩当前 session 的上下文。"}, nil
	case "/new":
		current, err := s.sessions.Current(ref)
		if err != nil {
			return commandResult{}, err
		}
		outcome, err := s.clearCommandSessions(ref, input, current)
		if err != nil {
			return commandResult{}, err
		}
		return commandResult{handled: true, replyText: newSessionReply(outcome)}, nil
	case "/attach":
		if len(args) != 1 {
			return commandResult{handled: true, replyText: "用法：`/attach <session-id>`。"}, nil
		}
		current, err := s.sessions.Current(ref)
		if err != nil {
			return commandResult{}, err
		}
		target, err := s.resolveCommandSession(ref, input, current)
		if err != nil {
			return commandResult{}, err
		}
		if target.topLevel {
			return commandResult{handled: true, replyText: fmt.Sprintf(topicCommandTopLevelHint, "/attach")}, nil
		}
		if current.AgentBackend != "opencode" {
			return commandResult{handled: true, replyText: fmt.Sprintf("当前 backend `%s` 不支持 `/attach`，目前只支持 opencode session。", current.AgentBackend)}, nil
		}
		sessionID := strings.TrimSpace(args[0])
		if sessionID == "" {
			return commandResult{handled: true, replyText: "用法：`/attach <session-id>`。"}, nil
		}
		client, err := s.backends(s.cfg, current.Workspace.Settings)
		if err != nil {
			return commandResult{}, err
		}
		lookup, ok := client.(backend.SessionLookup)
		if !ok {
			return commandResult{handled: true, replyText: fmt.Sprintf("当前 backend `%s` 未实现 session 查询，无法校验 `/attach`。", current.AgentBackend)}, nil
		}
		info, err := lookup.GetSession(ctx, sessionID)
		if err != nil {
			return commandResult{handled: true, replyText: fmt.Sprintf("查询 session `%s` 失败：%s", sessionID, err)}, nil
		}
		if info.Directory != current.Workspace.Path {
			return commandResult{handled: true, replyText: "当前 `/attach` 只支持同一个 workspace path 下的 session，暂不支持跨 workspace attach。"}, nil
		}
		if err := s.bindCommandSession(ref, target, current.AgentBackend, sessionID); err != nil {
			return commandResult{}, err
		}
		return commandResult{handled: true, replyText: fmt.Sprintf("已将当前对话 attach 到 session `%s`。下一条普通消息会继续这个 session。", sessionID)}, nil
	case "/clear":
		current, err := s.sessions.Current(ref)
		if err != nil {
			return commandResult{}, err
		}
		outcome, err := s.clearCommandSessions(ref, input, current)
		if err != nil {
			return commandResult{}, err
		}
		return commandResult{handled: true, replyText: clearSessionReply(outcome)}, nil
	case "/btw":
		if len(args) == 0 {
			return commandResult{handled: true, replyText: "用法：`/btw <text>`。它会使用你在当前对话里的独立 btw session，不影响主 session。"}, nil
		}
		result, err := s.PromptConversationBTW(ctx, ref, input.SenderID, strings.TrimSpace(strings.Join(args, " ")))
		if err != nil {
			return commandResult{}, err
		}
		return commandResult{handled: true, replyText: result.ReplyText, viaAgent: true}, nil
	case "/btw-clear":
		current, err := s.sessions.CurrentForSender(ref, input.SenderID)
		if err != nil {
			return commandResult{}, err
		}
		hadSession := strings.TrimSpace(current.BTWSessionID) != ""
		if !hadSession {
			legacyCurrent, err := s.sessions.Current(ref)
			if err != nil {
				return commandResult{}, err
			}
			hadSession = strings.TrimSpace(legacyCurrent.BTWSessionID) != ""
		}
		if err := s.sessions.ClearBTW(ref, input.SenderID); err != nil {
			return commandResult{}, err
		}
		if hadSession {
			return commandResult{handled: true, replyText: "已清空你在当前对话的 btw session。主 session 不受影响。"}, nil
		}
		return commandResult{handled: true, replyText: "你在当前对话没有可清空的 btw session。"}, nil
	case "/connect-local":
		return s.handleConnectLocal(ref)
	case "/connect-bot":
		return s.handleConnectBot(ref)
	case "/disconnect-local":
		return s.handleDisconnectLocal(ref)
	default:
		return commandResult{handled: true, replyText: fmt.Sprintf("不支持的命令：`%s`\n\n%s", command, helpText)}, nil
	}
}

func (s *Service) handleConnectLocal(ref conversation.Ref) (commandResult, error) {
	if s.remote == nil {
		return commandResult{handled: true, replyText: "当前部署未启用本地 agent 接入。"}, nil
	}
	enabled, err := s.sessions.RemoteEnabled(ref)
	if err != nil {
		return commandResult{}, err
	}
	if !enabled {
		return commandResult{handled: true, replyText: "当前会话未开启本地 agent。先在 `.session-setting.json` 把 `settings.remoteEnabled` 设为 `true` 并 rebuild。"}, nil
	}
	if err := s.remote.ForceLocal(ref); err != nil {
		return commandResult{handled: true, replyText: "本地 agent 当前未连接，无法切到本地。请先在电脑上让插件连上来。"}, nil
	}
	return commandResult{handled: true, replyText: "已切到本地 agent。后续消息会转发给本地 agent 处理。"}, nil
}

func (s *Service) handleConnectBot(ref conversation.Ref) (commandResult, error) {
	if s.remote == nil {
		return commandResult{handled: true, replyText: "当前部署未启用本地 agent 接入。"}, nil
	}
	enabled, err := s.sessions.RemoteEnabled(ref)
	if err != nil {
		return commandResult{}, err
	}
	if !enabled {
		return commandResult{handled: true, replyText: "当前会话未开启本地 agent，消息本来就由 bot 处理。"}, nil
	}
	s.remote.ForceBot(ref)
	return commandResult{handled: true, replyText: "已切回 bot。后续消息由 bot 处理；本地 agent 仍保持连接，`/connect-local` 可再切回去。"}, nil
}

func (s *Service) handleDisconnectLocal(ref conversation.Ref) (commandResult, error) {
	if s.remote == nil {
		return commandResult{handled: true, replyText: "当前部署未启用本地 agent 接入。"}, nil
	}
	if !s.remote.Disconnect(ref) {
		return commandResult{handled: true, replyText: "本地 agent 当前未连接，无需断开。"}, nil
	}
	return commandResult{handled: true, replyText: "已强制断开本地 agent 连接，已切回 bot。若插件设置了自动重连，可能会重新连上并再次接管。"}, nil
}

func parseCommand(text string) (string, []string) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return "", nil
	}
	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return "", nil
	}
	return strings.ToLower(parts[0]), parts[1:]
}

func remoteRouteStatus(remote remoteRouter, ref conversation.Ref, enabled bool) string {
	if !enabled {
		return "disabled"
	}
	if remote == nil {
		return "unavailable"
	}
	if !remote.Connected(ref) {
		return "offline (走 bot)"
	}
	if remote.RouteIsLocal(ref) {
		return "local"
	}
	return "bot (已手动切回)"
}

// sessionContextTokens reports the latest assistant turn's token usage for the
// session /info is about (the topic session when inside a topic, otherwise the
// main active session). It is best-effort: any failure yields an empty string
// so /info still renders without the context line.
func (s *Service) sessionContextTokens(ctx context.Context, current *session.CurrentSession, topicSessionID string) string {
	sessionID := strings.TrimSpace(topicSessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(current.ActiveSessionID)
	}
	if sessionID == "" || s.backends == nil {
		return ""
	}
	client, err := s.backends(s.cfg, current.Workspace.Settings)
	if err != nil {
		return ""
	}
	lookup, ok := client.(backend.SessionMessageLookup)
	if !ok {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	messages, err := lookup.GetSessionMessages(ctx, sessionID)
	if err != nil {
		return ""
	}
	tokens, ok := backend.LatestContextTokens(messages)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d (input %d)", tokens.Total, tokens.Input)
}

// sessionModel reports the model /info should show as "in effect": the
// per-conversation override when set (passed per message), otherwise the
// backend's own default. It is best-effort: any failure yields an empty string
// so /info still renders.
func (s *Service) sessionModel(ctx context.Context, current *session.CurrentSession) string {
	if override := strings.TrimSpace(current.Workspace.Settings.Agent.Model); override != "" {
		return override
	}
	if s.backends == nil {
		return ""
	}
	client, err := s.backends(s.cfg, current.Workspace.Settings)
	if err != nil {
		return ""
	}
	catalog, ok := client.(backend.SessionModelCatalog)
	if !ok {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	model, err := catalog.CurrentModel(ctx, current.Workspace.Path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(model)
}

func buildInfoText(ref conversation.Ref, current *session.CurrentSession, sessionToken, topicKey, topicSessionID, webBaseURL string, remoteEnabled bool, remoteRoute, contextTokens, model string) string {
	consoleURL := buildConsoleURL(webBaseURL, sessionToken)
	lines := []string{
		linkedSectionTitle("当前会话", consoleURL),
		"```text",
		fmt.Sprintf("provider: %s", ref.Provider),
		fmt.Sprintf("conversation_id: %s", ref.ConversationID),
		fmt.Sprintf("workspace: %s", current.Workspace.Path),
		fmt.Sprintf("template: %s", current.Workspace.Settings.Template),
		fmt.Sprintf("skill_ids: %s", formatMountedIDs(current.Workspace.Settings.Mounts.SkillIDs)),
		fmt.Sprintf("subagent_ids: %s", formatMountedIDs(current.Workspace.Settings.Mounts.SubagentIDs)),
		fmt.Sprintf("session_web_token: %s", sessionToken),
	}
	if consoleURL != "" {
		lines = append(lines, fmt.Sprintf("console_url: %s", consoleURL))
	}
	lines = append(lines,
		"```",
		"",
		"**当前 agent**",
		"```text",
		fmt.Sprintf("backend: %s", current.AgentBackend),
		fmt.Sprintf("model: %s", defaultValue(model, "-")),
		fmt.Sprintf("remote_enabled: %t", remoteEnabled),
		fmt.Sprintf("remote_route: %s", remoteRoute),
		fmt.Sprintf("agents_mode: %s", current.Workspace.Settings.Agent.AgentsMode),
		fmt.Sprintf("opencode_config: %s", formatOpencodeConfig(current.Workspace.Settings.Agent.OpencodeConfig)),
		fmt.Sprintf("opencode_http_timeout_seconds: %d", current.Workspace.Settings.Agent.OpencodeHTTPTimeoutSeconds),
		fmt.Sprintf("session_id: %s", defaultValue(current.ActiveSessionID, "-")),
		fmt.Sprintf("btw_session_id: %s", defaultValue(current.BTWSessionID, "-")),
	)
	if workspace.IsTopicSessionReplyMode(current.Workspace.Settings.Settings.ReplyMode) {
		lines = append(lines,
			fmt.Sprintf("topic_id: %s", defaultValue(topicKey, "-")),
			fmt.Sprintf("topic_session_id: %s", defaultValue(topicSessionID, "-")),
		)
	}
	if strings.TrimSpace(contextTokens) != "" {
		lines = append(lines, fmt.Sprintf("context_tokens: %s", contextTokens))
	}
	lines = append(lines,
		fmt.Sprintf("reply_mode: %s", current.Workspace.Settings.Settings.ReplyMode),
		fmt.Sprintf("history_ttl_hours: %d", current.Workspace.Settings.Settings.HistoryTTLHours),
		fmt.Sprintf("accept_group_human_messages_without_mention: %t", current.Workspace.Settings.Settings.AcceptGroupHumanMessagesWithoutMention),
		fmt.Sprintf("accept_other_bot_messages: %t", current.Workspace.Settings.Settings.AcceptOtherBotMessages),
		fmt.Sprintf("accept_interactive_card_messages: %t", current.Workspace.Settings.Settings.AcceptInteractiveCardMessages),
		fmt.Sprintf("last_message_at: %s", formatTime(current.LastMessageAt)),
		"```",
	)
	return strings.Join(lines, "\n")
}

func buildConsoleURL(baseURL, sessionToken string) string {
	return buildConsoleURLWithTab(baseURL, sessionToken, "")
}

func buildConsoleURLWithTab(baseURL, sessionToken, tab string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	sessionToken = strings.TrimSpace(sessionToken)
	if baseURL == "" || sessionToken == "" || sessionToken == "-" {
		return ""
	}
	query := url.Values{}
	query.Set("token", sessionToken)
	if tab = strings.TrimSpace(tab); tab != "" {
		query.Set("tab", tab)
	}
	return baseURL + "/?" + query.Encode()
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Local().Format("2006-01-02 15:04:05")
}

func formatUnixMillis(value int64) string {
	if value <= 0 {
		return "-"
	}
	seconds := value / 1000
	nanos := (value % 1000) * int64(time.Millisecond)
	return formatTime(time.Unix(seconds, nanos))
}

func defaultValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func formatMountedIDs(ids []string) string {
	if len(ids) == 0 {
		return "-"
	}
	return strings.Join(ids, ",")
}

func formatOpencodeConfig(cfg map[string]any) string {
	if len(cfg) == 0 {
		return "-"
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return "<invalid>"
	}
	return string(data)
}

func buildRolesText(current string, roles []string) string {
	lines := []string{"**当前 role**", "```text", fmt.Sprintf("template: %s", defaultValue(current, "-")), "```", "", "**可切换 role**", "```text"}
	if len(roles) == 0 {
		lines = append(lines, "-")
	} else {
		lines = append(lines, strings.Join(roles, "\n"))
	}
	lines = append(lines, "```", "", "执行 `/roles <template-name>` 可切换当前会话 role。")
	return strings.Join(lines, "\n")
}

func buildPeekText(sessionID string, messages []backend.SessionMessage, transcriptURL string) string {
	transcriptURL = strings.TrimSpace(transcriptURL)
	lines := []string{
		linkedSectionTitle("当前输出快照", transcriptURL),
		"```text",
		fmt.Sprintf("session_id: %s", defaultValue(sessionID, "-")),
		fmt.Sprintf("transcript_messages: %d", len(messages)),
	}
	if transcriptURL != "" {
		lines = append(lines, fmt.Sprintf("transcript_url: %s", transcriptURL))
	}
	if len(messages) == 0 {
		lines = append(lines, "assistant_state: no_transcript_yet", "```")
		return strings.Join(lines, "\n")
	}
	latest := messages[len(messages)-1]
	lines = append(lines,
		fmt.Sprintf("latest_message_role: %s", defaultValue(strings.TrimSpace(latest.Role), "-")),
		fmt.Sprintf("latest_message_id: %s", defaultValue(strings.TrimSpace(latest.ID), "-")),
		fmt.Sprintf("latest_message_at: %s", formatUnixMillis(latest.CreatedAt)),
	)

	latestUser := latestMessageByRole(messages, "user")
	if latestUser != nil {
		lines = append(lines, fmt.Sprintf("latest_user_text: %s", defaultValue(truncatePeekText(visibleText(*latestUser)), "-")))
	}

	latestAssistant := latestMessageByRole(messages, "assistant")
	if latestAssistant == nil {
		lines = append(lines, "assistant_state: no_assistant_output_yet", "```")
		return strings.Join(lines, "\n")
	}

	finishReason := latestFinishReason(*latestAssistant)
	lines = append(lines,
		fmt.Sprintf("assistant_message_id: %s", defaultValue(strings.TrimSpace(latestAssistant.ID), "-")),
		fmt.Sprintf("assistant_message_at: %s", formatUnixMillis(latestAssistant.CreatedAt)),
		fmt.Sprintf("assistant_state: %s", assistantState(latest, *latestAssistant, finishReason)),
		fmt.Sprintf("assistant_part_types: %s", defaultValue(strings.Join(partTypes(*latestAssistant), ","), "-")),
		fmt.Sprintf("finish_reason: %s", defaultValue(finishReason, "-")),
		fmt.Sprintf("tool_calls: %s", defaultValue(strings.Join(toolSummaries(*latestAssistant), ", "), "-")),
		"visible_text:",
		defaultValue(truncatePeekText(visibleText(*latestAssistant)), "-"),
		"```",
	)
	return strings.Join(lines, "\n")
}

func linkedSectionTitle(title, href string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "** **"
	}
	href = strings.TrimSpace(href)
	if href == "" {
		return fmt.Sprintf("**%s**", title)
	}
	return fmt.Sprintf("[**%s**](%s)", title, href)
}

func latestMessageByRole(messages []backend.SessionMessage, role string) *backend.SessionMessage {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(messages[i].Role), role) {
			return &messages[i]
		}
	}
	return nil
}

func latestFinishReason(message backend.SessionMessage) string {
	for i := len(message.Parts) - 1; i >= 0; i-- {
		part := message.Parts[i]
		if strings.TrimSpace(part.Type) != "step-finish" {
			continue
		}
		if reason := strings.TrimSpace(part.Reason); reason != "" {
			return reason
		}
	}
	return ""
}

func assistantState(latest backend.SessionMessage, assistant backend.SessionMessage, finishReason string) string {
	if latest.ID != "" && latest.ID == assistant.ID {
		if finishReason == "" {
			return "running"
		}
		if finishReason == "tool-calls" {
			return "tool_calls_pending"
		}
		return "completed"
	}
	if strings.EqualFold(strings.TrimSpace(latest.Role), "user") {
		return "waiting_for_assistant"
	}
	if finishReason == "tool-calls" {
		return "tool_calls_pending"
	}
	if finishReason == "" {
		return "running"
	}
	return "completed"
}

func partTypes(message backend.SessionMessage) []string {
	types := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		partType := strings.TrimSpace(part.Type)
		if partType == "" {
			continue
		}
		types = append(types, partType)
	}
	return types
}

func toolSummaries(message backend.SessionMessage) []string {
	tools := []string{}
	for _, part := range message.Parts {
		if strings.TrimSpace(part.Type) != "tool" {
			continue
		}
		toolName := strings.TrimSpace(part.Tool)
		if toolName == "" {
			toolName = "tool"
		}
		status := strings.TrimSpace(part.ToolStatus)
		if status != "" {
			tools = append(tools, fmt.Sprintf("%s(%s)", toolName, status))
			continue
		}
		tools = append(tools, toolName)
	}
	return tools
}

func visibleText(message backend.SessionMessage) string {
	texts := []string{}
	for _, part := range message.Parts {
		if strings.TrimSpace(part.Type) != "text" {
			continue
		}
		text := strings.TrimSpace(part.Text)
		if text == "" {
			continue
		}
		texts = append(texts, text)
	}
	return strings.Join(texts, "\n\n")
}

func truncatePeekText(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	const limit = 600
	if len(trimmed) <= limit {
		return trimmed
	}
	return strings.TrimSpace(trimmed[:limit]) + "...(truncated)"
}

func (s *Service) cancelActiveRefuse(ref conversation.Ref) (int, error) {
	if s.control == nil {
		return 0, nil
	}
	return s.control.CancelActiveRefuse(ref, time.Now().UTC())
}

// noSessionHint phrases the "no session yet" reply for a session-scoped command,
// distinguishing the per-topic case from the conversation-wide one.
func noSessionHint(target commandSession, command string) string {
	if target.topicMode {
		return fmt.Sprintf("这个话题还没有 session。先在该话题里发一条普通消息创建 session，再执行 `%s`。", command)
	}
	return fmt.Sprintf("当前对话没有 active session。先发一条普通消息创建 session，再执行 `%s`。", command)
}

// bindCommandSession persists a session id produced by a command back to the
// slot the command targeted: the per-topic session in topic-session mode, or
// the main session otherwise.
func (s *Service) bindCommandSession(ref conversation.Ref, target commandSession, backendName, sessionID string) error {
	now := time.Now().UTC()
	if target.topicMode {
		return s.sessions.BindTopic(ref, target.topicKey, backendName, sessionID, now)
	}
	return s.sessions.Bind(ref, backendName, sessionID, now)
}

// commandClearOutcome captures what a /clear or /new reset actually did so the
// command can phrase its reply.
type commandClearOutcome struct {
	scope string // "main", "topic", or "all"
	had   bool   // whether anything was cleared
}

// clearCommandSessions resets the session(s) a /clear or /new should affect: the
// current topic when issued inside a thread, every topic plus the main session
// at top level in topic-session mode, or just the main session otherwise.
func (s *Service) clearCommandSessions(ref conversation.Ref, input TextInput, current *session.CurrentSession) (commandClearOutcome, error) {
	target, err := s.resolveCommandSession(ref, input, current)
	if err != nil {
		return commandClearOutcome{}, err
	}
	switch {
	case target.topicMode && target.topLevel:
		had, err := s.hasAnySession(ref, current)
		if err != nil {
			return commandClearOutcome{}, err
		}
		if err := s.sessions.ClearAllTopics(ref); err != nil {
			return commandClearOutcome{}, err
		}
		if err := s.sessions.ClearActive(ref); err != nil {
			return commandClearOutcome{}, err
		}
		return commandClearOutcome{scope: "all", had: had}, nil
	case target.topicMode:
		had := strings.TrimSpace(target.sessionID) != ""
		if err := s.sessions.ClearTopic(ref, target.topicKey); err != nil {
			return commandClearOutcome{}, err
		}
		return commandClearOutcome{scope: "topic", had: had}, nil
	default:
		had := strings.TrimSpace(current.ActiveSessionID) != ""
		if err := s.sessions.ClearActive(ref); err != nil {
			return commandClearOutcome{}, err
		}
		return commandClearOutcome{scope: "main", had: had}, nil
	}
}

// hasAnySession reports whether the conversation has any session worth clearing,
// counting both the main slot and per-topic sessions.
func (s *Service) hasAnySession(ref conversation.Ref, current *session.CurrentSession) (bool, error) {
	if strings.TrimSpace(current.ActiveSessionID) != "" {
		return true, nil
	}
	return s.sessions.HasTopicSessions(ref)
}

func clearSessionReply(outcome commandClearOutcome) string {
	switch outcome.scope {
	case "topic":
		if outcome.had {
			return "已清空当前话题的 session。"
		}
		return "当前话题没有可清空的 session。"
	case "all":
		if outcome.had {
			return "已清空当前会话的所有话题 session（整会话重置）。"
		}
		return "当前会话没有可清空的 session。"
	default:
		if outcome.had {
			return "已清空当前对话的 session。"
		}
		return "当前对话没有可清空的 session。"
	}
}

func newSessionReply(outcome commandClearOutcome) string {
	switch outcome.scope {
	case "topic":
		if outcome.had {
			return "已清空当前话题的旧 session。该话题的下一条消息会创建新的 session。"
		}
		return "当前话题还没有 session。该话题的下一条消息会创建新的 session。"
	case "all":
		if outcome.had {
			return "已清空当前会话的所有话题 session（整会话重置）。下一条消息会创建新的 session。"
		}
		return "当前会话还没有 session。下一条消息会创建新的 session。"
	default:
		if outcome.had {
			return "已清空当前对话的旧 session。下一条消息会创建新的 session。"
		}
		return "当前对话还没有 session。下一条消息会创建新的 session。"
	}
}
