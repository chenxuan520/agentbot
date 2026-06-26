package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/conversation"
)

func writeRuntimeContext(cfg config.Config, workspacePath string, ref conversation.Ref) error {
	runtimeDir := filepath.Join(workspacePath, ".agents", "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
	}

	baseURL := cfg.LocalAPIBaseURL()
	envContent := "export AGENT_BOT_API_BASE_URL=" + shellQuote(baseURL) + "\n" +
		"export AGENT_BOT_PROVIDER=" + shellQuote(ref.Provider) + "\n" +
		"export AGENT_BOT_CONVERSATION_ID=" + shellQuote(ref.ConversationID) + "\n"
	if err := os.WriteFile(filepath.Join(runtimeDir, "context.env"), []byte(envContent), 0o644); err != nil {
		return err
	}

	data, err := json.MarshalIndent(map[string]any{
		"baseURL":        baseURL,
		"provider":       ref.Provider,
		"conversationId": ref.ConversationID,
	}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(runtimeDir, "localapi.json"), data, 0o644)
}
