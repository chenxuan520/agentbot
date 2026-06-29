package localapi

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/chenxuan520/agentbot/internal/backend"
)

type adminModelOption struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ContextLimit int    `json:"contextLimit"`
}

type adminModelProvider struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Default string             `json:"default"`
	Models  []adminModelOption `json:"models"`
}

type adminModelsResponse struct {
	Current   string               `json:"current"`
	Providers []adminModelProvider `json:"providers"`
}

func (s *Server) handleAdminSessionModels(c *gin.Context) {
	ref, ok := s.authorizeSessionRef(c)
	if !ok {
		return
	}
	current, err := s.sessions.Current(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	if s.backends == nil {
		writeError(c, fmt.Errorf("backend factory is not configured"))
		return
	}
	client, err := s.backends(s.cfg, current.Workspace.Settings)
	if err != nil {
		writeError(c, err)
		return
	}
	catalogProvider, ok := client.(backend.SessionModelCatalog)
	if !ok {
		writeStatusError(c, http.StatusNotImplemented, fmt.Errorf("backend %q does not expose a model catalog", current.AgentBackend))
		return
	}
	catalog, err := catalogProvider.ListModels(c.Request.Context(), current.Workspace.Path)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, marshalModelsResponse(catalog))
}

func marshalModelsResponse(catalog backend.ModelCatalog) adminModelsResponse {
	providers := make([]adminModelProvider, 0, len(catalog.Providers))
	for _, provider := range catalog.Providers {
		models := make([]adminModelOption, 0, len(provider.Models))
		for _, model := range provider.Models {
			models = append(models, adminModelOption{
				ID:           model.ID,
				Name:         model.Name,
				ContextLimit: model.ContextLimit,
			})
		}
		providers = append(providers, adminModelProvider{
			ID:      provider.ID,
			Name:    provider.Name,
			Default: provider.Default,
			Models:  models,
		})
	}
	return adminModelsResponse{Current: catalog.Current, Providers: providers}
}
