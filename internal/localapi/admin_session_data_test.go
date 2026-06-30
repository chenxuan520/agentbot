package localapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenxuan520/agentbot/internal/accesstoken"
	"github.com/chenxuan520/agentbot/internal/config"
	"github.com/chenxuan520/agentbot/internal/conversation"
	"github.com/chenxuan520/agentbot/internal/session"
	storesqlite "github.com/chenxuan520/agentbot/internal/store/sqlite"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

func TestSessionTokenCanManageSessionSkillsAndImportExportData(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-private-skill"}
	current, err := sessions.Current(ref)
	if err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	token, err := access.EnsureSessionToken(ref)
	if err != nil {
		t.Fatalf("ensure session token: %v", err)
	}

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/sessions/feishu/chat-private-skill/session-skills", bytes.NewReader([]byte(`{"id":"local-skill","content":"# Local Skill\n\nprivate\n"}`)))
	createReq.Header.Set("Authorization", "Bearer "+token)
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	router.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%s", createResp.Code, createResp.Body.String())
	}

	settings, err := workspace.LoadSettings(current.Workspace.Path)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if len(settings.Mounts.SkillIDs) != 0 {
		t.Fatalf("private skill should not be stored in mounts.skillIds: %+v", settings.Mounts.SkillIDs)
	}
	if _, err := os.Lstat(filepath.Join(current.Workspace.Path, ".agents", "skills", "local-skill")); err != nil {
		t.Fatalf("stat mounted private skill: %v", err)
	}

	memoryPath := filepath.Join(current.Workspace.Path, ".agents", "memory", "info.md")
	if err := os.WriteFile(memoryPath, []byte("remember\n"), 0o644); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	hookPath := filepath.Join(current.Workspace.Path, ".agents", "hooks", "before_message.py")
	if err := os.WriteFile(hookPath, []byte("# hook\n"), 0o644); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	reactionHookPath := filepath.Join(current.Workspace.Path, ".agents", "hooks", "on_reaction.py")
	if err := os.WriteFile(reactionHookPath, []byte("# reaction hook\n"), 0o644); err != nil {
		t.Fatalf("write reaction hook: %v", err)
	}

	exportReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/feishu/chat-private-skill/session-data/export", nil)
	exportReq.Header.Set("Authorization", "Bearer "+token)
	exportResp := httptest.NewRecorder()
	router.ServeHTTP(exportResp, exportReq)
	if exportResp.Code != http.StatusOK {
		t.Fatalf("export status = %d, body=%s", exportResp.Code, exportResp.Body.String())
	}
	exported := exportResp.Body.Bytes()
	if len(exported) == 0 {
		t.Fatal("expected exported zip data")
	}

	if err := os.RemoveAll(filepath.Join(current.Workspace.Path, ".agents", "session-skills")); err != nil {
		t.Fatalf("remove session skills: %v", err)
	}
	if err := os.WriteFile(memoryPath, []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("overwrite memory: %v", err)
	}
	if err := os.WriteFile(hookPath, []byte("# changed hook\n"), 0o644); err != nil {
		t.Fatalf("overwrite hook: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "session-data.zip")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(exported); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	importReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/sessions/feishu/chat-private-skill/session-data/import", &body)
	importReq.Header.Set("Authorization", "Bearer "+token)
	importReq.Header.Set("Content-Type", writer.FormDataContentType())
	importResp := httptest.NewRecorder()
	router.ServeHTTP(importResp, importReq)
	if importResp.Code != http.StatusOK {
		t.Fatalf("import status = %d, body=%s", importResp.Code, importResp.Body.String())
	}
	var importBody struct {
		Manifest struct {
			HasMemory        bool `json:"hasMemory"`
			HasHooks         bool `json:"hasHooks"`
			HasSessionSkills bool `json:"hasSessionSkills"`
		} `json:"manifest"`
	}
	if err := json.Unmarshal(importResp.Body.Bytes(), &importBody); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if !importBody.Manifest.HasMemory || !importBody.Manifest.HasSessionSkills {
		t.Fatalf("unexpected manifest: %s", importResp.Body.String())
	}
	if !importBody.Manifest.HasHooks {
		t.Fatalf("expected hooks in manifest: %s", importResp.Body.String())
	}

	memoryData, err := os.ReadFile(memoryPath)
	if err != nil {
		t.Fatalf("read imported memory: %v", err)
	}
	if string(memoryData) != "remember\n" {
		t.Fatalf("memory content = %q, want remember", string(memoryData))
	}
	hookData, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read imported hook: %v", err)
	}
	if string(hookData) != "# hook\n" {
		t.Fatalf("hook content = %q, want original hook", string(hookData))
	}
	reactionHookData, err := os.ReadFile(reactionHookPath)
	if err != nil {
		t.Fatalf("read imported reaction hook: %v", err)
	}
	if string(reactionHookData) != "# reaction hook\n" {
		t.Fatalf("reaction hook content = %q, want original reaction hook", string(reactionHookData))
	}
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sessions/feishu/chat-private-skill/session-skills", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listResp := httptest.NewRecorder()
	router.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listResp.Code, listResp.Body.String())
	}
	var listBody struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listResp.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listBody.Items) != 1 || listBody.Items[0].ID != "local-skill" {
		t.Fatalf("unexpected session skills: %s", listResp.Body.String())
	}
}

func TestSessionTokenCanUploadPrivateSkillZip(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-private-skill-upload"}
	current, err := sessions.Current(ref)
	if err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	token, err := access.EnsureSessionToken(ref)
	if err != nil {
		t.Fatalf("ensure session token: %v", err)
	}

	zipData, err := buildSkillZip("local-zip-skill", map[string]string{
		"SKILL.md":          "# Local Zip Skill\n\nprivate\n",
		"references/doc.md": "hello\n",
	})
	if err != nil {
		t.Fatalf("build skill zip: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "local-zip-skill.zip")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(zipData); err != nil {
		t.Fatalf("write zip body: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/sessions/feishu/chat-private-skill-upload/session-skills/upload", &body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body=%s", resp.Code, resp.Body.String())
	}
	if _, err := os.Stat(filepath.Join(current.Workspace.Path, ".agents", "session-skills", "local-zip-skill", "SKILL.md")); err != nil {
		t.Fatalf("stat uploaded session skill: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(current.Workspace.Path, ".agents", "skills", "local-zip-skill")); err != nil {
		t.Fatalf("stat mounted uploaded session skill: %v", err)
	}
}

func TestSessionDataImportRejectsPrivateSkillThatConflictsWithPublicSkill(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)
	writeSkill(t, root, "public-skill", "Public Skill", map[string]string{"SKILL.md": "# Public Skill\n"})

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-private-skill-conflict"}
	if _, err := sessions.Current(ref); err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	token, err := access.EnsureSessionToken(ref)
	if err != nil {
		t.Fatalf("ensure session token: %v", err)
	}

	zipData, err := buildSessionDataZip(map[string]string{
		"session-skills/public-skill/SKILL.md": "# Shadow Skill\n",
		"manifest.json":                        "{\"version\":1}\n",
	})
	if err != nil {
		t.Fatalf("build session data zip: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "session-data.zip")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(zipData); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	importReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/sessions/feishu/chat-private-skill-conflict/session-data/import", &body)
	importReq.Header.Set("Authorization", "Bearer "+token)
	importReq.Header.Set("Content-Type", writer.FormDataContentType())
	importResp := httptest.NewRecorder()
	router.ServeHTTP(importResp, importReq)
	if importResp.Code != http.StatusConflict {
		t.Fatalf("import status = %d, body=%s", importResp.Code, importResp.Body.String())
	}
}

func TestSessionSkillFileDelete(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTemplate(t, root)

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	manager := workspace.NewManager(cfg, store)
	sessions := session.NewService(store, manager)
	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, sessions, nil, access)
	router := server.router()

	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-session-skill-file"}
	current, err := sessions.Current(ref)
	if err != nil {
		t.Fatalf("prepare session: %v", err)
	}
	token, err := access.EnsureSessionToken(ref)
	if err != nil {
		t.Fatalf("ensure session token: %v", err)
	}

	base := "/api/v1/admin/sessions/feishu/chat-session-skill-file/session-skills"

	createReq := httptest.NewRequest(http.MethodPost, base, bytes.NewReader([]byte(`{"id":"local-skill","content":"# Local Skill\n"}`)))
	createReq.Header.Set("Authorization", "Bearer "+token)
	createReq.Header.Set("Content-Type", "application/json")
	createResp := httptest.NewRecorder()
	router.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusOK {
		t.Fatalf("create session skill status = %d, body=%s", createResp.Code, createResp.Body.String())
	}

	addReq := httptest.NewRequest(http.MethodPost, base+"/local-skill/files/content", bytes.NewReader([]byte(`{"path":"bin/run.sh","content":"#!/usr/bin/env bash\n"}`)))
	addReq.Header.Set("Authorization", "Bearer "+token)
	addReq.Header.Set("Content-Type", "application/json")
	addResp := httptest.NewRecorder()
	router.ServeHTTP(addResp, addReq)
	if addResp.Code != http.StatusOK {
		t.Fatalf("add session skill file status = %d, body=%s", addResp.Code, addResp.Body.String())
	}
	skillDir := filepath.Join(current.Workspace.Path, ".agents", "session-skills", "local-skill")
	if _, err := os.Stat(filepath.Join(skillDir, "bin", "run.sh")); err != nil {
		t.Fatalf("expected session skill file created: %v", err)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, base+"/local-skill/files/content?path=bin/run.sh", nil)
	deleteReq.Header.Set("Authorization", "Bearer "+token)
	deleteResp := httptest.NewRecorder()
	router.ServeHTTP(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete session skill file status = %d, body=%s", deleteResp.Code, deleteResp.Body.String())
	}
	if _, err := os.Stat(filepath.Join(skillDir, "bin", "run.sh")); !os.IsNotExist(err) {
		t.Fatalf("expected session skill file removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "bin")); !os.IsNotExist(err) {
		t.Fatalf("expected empty parent dir pruned, err=%v", err)
	}

	aliasReq := httptest.NewRequest(http.MethodPost, base+"/local-skill/files/content", bytes.NewReader([]byte(`{"path":"./SKILL.md","content":"overwritten\n"}`)))
	aliasReq.Header.Set("Authorization", "Bearer "+token)
	aliasReq.Header.Set("Content-Type", "application/json")
	aliasResp := httptest.NewRecorder()
	router.ServeHTTP(aliasResp, aliasReq)
	if aliasResp.Code != http.StatusConflict {
		t.Fatalf("create alias session skill file status = %d, body=%s", aliasResp.Code, aliasResp.Body.String())
	}

	protectedReq := httptest.NewRequest(http.MethodDelete, base+"/local-skill/files/content?path=SKILL.md", nil)
	protectedReq.Header.Set("Authorization", "Bearer "+token)
	protectedResp := httptest.NewRecorder()
	router.ServeHTTP(protectedResp, protectedReq)
	if protectedResp.Code != http.StatusBadRequest {
		t.Fatalf("delete session SKILL.md status = %d, body=%s", protectedResp.Code, protectedResp.Body.String())
	}
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatalf("expected session SKILL.md preserved, err=%v", err)
	}

	skillInfo, err := os.Stat(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("stat session skill entry file: %v", err)
	}
	expectedLowercaseStatus := http.StatusNotFound
	if aliasInfo, err := os.Stat(filepath.Join(skillDir, "skill.md")); err == nil {
		if os.SameFile(aliasInfo, skillInfo) {
			expectedLowercaseStatus = http.StatusBadRequest
		}
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat session lowercase alias path: %v", err)
	}

	protectedCaseReq := httptest.NewRequest(http.MethodDelete, base+"/local-skill/files/content?path=skill.md", nil)
	protectedCaseReq.Header.Set("Authorization", "Bearer "+token)
	protectedCaseResp := httptest.NewRecorder()
	router.ServeHTTP(protectedCaseResp, protectedCaseReq)
	if protectedCaseResp.Code != expectedLowercaseStatus {
		t.Fatalf("delete lowercase session skill.md status = %d, want %d, body=%s", protectedCaseResp.Code, expectedLowercaseStatus, protectedCaseResp.Body.String())
	}
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatalf("expected session SKILL.md preserved after lowercase delete, err=%v", err)
	}
}

func buildSkillZip(skillID string, files map[string]string) ([]byte, error) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	keys := make([]string, 0, len(files))
	for path := range files {
		keys = append(keys, path)
	}
	for _, path := range keys {
		entry, err := writer.Create(skillID + "/" + path)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write([]byte(files[path])); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func buildSessionDataZip(files map[string]string) ([]byte, error) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for path, content := range files {
		entry, err := writer.Create(path)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
