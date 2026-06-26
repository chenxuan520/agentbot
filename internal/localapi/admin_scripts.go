package localapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

func (s *Server) handleAdminScriptsList(c *gin.Context) {
	if !requireProjectAdminScope(c) {
		return
	}
	items, err := listScriptFiles(s.cfg.RootDir)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) handleAdminScriptFileGet(c *gin.Context) {
	if !requireProjectAdminScope(c) {
		return
	}
	path := strings.TrimSpace(c.Query("path"))
	content, err := readScriptFile(s.cfg.RootDir, path)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"path":    path,
		"content": content,
		"exists":  true,
	})
}

func (s *Server) handleAdminScriptFileUpdate(c *gin.Context) {
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
	if err := writeScriptFile(s.cfg.RootDir, body.Path, body.Content); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func scriptsRoot(rootDir string) string {
	return filepath.Join(rootDir, "scripts")
}

func listScriptFiles(rootDir string) ([]adminFileItem, error) {
	root := scriptsRoot(rootDir)
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return []adminFileItem{}, nil
		}
		return nil, err
	}
	entries := []adminFileItem{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if info.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if isDisallowedScriptPath(rel) {
			return nil
		}
		entries = append(entries, adminFileItem{
			Path:      rel,
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

func readScriptFile(rootDir, relPath string) (string, error) {
	resolved, err := resolveScriptFile(rootDir, relPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func writeScriptFile(rootDir, relPath, content string) error {
	resolved, err := resolveScriptFile(rootDir, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return err
	}
	return os.WriteFile(resolved, []byte(content), 0o644)
}

func resolveScriptFile(rootDir, relPath string) (string, error) {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", fmt.Errorf("path is required")
	}
	cleaned := filepath.Clean(relPath)
	if filepath.IsAbs(cleaned) || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid relative path %q", relPath)
	}
	normalized := filepath.ToSlash(cleaned)
	if isDisallowedScriptPath(normalized) {
		return "", fmt.Errorf("unsupported script path %q", relPath)
	}
	root := scriptsRoot(rootDir)
	resolved := filepath.Join(root, cleaned)
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}
	return resolved, nil
}

func isDisallowedScriptPath(relPath string) bool {
	parts := strings.Split(relPath, "/")
	for _, part := range parts {
		if part == "__pycache__" {
			return true
		}
	}
	return strings.HasSuffix(relPath, ".pyc")
}
