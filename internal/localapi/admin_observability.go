package localapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/chenxuan520/agentbot/internal/backendfactory"
	"github.com/chenxuan520/agentbot/internal/observability"
)

// handleAdminObservability returns recent recorded failures, counters, and a
// best-effort live health check of the default backend and provider. It is
// project-scoped because the data spans every conversation.
func (s *Server) handleAdminObservability(c *gin.Context) {
	if !requireProjectAdminScope(c) {
		return
	}
	snapshot := observability.Default.Snapshot()
	c.JSON(http.StatusOK, gin.H{
		"startedAt": snapshot.StartedAt,
		"now":       snapshot.Now,
		"counters":  snapshot.Counters,
		"events":    snapshot.Events,
		"health": gin.H{
			"backend":  s.backendHealth(c.Request.Context()),
			"provider": s.providerHealth(c.Request.Context()),
		},
	})
}

func (s *Server) backendHealth(ctx context.Context) gin.H {
	name := s.cfg.DefaultBackend
	client, err := backendfactory.FromConfig(s.cfg, name)
	if err != nil {
		return gin.H{"name": name, "ok": false, "error": err.Error()}
	}
	hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := client.Health(hctx); err != nil {
		return gin.H{"name": client.Name(), "ok": false, "error": err.Error()}
	}
	return gin.H{"name": client.Name(), "ok": true}
}

func (s *Server) providerHealth(ctx context.Context) gin.H {
	name := s.cfg.DefaultProvider
	if s.providers == nil {
		return gin.H{"name": name, "ok": false, "error": "provider factory is not configured"}
	}
	client, err := s.providers(s.cfg, name)
	if err != nil {
		return gin.H{"name": name, "ok": false, "error": err.Error()}
	}
	hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := client.Health(hctx); err != nil {
		return gin.H{"name": client.Name(), "ok": false, "error": err.Error()}
	}
	return gin.H{"name": client.Name(), "ok": true}
}
