package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const SettingsFileName = ".session-setting.json"

const AgentsFileName = "AGENTS.md"

const (
	AgentsModeTemplate = "template"
	AgentsModeCustom   = "custom"
)

const (
	ReplyModeDirect = "direct"
	ReplyModeThread = "thread"
	ReplyModeTopic  = "topic"
	// ReplyModeTopicSession behaves like topic (reply in thread) but also gives
	// each topic its own backend session instead of one shared conversation session.
	ReplyModeTopicSession = "topic-session"
)

type Settings struct {
	Version  int           `json:"version"`
	Template string        `json:"template"`
	Agent    AgentConfig   `json:"agent"`
	Settings RuntimeConfig `json:"settings"`
	Mounts   MountConfig   `json:"mounts"`
}

type AgentConfig struct {
	Backend                    string         `json:"backend"`
	AgentsMode                 string         `json:"agentsMode,omitempty"`
	OpencodeConfig             map[string]any `json:"opencodeConfig,omitempty"`
	OpencodeHTTPTimeoutSeconds int            `json:"opencodeHTTPTimeoutSeconds,omitempty"`
}

type RuntimeConfig struct {
	ReplyMode                              string `json:"replyMode"`
	HistoryTTLHours                        int    `json:"historyTTLHours"`
	AcceptGroupHumanMessagesWithoutMention bool   `json:"acceptGroupHumanMessagesWithoutMention"`
    // AcceptOtherBotMessages controls whether messages sent by other bots
    // (provider senderType "app", excluding ourselves) are polled and replayed
    // into the processing chain.
    AcceptOtherBotMessages        bool `json:"acceptOtherBotMessages"`
    AcceptInteractiveCardMessages bool `json:"acceptInteractiveCardMessages"`
	// RemoteEnabled opts this conversation in to the remote-agent route: when a
	// local agent plugin connects with this conversation's session token, inbound
	// messages are relayed to it instead of the default backend. It is the safety
	// gate for the feature; while false, plugin connections are refused and the
	// conversation always talks to the default backend.
	RemoteEnabled bool `json:"remoteEnabled"`
}

type MountConfig struct {
	SkillIDs    []string `json:"skillIds"`
	SubagentIDs []string `json:"subagentIds"`
	RepoIDs     []string `json:"repoIds"`
}

func DefaultSettings() Settings {
	return Settings{
		Version:  1,
		Template: "default",
		Agent: AgentConfig{
			Backend:    "opencode",
			AgentsMode: AgentsModeTemplate,
		},
		Settings: RuntimeConfig{
			ReplyMode:                              "direct",
			HistoryTTLHours:                        24,
			AcceptGroupHumanMessagesWithoutMention: false,
			AcceptOtherBotMessages:                 false,
			AcceptInteractiveCardMessages:          false,
		},
		Mounts: MountConfig{SkillIDs: []string{}, SubagentIDs: []string{}, RepoIDs: []string{}},
	}
}

func LoadSettings(workspaceDir string) (Settings, error) {
	path := filepath.Join(workspaceDir, SettingsFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, err
	}

	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return Settings{}, err
	}
	applyDefaults(&settings)
	return settings, nil
}

func SaveSettings(workspaceDir string, settings Settings) error {
	applyDefaults(&settings)

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	path := filepath.Join(workspaceDir, SettingsFileName)
	return os.WriteFile(path, data, 0o644)
}

func applyDefaults(settings *Settings) {
	defaults := DefaultSettings()

	if settings.Version == 0 {
		settings.Version = defaults.Version
	}
	if settings.Template == "" {
		settings.Template = defaults.Template
	}
	if settings.Agent.Backend == "" {
		settings.Agent.Backend = defaults.Agent.Backend
	}
	settings.Agent.AgentsMode = NormalizeAgentsMode(settings.Agent.AgentsMode)
	if settings.Agent.OpencodeHTTPTimeoutSeconds < 0 {
		settings.Agent.OpencodeHTTPTimeoutSeconds = 0
	}
	if settings.Settings.ReplyMode == "" {
		settings.Settings.ReplyMode = defaults.Settings.ReplyMode
	}
	if settings.Settings.HistoryTTLHours <= 0 {
		settings.Settings.HistoryTTLHours = defaults.Settings.HistoryTTLHours
	}
	if settings.Mounts.SkillIDs == nil {
		settings.Mounts.SkillIDs = defaults.Mounts.SkillIDs
	}
	if settings.Mounts.SubagentIDs == nil {
		settings.Mounts.SubagentIDs = defaults.Mounts.SubagentIDs
	}
	if settings.Mounts.RepoIDs == nil {
		settings.Mounts.RepoIDs = defaults.Mounts.RepoIDs
	}
}

func NormalizeAgentsMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AgentsModeCustom:
		return AgentsModeCustom
	default:
		return AgentsModeTemplate
	}
}

func IsCustomAgentsMode(value string) bool {
	return NormalizeAgentsMode(value) == AgentsModeCustom
}

func IsTopicSessionReplyMode(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), ReplyModeTopicSession)
}
