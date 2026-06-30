package scheduler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	appstore "github.com/chenxuan520/agentbot/internal/store"
)

const TriggerLogFile = "triggered-jobs.jsonl"

type FileHandler struct {
	workspaces appstore.WorkspaceStore
}

func NewFileHandler(workspaces appstore.WorkspaceStore) *FileHandler {
	return &FileHandler{workspaces: workspaces}
}

func (h *FileHandler) Handle(job Job, triggeredAt time.Time) error {
	record, err := h.workspaces.Get(job.Ref())
	if err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("workspace not found for scheduled job %s", job.ID)
	}

	runtimeDir := filepath.Join(record.WorkspacePath, ".agents", "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
	}

	entry := map[string]any{
		"jobID":          job.ID,
		"provider":       job.Provider,
		"conversationID": job.ConversationID,
		"route":          job.Route,
		"payload":        json.RawMessage(job.Payload),
		"runAt":          job.RunAt.UTC().Format(time.RFC3339),
		"triggeredAt":    triggeredAt.UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	path := filepath.Join(runtimeDir, TriggerLogFile)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.Write(append(data, '\n'))
	return err
}
