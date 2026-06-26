package backendfactory

import (
	"fmt"
	"time"

	"github.com/chenxuan520/agentbot/internal/backend"
	opencodebackend "github.com/chenxuan520/agentbot/internal/backend/opencode"
	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

func FromConfig(cfg config.Config, name string) (backend.Client, error) {
	return fromTimeoutSeconds(cfg, name, 0)
}

func FromSettings(cfg config.Config, settings workspace.Settings) (backend.Client, error) {
	return fromTimeoutSeconds(cfg, settings.Agent.Backend, settings.Agent.OpencodeHTTPTimeoutSeconds)
}

func fromTimeoutSeconds(cfg config.Config, name string, timeoutSeconds int) (backend.Client, error) {
	switch name {
	case "opencode":
		baseURL := cfg.BackendBaseURLs[name]
		if baseURL == "" {
			return nil, fmt.Errorf("missing base url for backend %q", name)
		}
		options := opencodebackend.Options{}
		if timeoutSeconds > 0 {
			options.HTTPTimeout = time.Duration(timeoutSeconds) * time.Second
		}
		return opencodebackend.NewWithOptions(baseURL, options), nil
	default:
		return nil, fmt.Errorf("unsupported backend %q", name)
	}
}
