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

var ErrSubagentNotFound = errors.New("subagent not found")
var ErrSubagentAlreadyExists = errors.New("subagent already exists")

func (s *Server) handleAdminSubagentList(c *gin.Context) {
	items, err := listSubagentSummaries(s.cfg.SubagentRootDir, filepath.Join(s.cfg.RootDir, "subagents"))
	if err != nil {
		writeSubagentError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) handleAdminSubagentGet(c *gin.Context) {
	scope, ok := adminScopeFromContext(c)
	if !ok {
		writeStatusError(c, http.StatusUnauthorized, fmt.Errorf("missing auth scope"))
		return
	}
	detail, err := getSubagentDetail(s.cfg.SubagentRootDir, filepath.Join(s.cfg.RootDir, "subagents"), strings.TrimSpace(c.Param("subagentId")), scope.Kind != accesstoken.ScopeProject)
	if err != nil {
		writeSubagentError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (s *Server) handleAdminSubagentCreate(c *gin.Context) {
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
	detail, err := createSubagent(s.cfg.SubagentRootDir, filepath.Join(s.cfg.RootDir, "subagents"), body.ID, body.Content)
	if err != nil {
		writeSubagentError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (s *Server) handleAdminSubagentUpdate(c *gin.Context) {
	if !requireProjectAdminScope(c) {
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if !bindJSON(c, &body) {
		return
	}
	detail, err := updateSubagent(s.cfg.SubagentRootDir, filepath.Join(s.cfg.RootDir, "subagents"), strings.TrimSpace(c.Param("subagentId")), body.Content)
	if err != nil {
		writeSubagentError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (s *Server) handleAdminSubagentDelete(c *gin.Context) {
	if !requireProjectAdminScope(c) {
		return
	}
	if err := deleteSubagent(s.cfg.SubagentRootDir, filepath.Join(s.cfg.RootDir, "subagents"), strings.TrimSpace(c.Param("subagentId"))); err != nil {
		writeSubagentError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func listSubagentSummaries(subagentRoot, legacySubagentRoot string) ([]gin.H, error) {
	paths, err := listSubagentSourcePaths(subagentRoot, legacySubagentRoot)
	if err != nil {
		return nil, err
	}

	items := make([]gin.H, 0, len(paths))
	for _, path := range paths {
		item, err := subagentSummaryFromPath(path)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i]["id"].(string) < items[j]["id"].(string)
	})
	return items, nil
}

func getSubagentDetail(subagentRoot, legacySubagentRoot, subagentID string, readOnly bool) (gin.H, error) {
	path, subagentID, err := resolveSubagentPath(subagentRoot, legacySubagentRoot, subagentID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	meta := parseMarkdownFrontmatter(string(data))
	title := strings.TrimSpace(meta["name"])
	if title == "" {
		title = firstMarkdownHeading(string(data))
	}
	if title == "" {
		title = subagentID
	}
	return gin.H{
		"id":          subagentID,
		"title":       title,
		"description": strings.TrimSpace(meta["description"]),
		"mode":        strings.TrimSpace(meta["mode"]),
		"hasFile":     true,
		"path":        path,
		"updatedAt":   info.ModTime().UTC(),
		"readOnly":    readOnly,
		"content":     string(data),
	}, nil
}

func createSubagent(subagentRoot, legacySubagentRoot, subagentID, content string) (gin.H, error) {
	subagentID = strings.TrimSpace(subagentID)
	if !isSafeSkillID(subagentID) {
		return nil, fmt.Errorf("invalid subagent id %q", subagentID)
	}
	if _, _, err := resolveSubagentPath(subagentRoot, legacySubagentRoot, subagentID); err == nil {
		return nil, fmt.Errorf("subagent %q already exists: %w", subagentID, ErrSubagentAlreadyExists)
	} else if !errors.Is(err, ErrSubagentNotFound) {
		return nil, err
	}
	if err := os.MkdirAll(subagentRoot, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(subagentRoot, subagentID+".md")
	if strings.TrimSpace(content) == "" {
		content = defaultSubagentContent(subagentID)
	}
	if err := os.WriteFile(path, []byte(ensureTrailingNewline(content)), 0o644); err != nil {
		return nil, err
	}
	return getSubagentDetail(subagentRoot, legacySubagentRoot, subagentID, false)
}

func updateSubagent(subagentRoot, legacySubagentRoot, subagentID, content string) (gin.H, error) {
	path, subagentID, err := resolveSubagentPath(subagentRoot, legacySubagentRoot, subagentID)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(ensureTrailingNewline(content)), 0o644); err != nil {
		return nil, err
	}
	return getSubagentDetail(subagentRoot, legacySubagentRoot, subagentID, false)
}

func deleteSubagent(subagentRoot, legacySubagentRoot, subagentID string) error {
	paths, _, err := resolveSubagentPaths(subagentRoot, legacySubagentRoot, subagentID)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func subagentSummaryFromPath(path string) (gin.H, error) {
	name := strings.TrimSpace(filepath.Base(path))
	id := strings.TrimSuffix(name, filepath.Ext(name))
	if !isSafeSkillID(id) {
		return nil, fmt.Errorf("invalid subagent id %q", id)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	meta := parseMarkdownFrontmatter(string(data))
	title := strings.TrimSpace(meta["name"])
	if title == "" {
		title = firstMarkdownHeading(string(data))
	}
	if title == "" {
		title = id
	}
	return gin.H{
		"id":          id,
		"title":       title,
		"description": strings.TrimSpace(meta["description"]),
		"mode":        strings.TrimSpace(meta["mode"]),
		"hasFile":     true,
	}, nil
}

func resolveSubagentPath(subagentRoot, legacySubagentRoot, subagentID string) (string, string, error) {
	paths, subagentID, err := resolveSubagentPaths(subagentRoot, legacySubagentRoot, subagentID)
	if err != nil {
		return "", "", err
	}
	return paths[0], subagentID, nil
}

func resolveSubagentPaths(subagentRoot, legacySubagentRoot, subagentID string) ([]string, string, error) {
	subagentID = strings.TrimSpace(subagentID)
	if !isSafeSkillID(subagentID) {
		return nil, "", fmt.Errorf("invalid subagent id %q", subagentID)
	}
	paths := make([]string, 0, 2)
	for _, root := range subagentSourceRoots(subagentRoot, legacySubagentRoot) {
		path := filepath.Join(root, subagentID+".md")
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, "", err
		}
		if info.IsDir() {
			continue
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil, "", fmt.Errorf("subagent %q not found: %w", subagentID, ErrSubagentNotFound)
	}
	return paths, subagentID, nil
}

func listSubagentSourcePaths(subagentRoot, legacySubagentRoot string) ([]string, error) {
	pathsByID := map[string]string{}
	for _, root := range subagentSourceRoots(subagentRoot, legacySubagentRoot) {
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := strings.TrimSpace(entry.Name())
			if name == "" || strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".md") {
				continue
			}
			id := strings.TrimSuffix(name, filepath.Ext(name))
			if _, exists := pathsByID[id]; exists {
				continue
			}
			pathsByID[id] = filepath.Join(root, name)
		}
	}
	paths := make([]string, 0, len(pathsByID))
	for _, path := range pathsByID {
		paths = append(paths, path)
	}
	return paths, nil
}

func subagentSourceRoots(subagentRoot, legacySubagentRoot string) []string {
	roots := []string{}
	seen := map[string]struct{}{}
	for _, root := range []string{subagentRoot, legacySubagentRoot} {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	return roots
}

func writeSubagentError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrSubagentNotFound):
		writeStatusError(c, http.StatusNotFound, err)
	case errors.Is(err, ErrSubagentAlreadyExists):
		writeStatusError(c, http.StatusConflict, err)
	default:
		writeError(c, err)
	}
}

func parseMarkdownFrontmatter(content string) map[string]string {
	content = strings.TrimPrefix(content, "\ufeff")
	if !strings.HasPrefix(content, "---\n") {
		return map[string]string{}
	}
	lines := strings.Split(content, "\n")
	meta := map[string]string{}
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			return meta
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			continue
		}
		meta[key] = strings.Trim(value, `"'`)
	}
	return map[string]string{}
}

func defaultSubagentContent(subagentID string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: \nmode: subagent\n---\n\n# %s\n", subagentID, subagentID)
}

func ensureTrailingNewline(content string) string {
	if strings.HasSuffix(content, "\n") {
		return content
	}
	return content + "\n"
}
