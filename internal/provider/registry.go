package provider

import (
	"fmt"

	"github.com/chenxuan520/agentbot/internal/config"
	feishuprovider "github.com/chenxuan520/agentbot/internal/provider/feishu"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
)

func FromConfig(cfg config.Config, name string) (providerapi.Client, error) {
	switch name {
	case "feishu":
		return feishuprovider.New(cfg.FeishuAppID, cfg.FeishuAppSecret, cfg.FeishuAckEmoji, cfg.FeishuRemoteAckEmoji), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", name)
	}
}
