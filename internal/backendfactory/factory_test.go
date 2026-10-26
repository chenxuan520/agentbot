package backendfactory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chenxuan520/agentbot/internal/config"
)

func TestFactoryAutomaticallySelectsBothOpenCodeAPIs(t *testing.T) {
	t.Parallel()
	for _, native := range []bool{false, true} {
		name := "v1"
		if native {
			name = "v2"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				username, password, ok := r.BasicAuth()
				if !ok || username != "opencode" || password != "test-server-password" {
					t.Error("factory did not provide backend authentication")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/info":
					if native {
						_, _ = w.Write([]byte(`{"version":"2.1.2"}`))
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				case "/global/health":
					if native {
						t.Error("V2 was sent a legacy health request")
					}
					_, _ = w.Write([]byte(`{"healthy":true,"version":"1.18.16"}`))
				default:
					t.Errorf("unexpected backend request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			cfg := config.Default(t.TempDir())
			cfg.BackendBaseURLs["opencode"] = server.URL
			cfg.OpencodePassword = "test-server-password"
			client, err := FromConfig(cfg, "opencode")
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Health(context.Background()); err != nil {
				t.Fatal(err)
			}
			if client.Name() != "opencode" {
				t.Fatalf("backend name changed: %s", client.Name())
			}
		})
	}
}
