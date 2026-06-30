package localapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/chenxuan520/agentbot/internal/conversation"
)

type sessionDataManifest struct {
	Version          int       `json:"version"`
	ExportedAt       time.Time `json:"exportedAt"`
	HasMemory        bool      `json:"hasMemory"`
	HasHooks         bool      `json:"hasHooks"`
	HasSessionSkills bool      `json:"hasSessionSkills"`
}

func sessionSkillRootDir(workspacePath string) string {
	return filepath.Join(workspacePath, ".agents", "session-skills")
}

func sessionMemoryRootDir(workspacePath string) string {
	return filepath.Join(workspacePath, ".agents", "memory")
}

func sessionHooksRootDir(workspacePath string) string {
	return filepath.Join(workspacePath, ".agents", "hooks")
}

func (s *Server) ensureAuthorizedSessionWorkspace(c *gin.Context) (conversation.Ref, string, bool) {
	ref, ok := s.authorizeSessionRef(c)
	if !ok {
		return conversation.Ref{}, "", false
	}
	if s.sessions == nil {
		writeError(c, fmt.Errorf("session service is not configured"))
		return conversation.Ref{}, "", false
	}
	current, err := s.sessions.Current(ref)
	if err != nil {
		writeError(c, err)
		return conversation.Ref{}, "", false
	}
	return ref, current.Workspace.Path, true
}

func syncSessionPrivateSkills(current *Server, ref conversation.Ref) error {
	if current.sessions == nil {
		return fmt.Errorf("session service is not configured")
	}
	_, err := current.sessions.Current(ref)
	return err
}

func (s *Server) handleAdminSessionSkillList(c *gin.Context) {
	_, workspacePath, ok := s.ensureAuthorizedSessionWorkspace(c)
	if !ok {
		return
	}
	items, err := listSkillSummaries(sessionSkillRootDir(workspacePath))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) handleAdminSessionSkillCreate(c *gin.Context) {
	ref, workspacePath, ok := s.ensureAuthorizedSessionWorkspace(c)
	if !ok {
		return
	}
	var body struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	if !bindJSON(c, &body) {
		return
	}
	id := strings.TrimSpace(body.ID)
	if _, _, err := resolveSkillDir(s.cfg.SkillRootDir, id); err == nil {
		writeStatusError(c, http.StatusConflict, fmt.Errorf("skill %q already exists as a public skill", id))
		return
	} else if err != nil && !errors.Is(err, ErrSkillNotFound) {
		writeError(c, err)
		return
	}
	detail, err := createSkill(sessionSkillRootDir(workspacePath), id, body.Content)
	if err != nil {
		writeSkillError(c, err)
		return
	}
	if err := syncSessionPrivateSkills(s, ref); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (s *Server) handleAdminSessionSkillUpload(c *gin.Context) {
	ref, workspacePath, ok := s.ensureAuthorizedSessionWorkspace(c)
	if !ok {
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		writeStatusError(c, http.StatusBadRequest, fmt.Errorf("file is required"))
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		writeError(c, err)
		return
	}
	defer file.Close()
	readerAt, ok := file.(interface {
		io.ReaderAt
		io.Reader
	})
	if !ok {
		writeError(c, fmt.Errorf("uploaded file does not support random access"))
		return
	}
	zipReader, err := zip.NewReader(readerAt, fileHeader.Size)
	if err != nil {
		writeError(c, err)
		return
	}
	skillID, err := inferSkillID(zipReader)
	if err != nil {
		writeError(c, err)
		return
	}
	if _, _, err := resolveSkillDir(s.cfg.SkillRootDir, skillID); err == nil {
		writeStatusError(c, http.StatusConflict, fmt.Errorf("skill %q already exists as a public skill", skillID))
		return
	} else if err != nil && !errors.Is(err, ErrSkillNotFound) {
		writeError(c, err)
		return
	}
	if err := extractSkillZip(sessionSkillRootDir(workspacePath), skillID, zipReader); err != nil {
		writeError(c, err)
		return
	}
	if err := syncSessionPrivateSkills(s, ref); err != nil {
		writeError(c, err)
		return
	}
	detail, err := getSkillDetail(sessionSkillRootDir(workspacePath), skillID, false)
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (s *Server) handleAdminSessionSkillGet(c *gin.Context) {
	_, workspacePath, ok := s.ensureAuthorizedSessionWorkspace(c)
	if !ok {
		return
	}
	detail, err := getSkillDetail(sessionSkillRootDir(workspacePath), strings.TrimSpace(c.Param("skillId")), false)
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (s *Server) handleAdminSessionSkillFilesList(c *gin.Context) {
	_, workspacePath, ok := s.ensureAuthorizedSessionWorkspace(c)
	if !ok {
		return
	}
	items, err := listSkillFiles(sessionSkillRootDir(workspacePath), strings.TrimSpace(c.Param("skillId")))
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) handleAdminSessionSkillFileGet(c *gin.Context) {
	_, workspacePath, ok := s.ensureAuthorizedSessionWorkspace(c)
	if !ok {
		return
	}
	path := strings.TrimSpace(c.Query("path"))
	content, err := readSkillFile(sessionSkillRootDir(workspacePath), strings.TrimSpace(c.Param("skillId")), path)
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": path, "content": content, "exists": true, "readOnly": false})
}

func (s *Server) handleAdminSessionSkillFileCreate(c *gin.Context) {
	_, workspacePath, ok := s.ensureAuthorizedSessionWorkspace(c)
	if !ok {
		return
	}
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !bindJSON(c, &body) {
		return
	}
	if err := createSkillFile(sessionSkillRootDir(workspacePath), strings.TrimSpace(c.Param("skillId")), body.Path, body.Content); err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleAdminSessionSkillFileUpdate(c *gin.Context) {
	_, workspacePath, ok := s.ensureAuthorizedSessionWorkspace(c)
	if !ok {
		return
	}
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !bindJSON(c, &body) {
		return
	}
	if err := writeSkillFile(sessionSkillRootDir(workspacePath), strings.TrimSpace(c.Param("skillId")), body.Path, body.Content); err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleAdminSessionSkillFileDelete(c *gin.Context) {
	_, workspacePath, ok := s.ensureAuthorizedSessionWorkspace(c)
	if !ok {
		return
	}
	if err := deleteSkillFile(sessionSkillRootDir(workspacePath), strings.TrimSpace(c.Param("skillId")), strings.TrimSpace(c.Query("path"))); err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleAdminSessionSkillDelete(c *gin.Context) {
	ref, workspacePath, ok := s.ensureAuthorizedSessionWorkspace(c)
	if !ok {
		return
	}
	if err := deleteSkill(sessionSkillRootDir(workspacePath), strings.TrimSpace(c.Param("skillId"))); err != nil {
		writeSkillError(c, err)
		return
	}
	if err := syncSessionPrivateSkills(s, ref); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleAdminSessionDataExport(c *gin.Context) {
	ref, workspacePath, ok := s.ensureAuthorizedSessionWorkspace(c)
	if !ok {
		return
	}
	data, err := buildSessionDataExport(workspacePath)
	if err != nil {
		writeError(c, err)
		return
	}
	filename := fmt.Sprintf("%s-%s-session-data.zip", sanitizeExportName(ref.Provider), sanitizeExportName(ref.ConversationID))
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Data(http.StatusOK, "application/zip", data)
}

func (s *Server) handleAdminSessionDataImport(c *gin.Context) {
	ref, workspacePath, ok := s.ensureAuthorizedSessionWorkspace(c)
	if !ok {
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		writeStatusError(c, http.StatusBadRequest, fmt.Errorf("file is required"))
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		writeError(c, err)
		return
	}
	defer file.Close()
	readerAt, ok := file.(interface {
		io.ReaderAt
		io.Reader
	})
	if !ok {
		writeError(c, fmt.Errorf("uploaded file does not support random access"))
		return
	}
	zipReader, err := zip.NewReader(readerAt, fileHeader.Size)
	if err != nil {
		writeError(c, err)
		return
	}
	if err := validateSessionDataBundle(zipReader, s.cfg.SkillRootDir); err != nil {
		if errors.Is(err, ErrSkillAlreadyExists) {
			writeStatusError(c, http.StatusConflict, err)
			return
		}
		writeStatusError(c, http.StatusBadRequest, err)
		return
	}
	manifest, err := importSessionDataBundle(workspacePath, zipReader)
	if err != nil {
		writeError(c, err)
		return
	}
	if err := syncSessionPrivateSkills(s, ref); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "manifest": manifest})
}

func validateSessionDataBundle(reader *zip.Reader, publicSkillRoot string) error {
	seenSessionSkills := map[string]struct{}{}
	for _, file := range reader.File {
		name := strings.TrimPrefix(strings.TrimSpace(file.Name), "./")
		if name == "" || strings.HasPrefix(name, "__MACOSX/") {
			continue
		}
		parts := strings.Split(name, "/")
		if len(parts) == 0 || parts[0] != "session-skills" {
			continue
		}
		if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
			if file.FileInfo().IsDir() {
				continue
			}
			return fmt.Errorf("invalid session skill entry %q", name)
		}
		skillID := strings.TrimSpace(parts[1])
		if _, ok := seenSessionSkills[skillID]; ok {
			continue
		}
		seenSessionSkills[skillID] = struct{}{}
		if !isSafeSkillID(skillID) {
			return fmt.Errorf("invalid session skill id %q", skillID)
		}
		if _, _, err := resolveSkillDir(publicSkillRoot, skillID); err == nil {
			return fmt.Errorf("skill %q already exists as a public skill: %w", skillID, ErrSkillAlreadyExists)
		} else if err != nil && !errors.Is(err, ErrSkillNotFound) {
			return err
		}
	}
	return nil
}

func buildSessionDataExport(workspacePath string) ([]byte, error) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	manifest := sessionDataManifest{Version: 1, ExportedAt: time.Now().UTC()}
	if err := addDirToZip(writer, sessionMemoryRootDir(workspacePath), "memory", &manifest.HasMemory); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := addDirToZip(writer, sessionHooksRootDir(workspacePath), "hooks", &manifest.HasHooks); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := addDirToZip(writer, sessionSkillRootDir(workspacePath), "session-skills", &manifest.HasSessionSkills); err != nil {
		_ = writer.Close()
		return nil, err
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	manifestBytes = append(manifestBytes, '\n')
	entry, err := writer.Create("manifest.json")
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	if _, err := entry.Write(manifestBytes); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func addDirToZip(writer *zip.Writer, rootPath, zipPrefix string, hasFiles *bool) error {
	if _, err := os.Stat(rootPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return filepath.Walk(rootPath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(rootPath, path)
		if err != nil {
			return err
		}
		zipPath := filepath.ToSlash(filepath.Join(zipPrefix, rel))
		entry, err := writer.Create(zipPath)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := entry.Write(data); err != nil {
			return err
		}
		*hasFiles = true
		return nil
	})
}

func importSessionDataBundle(workspacePath string, reader *zip.Reader) (sessionDataManifest, error) {
	tempRoot, err := os.MkdirTemp(filepath.Dir(workspacePath), ".session-data-import-")
	if err != nil {
		return sessionDataManifest{}, err
	}
	defer os.RemoveAll(tempRoot)

	manifest := sessionDataManifest{Version: 1}
	for _, file := range reader.File {
		name := strings.TrimPrefix(strings.TrimSpace(file.Name), "./")
		if name == "" || strings.HasPrefix(name, "__MACOSX/") {
			continue
		}
		topLevel := strings.SplitN(name, "/", 2)[0]
		if topLevel != "manifest.json" && topLevel != "memory" && topLevel != "hooks" && topLevel != "session-skills" {
			return sessionDataManifest{}, fmt.Errorf("unsupported session data entry %q", name)
		}
		if file.FileInfo().IsDir() {
			continue
		}
		resolved, err := resolveZipExtractPath(tempRoot, name)
		if err != nil {
			return sessionDataManifest{}, err
		}
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			return sessionDataManifest{}, err
		}
		reader, err := file.Open()
		if err != nil {
			return sessionDataManifest{}, err
		}
		data, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			return sessionDataManifest{}, err
		}
		if name == "manifest.json" {
			if err := json.Unmarshal(data, &manifest); err != nil {
				return sessionDataManifest{}, err
			}
			continue
		}
		if err := os.WriteFile(resolved, data, 0o644); err != nil {
			return sessionDataManifest{}, err
		}
	}
	memorySource := filepath.Join(tempRoot, "memory")
	if err := replaceDir(memorySource, sessionMemoryRootDir(workspacePath)); err != nil {
		return sessionDataManifest{}, err
	}
	hooksSource := filepath.Join(tempRoot, "hooks")
	if err := replaceDir(hooksSource, sessionHooksRootDir(workspacePath)); err != nil {
		return sessionDataManifest{}, err
	}
	skillSource := filepath.Join(tempRoot, "session-skills")
	if err := replaceDir(skillSource, sessionSkillRootDir(workspacePath)); err != nil {
		return sessionDataManifest{}, err
	}
	manifest.HasMemory = dirHasFiles(sessionMemoryRootDir(workspacePath))
	manifest.HasHooks = dirHasFiles(sessionHooksRootDir(workspacePath))
	manifest.HasSessionSkills = dirHasFiles(sessionSkillRootDir(workspacePath))
	return manifest, nil
}

func replaceDir(sourceRoot, targetRoot string) error {
	if err := os.RemoveAll(targetRoot); err != nil {
		return err
	}
	if _, err := os.Stat(sourceRoot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.MkdirAll(targetRoot, 0o755)
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetRoot), 0o755); err != nil {
		return err
	}
	return os.Rename(sourceRoot, targetRoot)
}

func dirHasFiles(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if dirHasFiles(filepath.Join(root, entry.Name())) {
				return true
			}
			continue
		}
		return true
	}
	return false
}

func sanitizeExportName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "session"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(value)
}
