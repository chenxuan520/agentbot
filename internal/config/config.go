package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	RootDir         string
	TemplateRootDir string
	ChatRootDir     string
	SkillRootDir    string
	SubagentRootDir string
	RepoRootDir     string
	DBPath          string

	DefaultProvider string
	DefaultBackend  string

	SchedulerIntervalSeconds int
	SchedulerBatchLimit      int

	ServerHost string
	ServerPort int

	BackendBaseURLs map[string]string

	OpencodeConfig map[string]any
	ProjectToken   string
	AuthSecret     string
	WebBaseURL     string

	FeishuAppID             string
	FeishuAppSecret         string
	FeishuAckEmoji          string
	FeishuRemoteAckEmoji    string
	FeishuFailureWebhookURL string
}

type fileConfig struct {
	Runtime   fileRuntime             `json:"runtime"`
	Server    fileServer              `json:"server"`
	Auth      fileAuth                `json:"auth"`
	Web       fileWeb                 `json:"web"`
	Providers map[string]fileProvider `json:"providers"`
	Backends  map[string]fileBackend  `json:"backends"`
}

type fileRuntime struct {
	DefaultProvider          string `json:"defaultProvider"`
	DefaultBackend           string `json:"defaultBackend"`
	SchedulerIntervalSeconds int    `json:"schedulerIntervalSeconds"`
	SchedulerBatchLimit      int    `json:"schedulerBatchLimit"`
}

type fileServer struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type fileAuth struct {
	ProjectToken string `json:"projectToken"`
	Secret       string `json:"secret"`
}

type fileWeb struct {
	BaseURL string `json:"baseURL"`
}

type fileProvider struct {
	AppID             string `json:"appID"`
	AppSecret         string `json:"appSecret"`
	AckEmoji          string `json:"ackEmoji"`
	RemoteAckEmoji    string `json:"remoteAckEmoji"`
	FailureWebhookURL string `json:"failureWebhookURL"`
}

type fileBackend struct {
	BaseURL string `json:"baseURL"`
}

func Load(root string) (Config, error) {
	cfg := Default(root)

	if err := mergeFileConfig(&cfg); err != nil {
		return Config{}, err
	}
	mergeEnvFallback(&cfg)
	applyDefaults(&cfg)
	return cfg, nil
}

func Default(root string) Config {
	cleanRoot := resolveRoot(root)
	return Config{
		RootDir:                  cleanRoot,
		TemplateRootDir:          filepath.Join(cleanRoot, "templates"),
		ChatRootDir:              filepath.Join(cleanRoot, "data", "chats"),
		SkillRootDir:             filepath.Join(cleanRoot, "agents", "skills"),
		SubagentRootDir:          filepath.Join(cleanRoot, "agents", "subagents"),
		RepoRootDir:              filepath.Join(cleanRoot, "agents", "repos"),
		DBPath:                   filepath.Join(cleanRoot, "data", "state.sqlite3"),
		DefaultProvider:          "feishu",
		DefaultBackend:           "opencode",
		SchedulerIntervalSeconds: 1,
		SchedulerBatchLimit:      50,
		ServerHost:               "127.0.0.1",
		ServerPort:               8080,
		BackendBaseURLs: map[string]string{
			"opencode": "http://localhost:4096",
		},
		FeishuAckEmoji: "OnIt",
	}
}

func resolveRoot(root string) string {
	if envRoot := os.Getenv("AGENT_BOT_ROOT"); envRoot != "" {
		root = envRoot
	}
	cleanRoot := filepath.Clean(root)
	if absRoot, err := filepath.Abs(cleanRoot); err == nil {
		cleanRoot = absRoot
	}
	return cleanRoot
}

func mergeFileConfig(cfg *Config) error {
	path := filepath.Join(cfg.RootDir, "agent-bot.json")
	if custom := os.Getenv("AGENT_BOT_CONFIG"); custom != "" {
		if filepath.IsAbs(custom) {
			path = custom
		} else {
			path = filepath.Join(cfg.RootDir, custom)
		}
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	var file fileConfig
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}

	if file.Runtime.DefaultProvider != "" {
		cfg.DefaultProvider = file.Runtime.DefaultProvider
	}
	if file.Runtime.DefaultBackend != "" {
		cfg.DefaultBackend = file.Runtime.DefaultBackend
	}
	if file.Runtime.SchedulerIntervalSeconds > 0 {
		cfg.SchedulerIntervalSeconds = file.Runtime.SchedulerIntervalSeconds
	}
	if file.Runtime.SchedulerBatchLimit > 0 {
		cfg.SchedulerBatchLimit = file.Runtime.SchedulerBatchLimit
	}

	if file.Server.Host != "" {
		cfg.ServerHost = file.Server.Host
	}
	if file.Server.Port > 0 {
		cfg.ServerPort = file.Server.Port
	}
	if file.Auth.ProjectToken != "" {
		cfg.ProjectToken = file.Auth.ProjectToken
	}
	if file.Auth.Secret != "" {
		cfg.AuthSecret = file.Auth.Secret
	}
	if value := strings.TrimSpace(file.Web.BaseURL); value != "" {
		cfg.WebBaseURL = strings.TrimRight(value, "/")
	}

	if providerCfg, ok := file.Providers["feishu"]; ok {
		if providerCfg.AppID != "" {
			cfg.FeishuAppID = providerCfg.AppID
		}
		if providerCfg.AppSecret != "" {
			cfg.FeishuAppSecret = providerCfg.AppSecret
		}
		if providerCfg.AckEmoji != "" {
			cfg.FeishuAckEmoji = providerCfg.AckEmoji
		}
		// Left empty on purpose when unset: the provider falls back to the ack
		// emoji, so the remote-agent forward reaction defaults to the same icon.
		if providerCfg.RemoteAckEmoji != "" {
			cfg.FeishuRemoteAckEmoji = providerCfg.RemoteAckEmoji
		}
		if value := strings.TrimSpace(providerCfg.FailureWebhookURL); value != "" {
			cfg.FeishuFailureWebhookURL = value
		}
	}

	if backendCfg, ok := file.Backends["opencode"]; ok {
		if backendCfg.BaseURL != "" {
			cfg.BackendBaseURLs["opencode"] = backendCfg.BaseURL
		}
	}

	return nil
}

func mergeEnvFallback(cfg *Config) {
	if cfg.BackendBaseURLs["opencode"] == "" {
		if value := os.Getenv("OPENCODE_BASE_URL"); value != "" {
			cfg.BackendBaseURLs["opencode"] = value
		}
	}
	if cfg.OpencodeConfig == nil {
		if value := os.Getenv("OPENCODE_CONFIG_CONTENT"); value != "" {
			var parsed map[string]any
			if json.Unmarshal([]byte(value), &parsed) == nil {
				cfg.OpencodeConfig = parsed
			}
		}
	}
	if cfg.FeishuAppID == "" {
		cfg.FeishuAppID = os.Getenv("APP_ID")
	}
	if cfg.FeishuAppSecret == "" {
		cfg.FeishuAppSecret = os.Getenv("APP_SECRET")
	}
	if cfg.FeishuAckEmoji == "" {
		cfg.FeishuAckEmoji = os.Getenv("PROCESSING_EMOJI_TYPE")
	}
	if cfg.ProjectToken == "" {
		cfg.ProjectToken = os.Getenv("AGENT_BOT_PROJECT_TOKEN")
	}
	if cfg.AuthSecret == "" {
		cfg.AuthSecret = os.Getenv("AGENT_BOT_AUTH_SECRET")
	}
	if cfg.WebBaseURL == "" {
		cfg.WebBaseURL = strings.TrimRight(strings.TrimSpace(os.Getenv("AGENT_BOT_WEB_BASE_URL")), "/")
	}
}

func applyDefaults(cfg *Config) {
	if cfg.BackendBaseURLs == nil {
		cfg.BackendBaseURLs = map[string]string{}
	}
	if cfg.BackendBaseURLs["opencode"] == "" {
		cfg.BackendBaseURLs["opencode"] = "http://localhost:4096"
	}
	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = "feishu"
	}
	if cfg.DefaultBackend == "" {
		cfg.DefaultBackend = "opencode"
	}
	if cfg.SchedulerIntervalSeconds <= 0 {
		cfg.SchedulerIntervalSeconds = 1
	}
	if cfg.SchedulerBatchLimit <= 0 {
		cfg.SchedulerBatchLimit = 50
	}
	if cfg.ServerHost == "" {
		cfg.ServerHost = "127.0.0.1"
	}
	if cfg.ServerPort <= 0 {
		cfg.ServerPort = 8080
	}
	if cfg.FeishuAckEmoji == "" {
		cfg.FeishuAckEmoji = "OnIt"
	}
}

func (c Config) LocalAPIBaseURL() string {
	host := c.ServerHost
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, c.ServerPort)
}
