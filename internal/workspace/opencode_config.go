package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/chenxuan520/agentbot/internal/config"
)

func writeBackendConfig(cfg config.Config, workspacePath string, settings Settings) error {
	if settings.Agent.Backend != "opencode" {
		return removeOpencodeConfigFiles(workspacePath)
	}
	if err := removeLegacyOpencodeConfig(workspacePath); err != nil {
		return err
	}

	configBody := map[string]any{
		"$schema": "https://opencode.ai/config.json",
	}
	if cfg.OpencodeConfig != nil {
		configBody = mergeJSON(configBody, cfg.OpencodeConfig)
	}
	if settings.Agent.OpencodeConfig != nil {
		configBody = mergeJSON(configBody, settings.Agent.OpencodeConfig)
	}
	enforceQuestionDenied(configBody)

	data, err := json.MarshalIndent(configBody, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	path := filepath.Join(workspacePath, "opencode.json")
	return os.WriteFile(path, data, 0o644)
}

func removeLegacyOpencodeConfig(workspacePath string) error {
	path := filepath.Join(workspacePath, ".agents", "opencode.json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func removeOpencodeConfigFiles(workspacePath string) error {
	for _, path := range []string{
		filepath.Join(workspacePath, ".agents", "opencode.json"),
		filepath.Join(workspacePath, "opencode.json"),
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func asJSONObject(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return map[string]any{}
	}
	return object
}

func enforceQuestionDenied(configBody map[string]any) {
	configBody["permission"] = mergeJSON(asJSONObject(configBody["permission"]), map[string]any{
		"question": "deny",
	})

	agents := asJSONObject(configBody["agent"])
	if len(agents) == 0 {
		return
	}
	for name, value := range agents {
		agentConfig := asJSONObject(value)
		agentConfig["permission"] = mergeJSON(asJSONObject(agentConfig["permission"]), map[string]any{
			"question": "deny",
		})
		agents[name] = agentConfig
	}
	configBody["agent"] = agents
}

func mergeJSON(base, patch map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(patch))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range patch {
		existing, ok := result[key].(map[string]any)
		next, okNext := value.(map[string]any)
		if ok && okNext {
			result[key] = mergeJSON(existing, next)
			continue
		}
		result[key] = value
	}
	return result
}
