package session

import (
	"os"
	"strings"
	"time"

	"github.com/chenxuan520/agentbot/internal/conversation"
	appstore "github.com/chenxuan520/agentbot/internal/store"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

type Service struct {
	store      appstore.WorkspaceStore
	workspaces *workspace.Manager
}

type PreparedSession struct {
	Workspace       *workspace.RuntimeWorkspace
	AgentBackend    string
	ActiveSessionID string
	NeedNewSession  bool
}

type CurrentSession struct {
	Workspace       *workspace.RuntimeWorkspace
	AgentBackend    string
	ActiveSessionID string
	BTWSessionID    string
	LastMessageAt   time.Time
}

type Summary struct {
	Provider        string    `json:"provider"`
	ConversationID  string    `json:"conversationId"`
	WorkspacePath   string    `json:"workspacePath"`
	Template        string    `json:"template"`
	AgentBackend    string    `json:"agentBackend"`
	ActiveSessionID string    `json:"activeSessionID"`
	BTWSessionID    string    `json:"btwSessionID"`
	ReplyMode       string    `json:"replyMode"`
	SkillIDs        []string  `json:"skillIDs"`
	SubagentIDs     []string  `json:"subagentIDs"`
	LastMessageAt   time.Time `json:"lastMessageAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type SessionSlot string

const (
	SessionSlotMain SessionSlot = "main"
	SessionSlotBTW  SessionSlot = "btw"
)

type GroupMessagePolicy struct {
	AcceptHumanMessagesWithoutMention bool
}

func NewService(store appstore.WorkspaceStore, workspaces *workspace.Manager) *Service {
	return &Service{store: store, workspaces: workspaces}
}

func (s *Service) Prepare(ref conversation.Ref, now time.Time) (*PreparedSession, error) {
	return s.PrepareSlot(ref, SessionSlotMain, now)
}

func (s *Service) PrepareSlot(ref conversation.Ref, slot SessionSlot, now time.Time) (*PreparedSession, error) {
	if slot == SessionSlotBTW {
		return s.PrepareBTW(ref, "", now)
	}
	ws, err := s.workspaces.Ensure(ref)
	if err != nil {
		return nil, err
	}

	record := ws.Record
	activeSessionID := record.ActiveSessionID
	needNewSession := activeSessionID == ""
	if !needNewSession && ws.Settings.Settings.HistoryTTLHours > 0 && !record.LastMessageAt.IsZero() {
		ttl := time.Duration(ws.Settings.Settings.HistoryTTLHours) * time.Hour
		needNewSession = now.UTC().Sub(record.LastMessageAt) >= ttl
	}

	if needNewSession {
		activeSessionID = ""
	}

	return &PreparedSession{
		Workspace:       ws,
		AgentBackend:    ws.Settings.Agent.Backend,
		ActiveSessionID: activeSessionID,
		NeedNewSession:  needNewSession,
	}, nil
}

func (s *Service) PrepareBTW(ref conversation.Ref, senderID string, now time.Time) (*PreparedSession, error) {
	ws, err := s.workspaces.Ensure(ref)
	if err != nil {
		return nil, err
	}

	activeSessionID, err := s.resolveBTWSessionID(ref, normalizeBTWSenderID(senderID), &ws.Record)
	if err != nil {
		return nil, err
	}
	needNewSession := strings.TrimSpace(activeSessionID) == ""
	if needNewSession {
		activeSessionID = ""
	}

	return &PreparedSession{
		Workspace:       ws,
		AgentBackend:    ws.Settings.Agent.Backend,
		ActiveSessionID: activeSessionID,
		NeedNewSession:  needNewSession,
	}, nil
}

func (s *Service) Current(ref conversation.Ref) (*CurrentSession, error) {
	return s.CurrentForSender(ref, "")
}

func (s *Service) CurrentForSender(ref conversation.Ref, senderID string) (*CurrentSession, error) {
	ws, err := s.workspaces.Ensure(ref)
	if err != nil {
		return nil, err
	}
	btwSessionID, err := s.resolveBTWSessionID(ref, normalizeBTWSenderID(senderID), &ws.Record)
	if err != nil {
		return nil, err
	}

	return &CurrentSession{
		Workspace:       ws,
		AgentBackend:    ws.Settings.Agent.Backend,
		ActiveSessionID: ws.Record.ActiveSessionID,
		BTWSessionID:    btwSessionID,
		LastMessageAt:   ws.Record.LastMessageAt,
	}, nil
}

func (s *Service) List() ([]Summary, error) {
	records, err := s.store.List()
	if err != nil {
		return nil, err
	}
	result := make([]Summary, 0, len(records))
	for _, record := range records {
		ref := conversation.Ref{Provider: record.Provider, ConversationID: record.ConversationID}
		hasBTWSessions, err := s.store.HasBTWSessions(ref)
		if err != nil {
			return nil, err
		}
		summary := Summary{
			Provider:        record.Provider,
			ConversationID:  record.ConversationID,
			WorkspacePath:   record.WorkspacePath,
			Template:        record.Template,
			AgentBackend:    record.AgentBackend,
			ActiveSessionID: record.ActiveSessionID,
			BTWSessionID:    record.BTWSessionID,
			ReplyMode:       workspace.DefaultSettings().Settings.ReplyMode,
			SkillIDs:        []string{},
			SubagentIDs:     []string{},
			LastMessageAt:   record.LastMessageAt,
			UpdatedAt:       record.UpdatedAt,
		}
		if hasBTWSessions && strings.TrimSpace(summary.BTWSessionID) == "" {
			summary.BTWSessionID = "<per-user>"
		}
		if record.WorkspacePath != "" {
			settings, err := workspace.LoadSettings(record.WorkspacePath)
			if err == nil {
				summary.Template = settings.Template
				summary.AgentBackend = settings.Agent.Backend
				summary.ReplyMode = settings.Settings.ReplyMode
				summary.SkillIDs = append([]string(nil), settings.Mounts.SkillIDs...)
				summary.SubagentIDs = append([]string(nil), settings.Mounts.SubagentIDs...)
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		}
		result = append(result, summary)
	}
	return result, nil
}

func (s *Service) GroupMessagePolicy(ref conversation.Ref) (GroupMessagePolicy, error) {
	settings, ok, err := s.loadStoredSettings(ref)
	if err != nil {
		return GroupMessagePolicy{}, err
	}
	if !ok {
		return GroupMessagePolicy{}, nil
	}
	return GroupMessagePolicy{
		AcceptHumanMessagesWithoutMention: settings.Settings.AcceptGroupHumanMessagesWithoutMention,
	}, nil
}

func (s *Service) AcceptOtherBotMessages(ref conversation.Ref) (bool, error) {
	settings, ok, err := s.loadStoredSettings(ref)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return settings.Settings.AcceptOtherBotMessages, nil
}

func (s *Service) ReplyMode(ref conversation.Ref) (string, error) {
	settings, ok, err := s.loadStoredSettings(ref)
	if err != nil {
		return "", err
	}
	if !ok {
		return workspace.DefaultSettings().Settings.ReplyMode, nil
	}
	return settings.Settings.ReplyMode, nil
}

func (s *Service) AcceptInteractiveCardMessages(ref conversation.Ref) (bool, error) {
	settings, ok, err := s.loadStoredSettings(ref)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return settings.Settings.AcceptInteractiveCardMessages, nil
}

func (s *Service) RemoteEnabled(ref conversation.Ref) (bool, error) {
	settings, ok, err := s.loadStoredSettings(ref)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return settings.Settings.RemoteEnabled, nil
}

func (s *Service) Bind(ref conversation.Ref, backendName, sessionID string, now time.Time) error {
	return s.BindSlot(ref, SessionSlotMain, backendName, sessionID, now)
}

func (s *Service) BindSlot(ref conversation.Ref, slot SessionSlot, backendName, sessionID string, now time.Time) error {
	if slot == SessionSlotBTW {
		return s.BindBTW(ref, "", backendName, sessionID, now)
	}
	record, err := s.store.Get(ref)
	if err != nil {
		return err
	}
	if record == nil {
		return workspace.ErrWorkspaceNotFound
	}

	record.AgentBackend = backendName
	record.ActiveSessionID = sessionID
	record.LastMessageAt = now.UTC()
	record.UpdatedAt = now.UTC()
	return s.store.Upsert(*record)
}

func (s *Service) BindBTW(ref conversation.Ref, senderID, backendName, sessionID string, now time.Time) error {
	record, err := s.store.Get(ref)
	if err != nil {
		return err
	}
	if record == nil {
		return workspace.ErrWorkspaceNotFound
	}

	normalizedSenderID := normalizeBTWSenderID(senderID)
	now = now.UTC()
	if normalizedSenderID != "" {
		if err := s.store.DeleteBTWSession(ref, ""); err != nil {
			return err
		}
	}
	if err := s.store.UpsertBTWSession(ref, normalizedSenderID, sessionID, now); err != nil {
		return err
	}
	record.AgentBackend = backendName
	if normalizedSenderID == "" {
		record.BTWSessionID = sessionID
	} else {
		record.BTWSessionID = ""
	}
	record.UpdatedAt = now
	return s.store.Upsert(*record)
}

func (s *Service) ClearActive(ref conversation.Ref) error {
	return s.ClearSlot(ref, SessionSlotMain)
}

func (s *Service) ClearSlot(ref conversation.Ref, slot SessionSlot) error {
	if slot == SessionSlotBTW {
		return s.ClearBTW(ref, "")
	}
	record, err := s.store.Get(ref)
	if err != nil {
		return err
	}
	if record == nil {
		return nil
	}

	record.ActiveSessionID = ""
	record.UpdatedAt = time.Now().UTC()
	return s.store.Upsert(*record)
}

func (s *Service) ClearBTW(ref conversation.Ref, senderID string) error {
	record, err := s.store.Get(ref)
	if err != nil {
		return err
	}
	if record == nil {
		return nil
	}

	if err := s.store.DeleteBTWSession(ref, normalizeBTWSenderID(senderID)); err != nil {
		return err
	}
	if normalizeBTWSenderID(senderID) != "" {
		if err := s.store.DeleteBTWSession(ref, ""); err != nil {
			return err
		}
	}
	record.BTWSessionID = ""
	record.UpdatedAt = time.Now().UTC()
	return s.store.Upsert(*record)
}

func (s *Service) PrepareTopic(ref conversation.Ref, topicKey string, now time.Time) (*PreparedSession, error) {
	topicKey = strings.TrimSpace(topicKey)
	if topicKey == "" {
		return s.PrepareSlot(ref, SessionSlotMain, now)
	}
	ws, err := s.workspaces.Ensure(ref)
	if err != nil {
		return nil, err
	}

	activeSessionID, err := s.store.GetTopicSession(ref, topicKey)
	if err != nil {
		return nil, err
	}
	activeSessionID = strings.TrimSpace(activeSessionID)
	needNewSession := activeSessionID == ""

	return &PreparedSession{
		Workspace:       ws,
		AgentBackend:    ws.Settings.Agent.Backend,
		ActiveSessionID: activeSessionID,
		NeedNewSession:  needNewSession,
	}, nil
}

func (s *Service) BindTopic(ref conversation.Ref, topicKey, backendName, sessionID string, now time.Time) error {
	topicKey = strings.TrimSpace(topicKey)
	if topicKey == "" {
		return s.BindSlot(ref, SessionSlotMain, backendName, sessionID, now)
	}
	return s.store.UpsertTopicSession(ref, topicKey, sessionID, now.UTC())
}

func (s *Service) ClearTopic(ref conversation.Ref, topicKey string) error {
	topicKey = strings.TrimSpace(topicKey)
	if topicKey == "" {
		return s.ClearSlot(ref, SessionSlotMain)
	}
	return s.store.DeleteTopicSession(ref, topicKey)
}

// HasTopicSessions reports whether the conversation has any per-topic session,
// used by control-plane commands to phrase their reset replies in
// topic-session reply mode.
func (s *Service) HasTopicSessions(ref conversation.Ref) (bool, error) {
	return s.store.HasTopicSessions(ref)
}

// ClearAllTopics drops every per-topic session of the conversation. It backs the
// top-level /clear and /new full-conversation reset in topic-session mode.
func (s *Service) ClearAllTopics(ref conversation.Ref) error {
	return s.store.DeleteTopicSessions(ref)
}

func (s *Service) TopicSessionID(ref conversation.Ref, topicKey string) (string, error) {
	topicKey = strings.TrimSpace(topicKey)
	if topicKey == "" {
		return "", nil
	}
	sessionID, err := s.store.GetTopicSession(ref, topicKey)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sessionID), nil
}

func (s *Service) ListTopicSessions(ref conversation.Ref, limit int) ([]appstore.TopicSessionRecord, error) {
	return s.store.ListTopicSessions(ref, limit)
}

func (s *Service) Delete(ref conversation.Ref) error {
	if s.workspaces != nil {
		if err := s.workspaces.Delete(ref); err != nil {
			return err
		}
	}
	if cleanup, ok := s.store.(interface{ DeleteConversationState(conversation.Ref) error }); ok {
		if err := cleanup.DeleteConversationState(ref); err != nil {
			return err
		}
	} else {
		if err := s.store.DeleteBTWSessions(ref); err != nil {
			return err
		}
		if err := s.store.DeleteTopicSessions(ref); err != nil {
			return err
		}
		if err := s.store.Delete(ref); err != nil {
			return err
		}
		if tokens, ok := s.store.(interface{ DeleteSessionToken(conversation.Ref) error }); ok {
			if err := tokens.DeleteSessionToken(ref); err != nil {
				return err
			}
		}
	}
	if s.workspaces != nil {
		if err := s.workspaces.SyncLegacyBotPoller(ref); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) RebuildWorkspace(ref conversation.Ref) (*workspace.RuntimeWorkspace, error) {
	return s.workspaces.Rebuild(ref)
}

func (s *Service) SyncLegacyBotPoller(ref conversation.Ref) error {
	return s.workspaces.SyncLegacyBotPoller(ref)
}

func (s *Service) SyncLegacyBotPollerState() error {
	return s.workspaces.SyncLegacyBotPoller(conversation.Ref{})
}

// EnabledOtherBotMessageRefs lists conversations that opted in to replaying
// other bots' messages, used by the in-process poller.
func (s *Service) EnabledOtherBotMessageRefs() ([]conversation.Ref, error) {
	return s.workspaces.EnabledOtherBotMessageRefs()
}

func (s *Service) ListTemplates() ([]string, error) {
	return s.workspaces.ListTemplates()
}

func (s *Service) SwitchTemplate(ref conversation.Ref, templateName string) (*workspace.RuntimeWorkspace, error) {
	return s.workspaces.SwitchTemplate(ref, templateName)
}

func (s *Service) ListTemplateSummaries() ([]workspace.TemplateSummary, error) {
	return s.workspaces.ListTemplateSummaries()
}

func (s *Service) GetTemplate(templateName string) (*workspace.TemplateDetail, error) {
	return s.workspaces.GetTemplate(templateName)
}

func (s *Service) CreateTemplate(templateName, copyFrom string) (*workspace.TemplateDetail, error) {
	return s.workspaces.CreateTemplate(templateName, copyFrom)
}

func (s *Service) UpdateTemplate(templateName string, settings workspace.Settings, agentsContent string) (*workspace.TemplateDetail, error) {
	return s.workspaces.UpdateTemplate(templateName, settings, agentsContent)
}

func (s *Service) DeleteTemplate(templateName string) error {
	return s.workspaces.DeleteTemplate(templateName)
}

func (s *Service) loadStoredSettings(ref conversation.Ref) (workspace.Settings, bool, error) {
	record, err := s.store.Get(ref)
	if err != nil {
		return workspace.Settings{}, false, err
	}
	if record == nil || record.WorkspacePath == "" {
		return workspace.Settings{}, false, nil
	}

	settings, err := workspace.LoadSettings(record.WorkspacePath)
	if err != nil {
		if os.IsNotExist(err) {
			return workspace.Settings{}, false, nil
		}
		return workspace.Settings{}, false, err
	}
	return settings, true, nil
}

func (s *Service) resolveBTWSessionID(ref conversation.Ref, senderID string, record *appstore.WorkspaceRecord) (string, error) {
	if sessionID, err := s.store.GetBTWSession(ref, senderID); err != nil {
		return "", err
	} else if strings.TrimSpace(sessionID) != "" {
		return sessionID, nil
	}
	if strings.TrimSpace(senderID) != "" {
		return "", nil
	}
	if record == nil {
		return "", nil
	}
	return strings.TrimSpace(record.BTWSessionID), nil
}

func normalizeBTWSenderID(senderID string) string {
	return strings.TrimSpace(senderID)
}
