package localapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenxuan520/agentbot/internal/accesstoken"
	"github.com/chenxuan520/agentbot/internal/config"
	storesqlite "github.com/chenxuan520/agentbot/internal/store/sqlite"
)

func TestAdminScriptsListGetAndUpdate(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts", "nested"), 0o755); err != nil {
		t.Fatalf("mkdir scripts dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scripts", "__pycache__"), 0o755); err != nil {
		t.Fatalf("mkdir pycache dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "example.py"), []byte("print('hello')\n"), 0o644); err != nil {
		t.Fatalf("write example.py: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "nested", "worker.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write worker.sh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "__pycache__", "ignored.pyc"), []byte("binary"), 0o644); err != nil {
		t.Fatalf("write ignored.pyc: %v", err)
	}

	cfg := config.Default(root)
	cfg.ProjectToken = "project-token"
	cfg.AuthSecret = "auth-secret"
	store, err := storesqlite.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	access := accesstoken.NewService(store, cfg.ProjectToken, cfg.AuthSecret)
	server := New(cfg, nil, nil, nil, nil, access)
	router := server.router()

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/scripts", nil)
	listReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	listResp := httptest.NewRecorder()
	router.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listResp.Code, listResp.Body.String())
	}
	var listBody struct {
		Items []struct {
			Path string `json:"path"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listResp.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listBody.Items) != 2 {
		t.Fatalf("items len = %d, want 2", len(listBody.Items))
	}
	if listBody.Items[0].Path != "example.py" || listBody.Items[1].Path != "nested/worker.sh" {
		t.Fatalf("unexpected items: %+v", listBody.Items)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/scripts/content?path=example.py", nil)
	getReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	getResp := httptest.NewRecorder()
	router.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get status = %d, body=%s", getResp.Code, getResp.Body.String())
	}
	if !bytes.Contains(getResp.Body.Bytes(), []byte(`"content":"print('hello')\n"`)) {
		t.Fatalf("unexpected get body: %s", getResp.Body.String())
	}

	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/admin/scripts/content", bytes.NewReader([]byte(`{"path":"example.py","content":"print('updated')\n"}`)))
	updateReq.Header.Set("Authorization", "Bearer "+cfg.ProjectToken)
	updateReq.Header.Set("Content-Type", "application/json")
	updateResp := httptest.NewRecorder()
	router.ServeHTTP(updateResp, updateReq)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", updateResp.Code, updateResp.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "scripts", "example.py"))
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	if string(data) != "print('updated')\n" {
		t.Fatalf("updated content = %q", string(data))
	}
}
