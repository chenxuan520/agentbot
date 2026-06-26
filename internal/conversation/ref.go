package conversation

import (
	"path/filepath"
	"strings"
)

type Ref struct {
	Provider       string
	ConversationID string
}

func (r Ref) WorkspacePath(root string) string {
	return filepath.Join(root, sanitizeSegment(r.Provider), sanitizeSegment(r.ConversationID))
}

func sanitizeSegment(value string) string {
	cleaned := strings.TrimSpace(value)
	if cleaned == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(cleaned)
}
