package localapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/chenxuan520/agentbot/internal/accesstoken"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

func (s *Server) handleAdminRoleList(c *gin.Context) {
	if !requireProjectAdminScope(c) {
		return
	}
	if s.sessions == nil {
		writeError(c, fmt.Errorf("session service is not configured"))
		return
	}
	items, err := s.sessions.ListTemplateSummaries()
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) handleAdminRoleGet(c *gin.Context) {
	if !requireProjectAdminScope(c) {
		return
	}
	if s.sessions == nil {
		writeError(c, fmt.Errorf("session service is not configured"))
		return
	}
	detail, err := s.sessions.GetTemplate(strings.TrimSpace(c.Param("roleId")))
	if err != nil {
		writeRoleError(c, err)
		return
	}
	c.JSON(http.StatusOK, roleDetailPayload(detail))
}

func (s *Server) handleAdminRoleCreate(c *gin.Context) {
	if !requireProjectAdminScope(c) {
		return
	}
	if s.sessions == nil {
		writeError(c, fmt.Errorf("session service is not configured"))
		return
	}
	var body struct {
		Name     string `json:"name"`
		CopyFrom string `json:"copyFrom"`
	}
	if !bindJSON(c, &body) {
		return
	}
	detail, err := s.sessions.CreateTemplate(body.Name, body.CopyFrom)
	if err != nil {
		writeRoleError(c, err)
		return
	}
	c.JSON(http.StatusOK, roleDetailPayload(detail))
}

func (s *Server) handleAdminRoleUpdate(c *gin.Context) {
	if !requireProjectAdminScope(c) {
		return
	}
	if s.sessions == nil {
		writeError(c, fmt.Errorf("session service is not configured"))
		return
	}
	var body struct {
		Settings      workspace.Settings `json:"settings"`
		AgentsContent string             `json:"agentsContent"`
	}
	if !bindJSON(c, &body) {
		return
	}
	detail, err := s.sessions.UpdateTemplate(strings.TrimSpace(c.Param("roleId")), body.Settings, body.AgentsContent)
	if err != nil {
		writeRoleError(c, err)
		return
	}
	c.JSON(http.StatusOK, roleDetailPayload(detail))
}

func (s *Server) handleAdminRoleDelete(c *gin.Context) {
	if !requireProjectAdminScope(c) {
		return
	}
	if s.sessions == nil {
		writeError(c, fmt.Errorf("session service is not configured"))
		return
	}
	if err := s.sessions.DeleteTemplate(strings.TrimSpace(c.Param("roleId"))); err != nil {
		writeRoleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func requireProjectAdminScope(c *gin.Context) bool {
	scope, ok := adminScopeFromContext(c)
	if !ok {
		writeStatusError(c, http.StatusUnauthorized, fmt.Errorf("missing auth scope"))
		return false
	}
	if scope.Kind != accesstoken.ScopeProject {
		writeStatusError(c, http.StatusForbidden, fmt.Errorf("session token cannot manage roles"))
		return false
	}
	return true
}

func writeRoleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, workspace.ErrTemplateNotFound):
		writeStatusError(c, http.StatusNotFound, err)
	case errors.Is(err, workspace.ErrTemplateAlreadyExists), errors.Is(err, workspace.ErrTemplateInUse), errors.Is(err, workspace.ErrTemplateProtected):
		writeStatusError(c, http.StatusConflict, err)
	default:
		writeError(c, err)
	}
}

func roleDetailPayload(detail *workspace.TemplateDetail) gin.H {
	return gin.H{
		"id":           detail.Name,
		"path":         detail.Path,
		"settings":     detail.Settings,
		"sessionCount": detail.SessionCount,
		"updatedAt":    detail.UpdatedAt,
		"agentsFile": gin.H{
			"path":         "AGENTS.md",
			"resolvedPath": detail.AgentsPath,
			"content":      detail.AgentsContent,
			"readOnly":     false,
		},
	}
}
