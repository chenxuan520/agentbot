package workspace

import (
	"errors"
	"os"
	"path/filepath"
)

func SyncAgentsFile(templateDir, workspacePath string, settings Settings, customContent []byte) error {
	templateAgentsPath := filepath.Join(templateDir, AgentsFileName)
	if _, err := os.Stat(templateAgentsPath); err != nil {
		return err
	}

	workspaceAgentsPath := filepath.Join(workspacePath, AgentsFileName)
	if err := os.RemoveAll(workspaceAgentsPath); err != nil {
		return err
	}

	if IsCustomAgentsMode(settings.Agent.AgentsMode) {
		content := customContent
		if content == nil {
			data, err := os.ReadFile(templateAgentsPath)
			if err != nil {
				return err
			}
			content = data
		}
		return os.WriteFile(workspaceAgentsPath, content, 0o644)
	}

	target, err := filepath.Rel(workspacePath, templateAgentsPath)
	if err != nil {
		target = templateAgentsPath
	}
	return os.Symlink(target, workspaceAgentsPath)
}

func readCustomAgentsSnapshot(workspacePath string) ([]byte, bool, error) {
	agentsPath := filepath.Join(workspacePath, AgentsFileName)
	info, err := os.Lstat(agentsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, nil
	}
	data, err := os.ReadFile(agentsPath)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}
