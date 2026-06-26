package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/conversation"
)

type PromptFileStore struct {
	rootDir string
}

func NewPromptFileStore(cfg config.Config) *PromptFileStore {
	return &PromptFileStore{rootDir: cfg.RootDir}
}

func (s *PromptFileStore) MaterializeRecurringPayload(ref conversation.Ref, route string, payload map[string]any) (map[string]any, error) {
	if payload == nil {
		return nil, nil
	}
	result := clonePayloadMap(payload)
	promptText, _ := result["promptText"].(string)
	if strings.TrimSpace(promptText) == "" {
		promptFile, _ := result["promptFile"].(string)
		if strings.TrimSpace(promptFile) == "" {
			return result, nil
		}
		content, err := s.ReadPrompt(promptFile)
		if err != nil {
			return nil, err
		}
		promptText = content
	}
	if strings.TrimSpace(promptText) == "" {
		return result, nil
	}

	relPath, err := s.WritePrompt(ref, route, promptText)
	if err != nil {
		return nil, err
	}

	delete(result, "promptText")
	result["promptFile"] = relPath
	return result, nil
}

func (s *PromptFileStore) WritePrompt(ref conversation.Ref, route, promptText string) (string, error) {
	dir := filepath.Join(
		s.rootDir,
		"data",
		"scheduler",
		"prompts",
		sanitizePathPart(ref.Provider),
		sanitizePathPart(ref.ConversationID),
	)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	prefix := sanitizePathPart(route)
	if prefix == "" {
		prefix = "prompt"
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.md", prefix, uuid.NewString()))
	if err := os.WriteFile(path, []byte(promptText), 0o644); err != nil {
		return "", err
	}

	relPath, err := filepath.Rel(s.rootDir, path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(relPath), nil
}

func (s *PromptFileStore) ReadPrompt(promptFile string) (string, error) {
	if strings.TrimSpace(promptFile) == "" {
		return "", nil
	}
	resolved, err := s.resolvePromptPath(promptFile)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *PromptFileStore) WritePromptContent(promptFile, promptText string) error {
	if strings.TrimSpace(promptFile) == "" {
		return fmt.Errorf("prompt file is required")
	}
	resolved, err := s.resolvePromptPath(promptFile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return err
	}
	return os.WriteFile(resolved, []byte(promptText), 0o644)
}

func (s *PromptFileStore) resolvePromptPath(promptFile string) (string, error) {
	promptFile = strings.TrimSpace(promptFile)
	if promptFile == "" {
		return "", nil
	}
	if filepath.IsAbs(promptFile) {
		return "", fmt.Errorf("prompt file must be relative: %s", promptFile)
	}

	resolved := filepath.Clean(filepath.Join(s.rootDir, filepath.FromSlash(promptFile)))
	if !isWithinRoot(s.rootDir, resolved) {
		return "", fmt.Errorf("prompt file escapes root: %s", promptFile)
	}
	return resolved, nil
}

func clonePayloadMap(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	result := make(map[string]any, len(payload))
	for key, value := range payload {
		result[key] = value
	}
	return result
}

func sanitizePathPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "_"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	value = replacer.Replace(value)
	if value == "" {
		return "_"
	}
	return value
}

func isWithinRoot(rootDir, targetPath string) bool {
	root := filepath.Clean(rootDir)
	target := filepath.Clean(targetPath)
	if target == root {
		return true
	}
	return strings.HasPrefix(target, root+string(os.PathSeparator))
}
