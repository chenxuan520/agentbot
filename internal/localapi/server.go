package localapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/chenxuan520/agentbot/internal/accesstoken"
	"github.com/chenxuan520/agentbot/internal/backend"
	"github.com/chenxuan520/agentbot/internal/backendfactory"
	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/control"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/flow"
	"github.com/chenxuan520/agentbot/internal/provider"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
	"github.com/chenxuan520/agentbot/internal/remoteagent"
	"github.com/chenxuan520/agentbot/internal/scheduler"
	"github.com/chenxuan520/agentbot/internal/session"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

type providerFactory func(config.Config, string) (providerapi.Client, error)
type backendFactory func(config.Config, workspace.Settings) (backend.Client, error)
type processTextFunc func(context.Context, flow.TextInput) (flow.PromptResult, error)

// RemoteAgentServer upgrades an authenticated request to a local agent plugin
// WebSocket bound to ref, and reports a conversation's live connection status.
// It is satisfied by *remoteagent.Hub and injected via SetRemoteAgentServer.
type RemoteAgentServer interface {
	Serve(w http.ResponseWriter, r *http.Request, ref conversation.Ref) error
	Status(ref conversation.Ref) remoteagent.Status
}

type Server struct {
	cfg       config.Config
	control   *control.Service
	scheduler *scheduler.Service
	prompts   *scheduler.PromptFileStore
	providers providerFactory
	backends  backendFactory
	process   processTextFunc
	sessions  *session.Service
	access    *accesstoken.Service
	remote    RemoteAgentServer
}

func New(cfg config.Config, controlService *control.Service, schedulerService *scheduler.Service, sessionService *session.Service, flowService *flow.Service, accessServices ...*accesstoken.Service) *Server {
	var accessService *accesstoken.Service
	if len(accessServices) > 0 {
		accessService = accessServices[0]
	}
	server := &Server{cfg: cfg, control: controlService, scheduler: schedulerService, prompts: scheduler.NewPromptFileStore(cfg), providers: provider.FromConfig, backends: backendfactory.FromSettings, sessions: sessionService, access: accessService}
	if flowService != nil {
		server.process = flowService.ProcessText
	}
	return server
}

// SetRemoteAgentServer wires the remote-agent hub that serves the plugin
// WebSocket endpoint. When unset, the endpoint reports the feature is disabled.
func (s *Server) SetRemoteAgentServer(remote RemoteAgentServer) {
	s.remote = remote
}

func (s *Server) BaseURL() string {
	return s.cfg.LocalAPIBaseURL()
}

func (s *Server) Listen(ctx context.Context) error {
	gin.SetMode(gin.ReleaseMode)
	router := s.router()

	server := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", s.cfg.ServerHost, s.cfg.ServerPort),
		Handler: router,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s *Server) router() *gin.Engine {
	router := gin.New()
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	router.POST("/api/v1/control/refuse", s.handleControlRefuse)
	router.GET("/api/v1/control/active", s.handleControlActive)
	router.POST("/api/v1/control/cancel", s.handleControlCancel)
	router.GET("/api/v1/remote-agent/ws", s.handleRemoteAgentWS)
	router.GET("/api/v1/provider/chat-members", s.handleProviderChatMembers)
	router.GET("/api/v1/provider/chat-messages", s.handleProviderChatMessages)
	router.POST("/api/v1/provider/recall-message", s.handleProviderRecallMessage)
	router.POST("/api/v1/provider/mock-message", s.handleProviderMockMessage)
	router.GET("/api/v1/schedule", s.handleScheduleList)
	router.POST("/api/v1/schedule", s.handleScheduleCreate)
	router.PUT("/api/v1/schedule", s.handleScheduleUpdate)
	router.POST("/api/v1/schedule/cancel", s.handleScheduleCancel)
	router.GET("/api/v1/session/settings", s.handleSessionSettingsGet)
	router.POST("/api/v1/session/settings", s.handleSessionSettingsUpdate)
	admin := router.Group("/api/v1/admin")
	admin.Use(s.requireAdminToken)
	admin.GET("/me", s.handleAdminMe)
	admin.GET("/observability", s.handleAdminObservability)
	admin.GET("/sessions", s.handleAdminSessionList)
	admin.POST("/sessions/display-names", s.handleAdminSessionDisplayNames)
	admin.GET("/sessions/:provider/:conversationId", s.handleAdminSessionDetail)
	admin.GET("/sessions/:provider/:conversationId/transcript", s.handleAdminSessionTranscript)
	admin.GET("/sessions/:provider/:conversationId/remote-status", s.handleAdminRemoteStatus)
	admin.DELETE("/sessions/:provider/:conversationId", s.handleAdminSessionDelete)
	admin.GET("/sessions/:provider/:conversationId/session-skills", s.handleAdminSessionSkillList)
	admin.POST("/sessions/:provider/:conversationId/session-skills", s.handleAdminSessionSkillCreate)
	admin.POST("/sessions/:provider/:conversationId/session-skills/upload", s.handleAdminSessionSkillUpload)
	admin.GET("/sessions/:provider/:conversationId/session-skills/:skillId", s.handleAdminSessionSkillGet)
	admin.GET("/sessions/:provider/:conversationId/session-skills/:skillId/files", s.handleAdminSessionSkillFilesList)
	admin.GET("/sessions/:provider/:conversationId/session-skills/:skillId/files/content", s.handleAdminSessionSkillFileGet)
	admin.PUT("/sessions/:provider/:conversationId/session-skills/:skillId/files/content", s.handleAdminSessionSkillFileUpdate)
	admin.DELETE("/sessions/:provider/:conversationId/session-skills/:skillId", s.handleAdminSessionSkillDelete)
	admin.GET("/sessions/:provider/:conversationId/session-data/export", s.handleAdminSessionDataExport)
	admin.POST("/sessions/:provider/:conversationId/session-data/import", s.handleAdminSessionDataImport)
	admin.GET("/sessions/:provider/:conversationId/agents", s.handleAdminSessionAgentsGet)
	admin.PUT("/sessions/:provider/:conversationId/agents", s.handleAdminSessionAgentsUpdate)
	admin.PUT("/sessions/:provider/:conversationId/settings", s.handleAdminSessionSettingsUpdate)
	admin.POST("/sessions/:provider/:conversationId/token/rotate", s.handleAdminSessionTokenRotate)
	admin.GET("/sessions/:provider/:conversationId/files/:kind", s.handleAdminSessionFilesList)
	admin.GET("/sessions/:provider/:conversationId/files/:kind/content", s.handleAdminSessionFileGet)
	admin.PUT("/sessions/:provider/:conversationId/files/:kind/content", s.handleAdminSessionFileUpdate)
	admin.GET("/roles", s.handleAdminRoleList)
	admin.POST("/roles", s.handleAdminRoleCreate)
	admin.GET("/roles/:roleId", s.handleAdminRoleGet)
	admin.PUT("/roles/:roleId", s.handleAdminRoleUpdate)
	admin.DELETE("/roles/:roleId", s.handleAdminRoleDelete)
	admin.GET("/subagents", s.handleAdminSubagentList)
	admin.POST("/subagents", s.handleAdminSubagentCreate)
	admin.GET("/subagents/:subagentId", s.handleAdminSubagentGet)
	admin.PUT("/subagents/:subagentId", s.handleAdminSubagentUpdate)
	admin.DELETE("/subagents/:subagentId", s.handleAdminSubagentDelete)
	admin.GET("/skills", s.handleAdminSkillList)
	admin.POST("/skills", s.handleAdminSkillCreate)
	admin.POST("/skills/upload", s.handleAdminSkillUpload)
	admin.GET("/skills/:skillId", s.handleAdminSkillGet)
	admin.GET("/skills/:skillId/files", s.handleAdminSkillFilesList)
	admin.GET("/skills/:skillId/files/content", s.handleAdminSkillFileGet)
	admin.PUT("/skills/:skillId/files/content", s.handleAdminSkillFileUpdate)
	admin.DELETE("/skills/:skillId", s.handleAdminSkillDelete)
	admin.GET("/repos", s.handleAdminRepoList)
	admin.POST("/repos", s.handleAdminRepoClone)
	admin.GET("/repos/:repoId/branches", s.handleAdminRepoBranches)
	admin.POST("/repos/:repoId/pull", s.handleAdminRepoPull)
	admin.POST("/repos/:repoId/checkout", s.handleAdminRepoCheckout)
	admin.GET("/scripts", s.handleAdminScriptsList)
	admin.GET("/scripts/content", s.handleAdminScriptFileGet)
	admin.PUT("/scripts/content", s.handleAdminScriptFileUpdate)
	return router
}
func (s *Server) handleRemoteAgentWS(c *gin.Context) {
	if s.remote == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "remote agent endpoint is not enabled"})
		return
	}
	if s.access == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "session token store is not configured"})
		return
	}
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		token = bearerToken(c.GetHeader("Authorization"))
	}
	scope, err := s.access.Validate(token)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	if scope.Kind != accesstoken.ScopeSession {
		c.JSON(http.StatusForbidden, gin.H{"error": "a session token is required"})
		return
	}
	ref := scope.Ref
	if s.sessions != nil {
		enabled, err := s.sessions.RemoteEnabled(ref)
		if err != nil {
			writeError(c, err)
			return
		}
		if !enabled {
			c.JSON(http.StatusForbidden, gin.H{"error": "remote agent is not enabled for this conversation"})
			return
		}
	}
	// Serve upgrades the connection and blocks until the plugin disconnects.
	_ = s.remote.Serve(c.Writer, c.Request, ref)
}

func bearerToken(header string) string {
	header = strings.TrimSpace(header)
	const prefix = "Bearer "
	if len(header) >= len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}

func (s *Server) handleControlRefuse(c *gin.Context) {
	var body struct {
		Provider       string `json:"provider"`
		ConversationID string `json:"conversationId"`
		UntilAt        string `json:"untilAt"`
		Reason         string `json:"reason"`
	}
	if !bindJSON(c, &body) {
		return
	}
	untilAt, err := time.Parse(time.RFC3339, body.UntilAt)
	if err != nil {
		writeError(c, err)
		return
	}
	result, err := s.control.Refuse(conversation.Ref{Provider: body.Provider, ConversationID: body.ConversationID}, untilAt, body.Reason)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) handleControlActive(c *gin.Context) {
	ref := conversation.Ref{Provider: c.Query("provider"), ConversationID: c.Query("conversationId")}
	result, err := s.control.Active(ref, time.Now().UTC())
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) handleControlCancel(c *gin.Context) {
	var body struct {
		RuleID string `json:"ruleId"`
	}
	if !bindJSON(c, &body) {
		return
	}
	if err := s.control.Cancel(body.RuleID); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleProviderChatMembers(c *gin.Context) {
	providerName := strings.TrimSpace(c.Query("provider"))
	conversationID := strings.TrimSpace(c.Query("conversationId"))
	if providerName == "" || conversationID == "" {
		writeError(c, fmt.Errorf("provider and conversationId are required"))
		return
	}
	client, err := s.providers(s.cfg, providerName)
	if err != nil {
		writeError(c, err)
		return
	}
	lister, ok := client.(providerapi.ChatMemberLister)
	if !ok {
		writeError(c, fmt.Errorf("provider %q does not support chat member listing", providerName))
		return
	}
	items, err := lister.ListChatMembers(c.Request.Context(), conversationID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"provider":       providerName,
		"conversationId": conversationID,
		"items":          items,
	})
}

func (s *Server) handleProviderChatMessages(c *gin.Context) {
	providerName := strings.TrimSpace(c.Query("provider"))
	conversationID := strings.TrimSpace(c.Query("conversationId"))
	if providerName == "" || conversationID == "" {
		writeError(c, fmt.Errorf("provider and conversationId are required"))
		return
	}
	client, err := s.providers(s.cfg, providerName)
	if err != nil {
		writeError(c, err)
		return
	}
	lister, ok := client.(providerapi.ChatMessageLister)
	if !ok {
		writeError(c, fmt.Errorf("provider %q does not support chat message listing", providerName))
		return
	}
	pageSize := 20
	if value := strings.TrimSpace(c.Query("pageSize")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			writeError(c, err)
			return
		}
		pageSize = parsed
	}
	result, err := lister.ListChatMessages(c.Request.Context(), conversationID, providerapi.ChatMessageListOptions{
		StartTime:          strings.TrimSpace(c.Query("startTime")),
		EndTime:            strings.TrimSpace(c.Query("endTime")),
		SortType:           strings.TrimSpace(c.Query("sortType")),
		PageSize:           pageSize,
		PageToken:          strings.TrimSpace(c.Query("pageToken")),
		CardMsgContentType: strings.TrimSpace(c.Query("cardMsgContentType")),
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"provider":       providerName,
		"conversationId": conversationID,
		"hasMore":        result.HasMore,
		"pageToken":      result.PageToken,
		"items":          result.Items,
	})
}

func (s *Server) handleProviderRecallMessage(c *gin.Context) {
	var body struct {
		Provider       string `json:"provider"`
		ConversationID string `json:"conversationId"`
		MessageID      string `json:"messageId"`
	}
	if !bindJSON(c, &body) {
		return
	}
	providerName := strings.TrimSpace(body.Provider)
	messageID := strings.TrimSpace(body.MessageID)
	if providerName == "" || messageID == "" {
		writeError(c, fmt.Errorf("provider and messageId are required"))
		return
	}
	client, err := s.providers(s.cfg, providerName)
	if err != nil {
		writeError(c, err)
		return
	}
	recaller, ok := client.(providerapi.MessageRecaller)
	if !ok {
		writeError(c, fmt.Errorf("provider %q does not support message recall", providerName))
		return
	}
	if err := recaller.RecallMessage(c.Request.Context(), messageID); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":             true,
		"provider":       providerName,
		"conversationId": strings.TrimSpace(body.ConversationID),
		"messageId":      messageID,
	})
}

func (s *Server) handleProviderMockMessage(c *gin.Context) {
	if s.process == nil {
		writeError(c, fmt.Errorf("flow service is not configured"))
		return
	}
	var body struct {
		Provider         string `json:"provider"`
		ConversationID   string `json:"conversationId"`
		ConversationType string `json:"conversationType"`
		MessageType      string `json:"messageType"`
		MessageID        string `json:"messageId"`
		RootMessageID    string `json:"rootMessageId"`
		ParentMessageID  string `json:"parentMessageId"`
		ThreadID         string `json:"threadId"`
		SenderType       string `json:"senderType"`
		SenderID         string `json:"senderId"`
		SystemText       string `json:"systemText"`
		Text             string `json:"text"`
		AddReaction      *bool  `json:"addReaction"`
		Async            *bool  `json:"async"`
	}
	if !bindJSON(c, &body) {
		return
	}
	addReaction := false
	if body.AddReaction != nil {
		addReaction = *body.AddReaction
	}
	async := true
	if body.Async != nil {
		async = *body.Async
	}
	input := flow.TextInput{
		Provider:         body.Provider,
		ConversationID:   body.ConversationID,
		ConversationType: body.ConversationType,
		MessageType:      body.MessageType,
		MessageID:        body.MessageID,
		RootMessageID:    body.RootMessageID,
		ParentMessageID:  body.ParentMessageID,
		ThreadID:         body.ThreadID,
		SenderType:       body.SenderType,
		SenderID:         body.SenderID,
		SystemText:       body.SystemText,
		Text:             body.Text,
		AddReaction:      addReaction,
	}
	if async {
		go func() {
			_, _ = s.process(context.Background(), input)
		}()
		c.JSON(http.StatusOK, gin.H{"accepted": true, "async": true})
		return
	}
	result, err := s.process(c.Request.Context(), input)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) handleScheduleCreate(c *gin.Context) {
	var body struct {
		Provider       string         `json:"provider"`
		ConversationID string         `json:"conversationId"`
		RunAt          string         `json:"runAt"`
		Cron           string         `json:"cron"`
		Timezone       string         `json:"timezone"`
		Route          string         `json:"route"`
		Payload        map[string]any `json:"payload"`
	}
	if !bindJSON(c, &body) {
		return
	}
	ref := conversation.Ref{Provider: body.Provider, ConversationID: body.ConversationID}
	payload := scheduler.ApplyCronPayload(body.Payload, body.Cron, body.Timezone)
	if strings.TrimSpace(body.Cron) != "" && s.prompts != nil {
		var err error
		payload, err = s.prompts.MaterializeRecurringPayload(ref, body.Route, payload)
		if err != nil {
			writeError(c, err)
			return
		}
	}
	if err := scheduler.ValidateMessageDeliveryPayload(payload); err != nil {
		writeError(c, err)
		return
	}
	runAt, err := scheduler.FirstRunAt(body.RunAt, body.Cron, body.Timezone, time.Now().UTC())
	if err != nil {
		writeError(c, err)
		return
	}
	result, err := s.scheduler.Schedule(ref, body.Route, mustJSON(payload), runAt)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) handleScheduleList(c *gin.Context) {
	ref := conversation.Ref{Provider: c.Query("provider"), ConversationID: c.Query("conversationId")}
	limit := 100
	includeDone := false
	if value := c.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			writeError(c, err)
			return
		}
		limit = parsed
	}
	if value := strings.TrimSpace(c.Query("includeDone")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			writeError(c, err)
			return
		}
		includeDone = parsed
	}
	result, err := s.listScheduleItems(ref, includeDone, limit)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) handleScheduleCancel(c *gin.Context) {
	var body struct {
		JobID string `json:"jobId"`
	}
	if !bindJSON(c, &body) {
		return
	}
	if err := s.scheduler.Cancel(body.JobID); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleSessionSettingsGet(c *gin.Context) {
	if s.sessions == nil {
		writeError(c, fmt.Errorf("session service is not configured"))
		return
	}
	ref := conversation.Ref{Provider: c.Query("provider"), ConversationID: c.Query("conversationId")}
	current, err := s.sessions.Current(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"provider":       ref.Provider,
		"conversationId": ref.ConversationID,
		"workspacePath":  current.Workspace.Path,
		"settings":       current.Workspace.Settings,
	})
}

func (s *Server) handleSessionSettingsUpdate(c *gin.Context) {
	if s.sessions == nil {
		writeError(c, fmt.Errorf("session service is not configured"))
		return
	}
	var body struct {
		Provider       string             `json:"provider"`
		ConversationID string             `json:"conversationId"`
		Settings       workspace.Settings `json:"settings"`
		Rebuild        *bool              `json:"rebuild"`
	}
	if !bindJSON(c, &body) {
		return
	}
	ref := conversation.Ref{Provider: body.Provider, ConversationID: body.ConversationID}
	current, err := s.sessions.Current(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	if strings.TrimSpace(body.Settings.Agent.AgentsMode) == "" {
		body.Settings.Agent.AgentsMode = current.Workspace.Settings.Agent.AgentsMode
	}
	if err := workspace.SaveSettings(current.Workspace.Path, body.Settings); err != nil {
		writeError(c, err)
		return
	}
	rebuild := true
	if body.Rebuild != nil {
		rebuild = *body.Rebuild
	}
	if rebuild {
		result, err := s.sessions.RebuildWorkspace(ref)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"provider":       ref.Provider,
			"conversationId": ref.ConversationID,
			"workspacePath":  result.Path,
			"settings":       result.Settings,
			"rebuilt":        true,
		})
		return
	}
	if err := s.sessions.SyncLegacyBotPoller(ref); err != nil {
		writeError(c, err)
		return
	}
	updated, err := s.sessions.Current(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"provider":       ref.Provider,
		"conversationId": ref.ConversationID,
		"workspacePath":  updated.Workspace.Path,
		"settings":       updated.Workspace.Settings,
		"rebuilt":        false,
	})
}

func bindJSON(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		writeError(c, err)
		return false
	}
	return true
}

func writeError(c *gin.Context, err error) {
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
}

func mustJSON(value any) string {
	if value == nil {
		return "{}"
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}
