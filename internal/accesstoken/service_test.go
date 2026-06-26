package accesstoken_test

import (
	"strings"
	"testing"

	"github.com/chenxuan520/agentbot/internal/accesstoken"
	"github.com/chenxuan520/agentbot/internal/conversation"
	storesqlite "github.com/chenxuan520/agentbot/internal/store/sqlite"
)

func TestSessionTokenLifecycle(t *testing.T) {
	t.Parallel()

	store, err := storesqlite.Open(t.TempDir() + "/state.sqlite3")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	service := accesstoken.NewService(store, "project-token", "secret-value")
	ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-1"}

	first, err := service.EnsureSessionToken(ref)
	if err != nil {
		t.Fatalf("ensure token: %v", err)
	}
	if first == "" {
		t.Fatal("expected generated token")
	}

	scope, err := service.Validate(first)
	if err != nil {
		t.Fatalf("validate session token: %v", err)
	}
	if scope.Kind != accesstoken.ScopeSession || scope.Ref.Provider != ref.Provider || scope.Ref.ConversationID != ref.ConversationID {
		t.Fatalf("unexpected scope: %+v", scope)
	}

	again, ok, err := service.SessionToken(ref)
	if err != nil {
		t.Fatalf("load token: %v", err)
	}
	if !ok || again != first {
		t.Fatalf("loaded token = %q ok=%v, want %q true", again, ok, first)
	}

	rotated, err := service.RotateSessionToken(ref)
	if err != nil {
		t.Fatalf("rotate token: %v", err)
	}
	if rotated == first {
		t.Fatal("expected rotated token to change")
	}
	if _, err := service.Validate(first); err == nil {
		t.Fatal("expected old token to become invalid after rotation")
	}

	projectScope, err := service.Validate("project-token")
	if err != nil {
		t.Fatalf("validate project token: %v", err)
	}
	if projectScope.Kind != accesstoken.ScopeProject {
		t.Fatalf("project scope kind = %q", projectScope.Kind)
	}
}

// Generated session tokens must stay a single double-click-selectable word, so
// they may not contain '-' (which breaks word selection in most terminals/UIs).
func TestSessionTokenHasNoDash(t *testing.T) {
	t.Parallel()

	store, err := storesqlite.Open(t.TempDir() + "/state.sqlite3")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	service := accesstoken.NewService(store, "project-token", "secret-value")

	for i := 0; i < 200; i++ {
		ref := conversation.Ref{Provider: "feishu", ConversationID: "chat-" + string(rune('a'+i%26)) + strings.Repeat("x", i)}
		token, err := service.RotateSessionToken(ref)
		if err != nil {
			t.Fatalf("rotate token: %v", err)
		}
		if strings.ContainsRune(token, '-') {
			t.Fatalf("session token must not contain '-': %q", token)
		}
		if !strings.HasPrefix(token, "abt_sess_") {
			t.Fatalf("session token missing prefix: %q", token)
		}
		if _, err := service.Validate(token); err != nil {
			t.Fatalf("validate generated token %q: %v", token, err)
		}
	}
}
