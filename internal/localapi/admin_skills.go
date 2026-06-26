package localapi

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/chenxuan520/agentbot/internal/accesstoken"
)

var ErrSkillNotFound = errors.New("skill not found")
var ErrSkillInUse = errors.New("skill is in use")
var ErrSkillAlreadyExists = errors.New("skill already exists")

func (s *Server) handleAdminSkillCreate(c *gin.Context) {
	if !requireProjectAdminScope(c) {
		return
	}
	var body struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	if !bindJSON(c, &body) {
		return
	}
	detail, err := createSkill(s.cfg.SkillRootDir, body.ID, body.Content)
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (s *Server) handleAdminSkillGet(c *gin.Context) {
	scope, ok := adminScopeFromContext(c)
	if !ok {
		writeStatusError(c, http.StatusUnauthorized, fmt.Errorf("missing auth scope"))
		return
	}
	detail, err := getSkillDetail(s.cfg.SkillRootDir, strings.TrimSpace(c.Param("skillId")), scope.Kind != accesstoken.ScopeProject)
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (s *Server) handleAdminSkillFilesList(c *gin.Context) {
	items, err := listSkillFiles(s.cfg.SkillRootDir, strings.TrimSpace(c.Param("skillId")))
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) handleAdminSkillFileGet(c *gin.Context) {
	scope, ok := adminScopeFromContext(c)
	if !ok {
		writeStatusError(c, http.StatusUnauthorized, fmt.Errorf("missing auth scope"))
		return
	}
	content, err := readSkillFile(s.cfg.SkillRootDir, strings.TrimSpace(c.Param("skillId")), strings.TrimSpace(c.Query("path")))
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"path":     strings.TrimSpace(c.Query("path")),
		"content":  content,
		"exists":   true,
		"readOnly": scope.Kind != accesstoken.ScopeProject,
	})
}

func (s *Server) handleAdminSkillFileUpdate(c *gin.Context) {
	if !requireProjectAdminScope(c) {
		return
	}
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !bindJSON(c, &body) {
		return
	}
	if err := writeSkillFile(s.cfg.SkillRootDir, strings.TrimSpace(c.Param("skillId")), body.Path, body.Content); err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleAdminSkillDelete(c *gin.Context) {
	if !requireProjectAdminScope(c) {
		return
	}
	if s.sessions == nil {
		writeError(c, fmt.Errorf("session service is not configured"))
		return
	}
	skillID := strings.TrimSpace(c.Param("skillId"))
	if err := s.ensureSkillNotInUse(skillID); err != nil {
		writeSkillError(c, err)
		return
	}
	if err := deleteSkill(s.cfg.SkillRootDir, skillID); err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) ensureSkillNotInUse(skillID string) error {
	sessionSummaries, err := s.sessions.List()
	if err != nil {
		return err
	}
	usedBySessions := make([]string, 0)
	for _, summary := range sessionSummaries {
		if containsString(summary.SkillIDs, skillID) {
			usedBySessions = append(usedBySessions, fmt.Sprintf("%s:%s", summary.Provider, summary.ConversationID))
		}
	}

	templateSummaries, err := s.sessions.ListTemplateSummaries()
	if err != nil {
		return err
	}
	usedByTemplates := make([]string, 0)
	for _, summary := range templateSummaries {
		detail, err := s.sessions.GetTemplate(summary.Name)
		if err != nil {
			return err
		}
		if containsString(detail.Settings.Mounts.SkillIDs, skillID) {
			usedByTemplates = append(usedByTemplates, detail.Name)
		}
	}

	if len(usedBySessions) == 0 && len(usedByTemplates) == 0 {
		return nil
	}
	sort.Strings(usedBySessions)
	sort.Strings(usedByTemplates)
	parts := make([]string, 0, 2)
	if len(usedBySessions) > 0 {
		parts = append(parts, fmt.Sprintf("sessions=[%s]", strings.Join(usedBySessions, ", ")))
	}
	if len(usedByTemplates) > 0 {
		parts = append(parts, fmt.Sprintf("templates=[%s]", strings.Join(usedByTemplates, ", ")))
	}
	return fmt.Errorf("skill %q is still referenced by %s: %w", skillID, strings.Join(parts, " "), ErrSkillInUse)
}

func getSkillDetail(skillRoot, skillID string, readOnly bool) (gin.H, error) {
	skillDir, skillID, err := resolveSkillDir(skillRoot, skillID)
	if err != nil {
		return nil, err
	}
	skillFilePath := filepath.Join(skillDir, "SKILL.md")
	content, err := os.ReadFile(skillFilePath)
	if err != nil {
		return nil, err
	}
	title := skillID
	if parsed := firstMarkdownHeading(string(content)); parsed != "" {
		title = parsed
	}
	info, err := os.Stat(skillFilePath)
	if err != nil {
		return nil, err
	}
	return gin.H{
		"id":           skillID,
		"title":        title,
		"path":         skillDir,
		"hasSkillFile": true,
		"readOnly":     readOnly,
		"updatedAt":    info.ModTime().UTC(),
	}, nil
}

func listSkillFiles(skillRoot, skillID string) ([]adminFileItem, error) {
	skillDir, _, err := resolveSkillDir(skillRoot, skillID)
	if err != nil {
		return nil, err
	}
	entries := []adminFileItem{}
	err = filepath.Walk(skillDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(skillDir, path)
		if err != nil {
			return err
		}
		entries = append(entries, adminFileItem{
			Path:      filepath.ToSlash(rel),
			Size:      info.Size(),
			UpdatedAt: info.ModTime().UTC(),
			Exists:    true,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	return entries, nil
}

func readSkillFile(skillRoot, skillID, relPath string) (string, error) {
	resolved, err := resolveSkillFile(skillRoot, skillID, relPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func writeSkillFile(skillRoot, skillID, relPath, content string) error {
	resolved, err := resolveSkillFile(skillRoot, skillID, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return err
	}
	return os.WriteFile(resolved, []byte(content), 0o644)
}

func deleteSkill(skillRoot, skillID string) error {
	skillDir, _, err := resolveSkillDir(skillRoot, skillID)
	if err != nil {
		return err
	}
	return os.RemoveAll(skillDir)
}

func createSkill(skillRoot, skillID, content string) (gin.H, error) {
	skillID = strings.TrimSpace(skillID)
	if !isSafeSkillID(skillID) {
		return nil, fmt.Errorf("invalid skill id %q", skillID)
	}
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		return nil, err
	}
	skillDir := filepath.Join(skillRoot, skillID)
	if _, err := os.Stat(skillDir); err == nil {
		return nil, fmt.Errorf("skill %q already exists: %w", skillID, ErrSkillAlreadyExists)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return nil, err
	}
	if strings.TrimSpace(content) == "" {
		content = defaultSkillContent(skillID)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(ensureTrailingNewline(content)), 0o644); err != nil {
		return nil, err
	}
	return getSkillDetail(skillRoot, skillID, false)
}

func resolveSkillDir(skillRoot, skillID string) (string, string, error) {
	skillID = strings.TrimSpace(skillID)
	if !isSafeSkillID(skillID) {
		return "", "", fmt.Errorf("invalid skill id %q", skillID)
	}
	skillDir := filepath.Join(skillRoot, skillID)
	info, err := os.Stat(skillDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("skill %q not found: %w", skillID, ErrSkillNotFound)
		}
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("skill %q not found: %w", skillID, ErrSkillNotFound)
	}
	return skillDir, skillID, nil
}

func resolveSkillFile(skillRoot, skillID, relPath string) (string, error) {
	skillDir, _, err := resolveSkillDir(skillRoot, skillID)
	if err != nil {
		return "", err
	}
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", fmt.Errorf("path is required")
	}
	cleaned := filepath.Clean(relPath)
	if filepath.IsAbs(cleaned) || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid relative path %q", relPath)
	}
	resolved := filepath.Join(skillDir, cleaned)
	rel, err := filepath.Rel(skillDir, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}
	return resolved, nil
}

func writeSkillError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrSkillNotFound):
		writeStatusError(c, http.StatusNotFound, err)
	case errors.Is(err, ErrSkillAlreadyExists), errors.Is(err, ErrSkillInUse):
		writeStatusError(c, http.StatusConflict, err)
	default:
		writeError(c, err)
	}
}

func defaultSkillContent(skillID string) string {
	return fmt.Sprintf(`---
name: %s
description: 描述这个 skill 的用途和触发条件。Use when the user asks for this workflow or explicitly mentions %s.
---

# %s

在这里描述这个 skill 的用途、触发条件和使用方法。
`, skillID, skillID, skillID)
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
