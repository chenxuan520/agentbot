package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrTemplateNotFound = errors.New("template not found")
var ErrTemplateAlreadyExists = errors.New("template already exists")
var ErrTemplateInUse = errors.New("template is in use")
var ErrTemplateProtected = errors.New("template is protected")

type TemplateSummary struct {
	Name         string    `json:"id"`
	Path         string    `json:"path"`
	SessionCount int       `json:"sessionCount"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type TemplateDetail struct {
	Name          string    `json:"id"`
	Path          string    `json:"path"`
	Settings      Settings  `json:"settings"`
	AgentsPath    string    `json:"agentsPath"`
	AgentsContent string    `json:"agentsContent"`
	SessionCount  int       `json:"sessionCount"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (m *Manager) ListTemplateSummaries() ([]TemplateSummary, error) {
	names, err := m.ListTemplates()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []TemplateSummary{}, nil
		}
		return nil, err
	}

	usage, err := m.templateUsageCounts()
	if err != nil {
		return nil, err
	}

	items := make([]TemplateSummary, 0, len(names))
	for _, name := range names {
		templateDir := filepath.Join(m.cfg.TemplateRootDir, name)
		updatedAt, err := templateUpdatedAt(templateDir)
		if err != nil {
			return nil, err
		}
		items = append(items, TemplateSummary{
			Name:         name,
			Path:         templateDir,
			SessionCount: usage[name],
			UpdatedAt:    updatedAt,
		})
	}
	return items, nil
}

func (m *Manager) GetTemplate(name string) (*TemplateDetail, error) {
	templateDir, name, err := m.resolveTemplateDir(name)
	if err != nil {
		return nil, err
	}

	settings, err := LoadSettings(templateDir)
	if err != nil {
		return nil, err
	}
	settings.Template = name

	agentsPath := filepath.Join(templateDir, "AGENTS.md")
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		return nil, err
	}

	usage, err := m.templateUsageCounts()
	if err != nil {
		return nil, err
	}
	updatedAt, err := templateUpdatedAt(templateDir)
	if err != nil {
		return nil, err
	}

	return &TemplateDetail{
		Name:          name,
		Path:          templateDir,
		Settings:      settings,
		AgentsPath:    agentsPath,
		AgentsContent: string(content),
		SessionCount:  usage[name],
		UpdatedAt:     updatedAt,
	}, nil
}

func (m *Manager) CreateTemplate(name, copyFrom string) (*TemplateDetail, error) {
	name, err := normalizeTemplateName(name)
	if err != nil {
		return nil, err
	}
	copyFrom, err = normalizeTemplateName(defaultValue(strings.TrimSpace(copyFrom), DefaultSettings().Template))
	if err != nil {
		return nil, err
	}

	sourceDir, _, err := m.resolveTemplateDir(copyFrom)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(m.cfg.TemplateRootDir, 0o755); err != nil {
		return nil, err
	}

	targetDir := filepath.Join(m.cfg.TemplateRootDir, name)
	if _, err := os.Stat(targetDir); err == nil {
		return nil, fmt.Errorf("template %q already exists: %w", name, ErrTemplateAlreadyExists)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	if err := copyDir(sourceDir, targetDir); err != nil {
		return nil, err
	}

	settings, err := LoadSettings(targetDir)
	if err != nil {
		return nil, err
	}
	settings.Template = name
	if err := SaveSettings(targetDir, settings); err != nil {
		return nil, err
	}

	return m.GetTemplate(name)
}

func (m *Manager) UpdateTemplate(name string, settings Settings, agentsContent string) (*TemplateDetail, error) {
	templateDir, name, err := m.resolveTemplateDir(name)
	if err != nil {
		return nil, err
	}

	settings.Template = name
	if err := SaveSettings(templateDir, settings); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(templateDir, "AGENTS.md"), []byte(agentsContent), 0o644); err != nil {
		return nil, err
	}

	return m.GetTemplate(name)
}

func (m *Manager) DeleteTemplate(name string) error {
	templateDir, name, err := m.resolveTemplateDir(name)
	if err != nil {
		return err
	}
	if name == DefaultSettings().Template {
		return fmt.Errorf("template %q cannot be deleted: %w", name, ErrTemplateProtected)
	}

	usage, err := m.templateUsageCounts()
	if err != nil {
		return err
	}
	if usage[name] > 0 {
		return fmt.Errorf("template %q is still used by %d session(s): %w", name, usage[name], ErrTemplateInUse)
	}
	return os.RemoveAll(templateDir)
}

func (m *Manager) templateUsageCounts() (map[string]int, error) {
	records, err := m.store.List()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, record := range records {
		name := strings.TrimSpace(record.Template)
		if name == "" {
			continue
		}
		counts[name]++
	}
	return counts, nil
}

func (m *Manager) resolveTemplateDir(name string) (string, string, error) {
	name, err := normalizeTemplateName(name)
	if err != nil {
		return "", "", err
	}
	templateDir := filepath.Join(m.cfg.TemplateRootDir, name)
	if _, err := os.Stat(templateDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("template %q not found: %w", name, ErrTemplateNotFound)
		}
		return "", "", err
	}
	return templateDir, name, nil
}

func normalizeTemplateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("template name is required")
	}
	if !isSafeTemplateName(name) {
		return "", fmt.Errorf("invalid template name %q", name)
	}
	return name, nil
}

func isSafeTemplateName(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

func templateUpdatedAt(templateDir string) (time.Time, error) {
	paths := []string{
		filepath.Join(templateDir, SettingsFileName),
		filepath.Join(templateDir, "AGENTS.md"),
	}
	updatedAt := time.Time{}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return time.Time{}, err
		}
		if info.ModTime().After(updatedAt) {
			updatedAt = info.ModTime().UTC()
		}
	}
	return updatedAt, nil
}

func defaultValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
