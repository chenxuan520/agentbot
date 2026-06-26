package localapi

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/chenxuan520/agentbot/internal/accesstoken"
	"github.com/chenxuan520/agentbot/internal/conversation"
	providerapi "github.com/chenxuan520/agentbot/internal/providerapi"
	"github.com/chenxuan520/agentbot/internal/session"
	"github.com/chenxuan520/agentbot/internal/workspace"
)

const adminScopeKey = "adminScope"

var allowedHookFiles = map[string]struct{}{
	"before_message.py": {},
	"on_reaction.py":    {},
	"after_reply.py":    {},
}

type adminFileItem struct {
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updatedAt"`
	Exists    bool      `json:"exists"`
}

type adminSessionSummary struct {
	session.Summary
	DisplayName string `json:"displayName,omitempty"`
	ChatMode    string `json:"chatMode,omitempty"`
}

func (s *Server) requireAdminToken(c *gin.Context) {
	if s.access == nil {
		writeStatusError(c, http.StatusServiceUnavailable, fmt.Errorf("admin auth is not configured"))
		c.Abort()
		return
	}
	token := strings.TrimSpace(c.GetHeader("X-Agent-Bot-Token"))
	if token == "" {
		header := strings.TrimSpace(c.GetHeader("Authorization"))
		if len(header) > 7 && strings.EqualFold(header[:7], "Bearer ") {
			token = strings.TrimSpace(header[7:])
		}
	}
	if token == "" {
		writeStatusError(c, http.StatusUnauthorized, fmt.Errorf("missing bearer token"))
		c.Abort()
		return
	}
	scope, err := s.access.Validate(token)
	if err != nil {
		writeStatusError(c, http.StatusUnauthorized, err)
		c.Abort()
		return
	}
	c.Set(adminScopeKey, scope)
	c.Next()
}

func (s *Server) handleAdminMe(c *gin.Context) {
	scope, ok := adminScopeFromContext(c)
	if !ok {
		writeStatusError(c, http.StatusUnauthorized, fmt.Errorf("missing auth scope"))
		return
	}
	body := gin.H{"scope": scope.Kind}
	if scope.Kind == accesstoken.ScopeSession {
		body["provider"] = scope.Ref.Provider
		body["conversationId"] = scope.Ref.ConversationID
	}
	c.JSON(http.StatusOK, body)
}

func (s *Server) handleAdminSessionList(c *gin.Context) {
	scope, ok := adminScopeFromContext(c)
	if !ok {
		writeStatusError(c, http.StatusUnauthorized, fmt.Errorf("missing auth scope"))
		return
	}
	if scope.Kind != accesstoken.ScopeProject {
		writeStatusError(c, http.StatusForbidden, fmt.Errorf("session token cannot list all sessions"))
		return
	}
	items, err := s.sessions.List()
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) handleAdminSessionDisplayNames(c *gin.Context) {
	scope, ok := adminScopeFromContext(c)
	if !ok {
		writeStatusError(c, http.StatusUnauthorized, fmt.Errorf("missing auth scope"))
		return
	}
	var body struct {
		Sessions []struct {
			Provider       string `json:"provider"`
			ConversationID string `json:"conversationId"`
		} `json:"sessions"`
	}
	if !bindJSON(c, &body) {
		return
	}
	refs := make([]conversation.Ref, 0, len(body.Sessions))
	seen := map[string]struct{}{}
	for _, item := range body.Sessions {
		ref := conversation.Ref{Provider: strings.TrimSpace(item.Provider), ConversationID: strings.TrimSpace(item.ConversationID)}
		if ref.Provider == "" || ref.ConversationID == "" {
			continue
		}
		if !scopeAllowsRef(scope, ref) {
			writeStatusError(c, http.StatusForbidden, fmt.Errorf("session token can only access its own session"))
			return
		}
		key := ref.Provider + ":" + ref.ConversationID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, ref)
	}
	c.JSON(http.StatusOK, gin.H{"items": s.resolveDisplayNamesBatch(c.Request.Context(), refs)})
}

func (s *Server) handleAdminRemoteStatus(c *gin.Context) {
	ref, ok := s.authorizeSessionRef(c)
	if !ok {
		return
	}
	enabled := false
	if s.sessions != nil {
		var err error
		enabled, err = s.sessions.RemoteEnabled(ref)
		if err != nil {
			writeError(c, err)
			return
		}
	}
	connected := false
	routeLocal := false
	agentID := ""
	sessionID := ""
	title := ""
	if s.remote != nil {
		status := s.remote.Status(ref)
		connected = status.Connected
		routeLocal = status.RouteLocal
		agentID = status.AgentID
		sessionID = status.SessionID
		title = status.Title
	}
	// route reflects what inbound messages actually do right now: disabled when
	// the feature is off, local only when enabled + connected + not forced to bot.
	route := "disabled"
	if enabled {
		if connected && routeLocal {
			route = "local"
		} else {
			route = "bot"
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":   enabled,
		"connected": connected,
		"route":     route,
		"agentId":   agentID,
		"sessionId": sessionID,
		"title":     title,
	})
}

func (s *Server) handleAdminSessionDetail(c *gin.Context) {
	ref, ok := s.authorizeSessionRef(c)
	if !ok {
		return
	}
	current, err := s.sessions.Current(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	templates, err := s.sessions.ListTemplates()
	if err != nil {
		writeError(c, err)
		return
	}
	sessionToken, err := s.access.EnsureSessionToken(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	displayInfo, _ := s.resolveSessionDisplayInfo(c.Request.Context(), ref)
	c.JSON(http.StatusOK, gin.H{
		"provider":           ref.Provider,
		"conversationId":     ref.ConversationID,
		"displayName":        strings.TrimSpace(displayInfo.DisplayName),
		"chatMode":           strings.TrimSpace(displayInfo.ChatMode),
		"workspacePath":      current.Workspace.Path,
		"backend":            current.AgentBackend,
		"activeSessionId":    current.ActiveSessionID,
		"lastMessageAt":      current.LastMessageAt,
		"settings":           current.Workspace.Settings,
		"availableTemplates": templates,
		"sessionToken":       sessionToken,
	})
}

func (s *Server) handleAdminSessionDelete(c *gin.Context) {
	if !requireProjectAdminScope(c) {
		return
	}
	ref := conversation.Ref{Provider: strings.TrimSpace(c.Param("provider")), ConversationID: strings.TrimSpace(c.Param("conversationId"))}
	if ref.Provider == "" || ref.ConversationID == "" {
		writeStatusError(c, http.StatusBadRequest, fmt.Errorf("provider and conversationId are required"))
		return
	}
	if err := s.sessions.Delete(ref); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleAdminSessionAgentsGet(c *gin.Context) {
	ref, ok := s.authorizeSessionRef(c)
	if !ok {
		return
	}
	current, err := s.sessions.Current(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	payload, err := buildSessionAgentsPayload(current.Workspace.Path, current.Workspace.Settings)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (s *Server) handleAdminSessionAgentsUpdate(c *gin.Context) {
	ref, ok := s.authorizeSessionRef(c)
	if !ok {
		return
	}
	var body struct {
		Mode    string  `json:"mode"`
		Content *string `json:"content"`
	}
	if !bindJSON(c, &body) {
		return
	}
	current, err := s.sessions.Current(ref)
	if err != nil {
		writeError(c, err)
		return
	}

	desiredMode := workspace.NormalizeAgentsMode(current.Workspace.Settings.Agent.AgentsMode)
	if rawMode := strings.TrimSpace(body.Mode); rawMode != "" {
		switch strings.ToLower(rawMode) {
		case workspace.AgentsModeTemplate, workspace.AgentsModeCustom:
			desiredMode = workspace.NormalizeAgentsMode(rawMode)
		default:
			writeStatusError(c, http.StatusBadRequest, fmt.Errorf("invalid agents mode %q", rawMode))
			return
		}
	}
	if desiredMode == workspace.AgentsModeTemplate && body.Content != nil {
		writeStatusError(c, http.StatusConflict, fmt.Errorf("current session follows template AGENTS.md; switch to custom mode before editing"))
		return
	}

	nextSettings := current.Workspace.Settings
	modeChanged := workspace.NormalizeAgentsMode(nextSettings.Agent.AgentsMode) != desiredMode
	if modeChanged {
		nextSettings.Agent.AgentsMode = desiredMode
		if err := workspace.SaveSettings(current.Workspace.Path, nextSettings); err != nil {
			writeError(c, err)
			return
		}
	}

	var customContent []byte
	if desiredMode == workspace.AgentsModeCustom {
		if body.Content != nil {
			customContent = []byte(*body.Content)
		} else {
			currentContent, err := os.ReadFile(filepath.Join(current.Workspace.Path, workspace.AgentsFileName))
			if err != nil {
				writeError(c, err)
				return
			}
			customContent = currentContent
		}
	}

	templateDir := filepath.Join(s.cfg.TemplateRootDir, nextSettings.Template)
	if err := workspace.SyncAgentsFile(templateDir, current.Workspace.Path, nextSettings, customContent); err != nil {
		writeError(c, err)
		return
	}

	if modeChanged || body.Content != nil {
		if err := s.sessions.ClearActive(ref); err != nil {
			writeError(c, err)
			return
		}
	}

	updated, err := s.sessions.Current(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	payload, err := buildSessionAgentsPayload(updated.Workspace.Path, updated.Workspace.Settings)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (s *Server) handleAdminSessionSettingsUpdate(c *gin.Context) {
	ref, ok := s.authorizeSessionRef(c)
	if !ok {
		return
	}
	var body struct {
		Settings workspace.Settings `json:"settings"`
	}
	if !bindJSON(c, &body) {
		return
	}
	current, err := s.sessions.Current(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	if strings.TrimSpace(body.Settings.Agent.AgentsMode) == "" {
		body.Settings.Agent.AgentsMode = current.Workspace.Settings.Agent.AgentsMode
	}
	if body.Settings.Agent.OpencodeConfig == nil {
		body.Settings.Agent.OpencodeConfig = current.Workspace.Settings.Agent.OpencodeConfig
	}
	if err := workspace.SaveSettings(current.Workspace.Path, body.Settings); err != nil {
		writeError(c, err)
		return
	}
	updated, err := s.sessions.RebuildWorkspace(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"provider":       ref.Provider,
		"conversationId": ref.ConversationID,
		"workspacePath":  updated.Path,
		"settings":       updated.Settings,
		"rebuilt":        true,
	})
}

func (s *Server) handleAdminSessionTokenRotate(c *gin.Context) {
	ref, ok := s.authorizeSessionRef(c)
	if !ok {
		return
	}
	if _, err := s.sessions.Current(ref); err != nil {
		writeError(c, err)
		return
	}
	token, err := s.access.RotateSessionToken(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"sessionToken": token})
}

func (s *Server) handleAdminSessionFilesList(c *gin.Context) {
	ref, ok := s.authorizeSessionRef(c)
	if !ok {
		return
	}
	current, err := s.sessions.Current(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	kind := strings.TrimSpace(c.Param("kind"))
	items, err := listWorkspaceFiles(current.Workspace.Path, kind)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) handleAdminSessionFileGet(c *gin.Context) {
	ref, ok := s.authorizeSessionRef(c)
	if !ok {
		return
	}
	current, err := s.sessions.Current(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	kind := strings.TrimSpace(c.Param("kind"))
	relPath := strings.TrimSpace(c.Query("path"))
	content, exists, err := readWorkspaceFile(current.Workspace.Path, kind, relPath)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": relPath, "content": content, "exists": exists})
}

func (s *Server) handleAdminSessionFileUpdate(c *gin.Context) {
	ref, ok := s.authorizeSessionRef(c)
	if !ok {
		return
	}
	current, err := s.sessions.Current(ref)
	if err != nil {
		writeError(c, err)
		return
	}
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !bindJSON(c, &body) {
		return
	}
	if err := writeWorkspaceFile(current.Workspace.Path, strings.TrimSpace(c.Param("kind")), body.Path, body.Content); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleAdminSkillList(c *gin.Context) {
	items, err := listSkillSummaries(s.cfg.SkillRootDir)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) handleAdminSkillUpload(c *gin.Context) {
	scope, ok := adminScopeFromContext(c)
	if !ok {
		writeStatusError(c, http.StatusUnauthorized, fmt.Errorf("missing auth scope"))
		return
	}
	if scope.Kind != accesstoken.ScopeProject {
		writeStatusError(c, http.StatusForbidden, fmt.Errorf("session token cannot upload public skills"))
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		writeError(c, err)
		return
	}
	uploaded, err := fileHeader.Open()
	if err != nil {
		writeError(c, err)
		return
	}
	defer uploaded.Close()
	zipReader, err := zip.NewReader(uploaded, fileHeader.Size)
	if err != nil {
		writeError(c, err)
		return
	}
	skillID, err := inferSkillID(zipReader)
	if err != nil {
		writeError(c, err)
		return
	}
	if err := extractSkillZip(s.cfg.SkillRootDir, skillID, zipReader); err != nil {
		writeError(c, err)
		return
	}
	items, err := listSkillSummaries(s.cfg.SkillRootDir)
	if err != nil {
		writeError(c, err)
		return
	}
	for _, item := range items {
		if item["id"] == skillID {
			c.JSON(http.StatusOK, item)
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"id": skillID})
}

func (s *Server) handleAdminRepoList(c *gin.Context) {
	items, err := listRepoSummaries(s.cfg.RepoRootDir)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func listRepoSummaries(repoRoot string) ([]gin.H, error) {
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return []gin.H{}, nil
		}
		return nil, err
	}
	items := make([]gin.H, 0, len(entries))
	for _, entry := range entries {
		id := entry.Name()
		if strings.HasPrefix(id, ".") {
			continue
		}
		// Follow symlinks (os.Stat) instead of entry.IsDir() so repos registered
		// as symlinks pointing into a source tree (e.g. agents/repos/<id> ->
		// /Users/.../<id>) are listed too; dangling/non-dir entries are skipped.
		fi, statErr := os.Stat(filepath.Join(repoRoot, id))
		if statErr != nil || !fi.IsDir() {
			continue
		}
		gitPath := filepath.Join(repoRoot, id, ".git")
		gitInfo, gitErr := os.Stat(gitPath)
		hasGit := gitErr == nil
		branch := ""
		if hasGit && gitInfo.IsDir() {
			branch = readGitBranch(gitPath)
		}
		items = append(items, gin.H{"id": id, "branch": branch, "hasGit": hasGit})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i]["id"].(string) < items[j]["id"].(string)
	})
	return items, nil
}

func (s *Server) handleAdminRepoClone(c *gin.Context) {
	scope, ok := adminScopeFromContext(c)
	if !ok {
		writeStatusError(c, http.StatusUnauthorized, fmt.Errorf("missing auth scope"))
		return
	}
	if scope.Kind != accesstoken.ScopeProject {
		writeStatusError(c, http.StatusForbidden, fmt.Errorf("session token cannot clone repos"))
		return
	}
	var body struct {
		URL string `json:"url"`
		ID  string `json:"id"`
	}
	if !bindJSON(c, &body) {
		return
	}
	url := strings.TrimSpace(body.URL)
	if err := validateRepoCloneURL(url); err != nil {
		writeStatusError(c, http.StatusBadRequest, err)
		return
	}
	id := strings.TrimSpace(body.ID)
	if id == "" {
		id = inferRepoIDFromURL(url)
	}
	if !isSafeSkillID(id) {
		writeStatusError(c, http.StatusBadRequest, fmt.Errorf("invalid repo id %q; use letters, digits, '-', '_', '.'", id))
		return
	}
	if workspace.IsReservedRepoMountID(id) {
		writeStatusError(c, http.StatusBadRequest, fmt.Errorf("repo id %q is reserved", id))
		return
	}
	branch, err := cloneRepo(c.Request.Context(), s.cfg.RepoRootDir, id, url)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "branch": branch, "hasGit": true})
}

// requireProjectScope gates repo-mutating endpoints (clone/pull/checkout) to the
// project token; session tokens are scoped to a single conversation and must not
// be able to mutate shared repos.
func (s *Server) requireProjectScope(c *gin.Context, action string) bool {
	scope, ok := adminScopeFromContext(c)
	if !ok {
		writeStatusError(c, http.StatusUnauthorized, fmt.Errorf("missing auth scope"))
		return false
	}
	if scope.Kind != accesstoken.ScopeProject {
		writeStatusError(c, http.StatusForbidden, fmt.Errorf("session token cannot %s repos", action))
		return false
	}
	return true
}

func (s *Server) handleAdminRepoBranches(c *gin.Context) {
	id := strings.TrimSpace(c.Param("repoId"))
	repoDir, status, err := s.resolveRepoDir(id)
	if err != nil {
		writeStatusError(c, status, err)
		return
	}
	current, branches, err := gitBranchList(c.Request.Context(), repoDir)
	if err != nil {
		writeStatusError(c, http.StatusBadGateway, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "current": current, "branches": branches})
}

func (s *Server) handleAdminRepoPull(c *gin.Context) {
	if !s.requireProjectScope(c, "pull") {
		return
	}
	id := strings.TrimSpace(c.Param("repoId"))
	repoDir, status, err := s.resolveRepoDir(id)
	if err != nil {
		writeStatusError(c, status, err)
		return
	}
	out, err := runGitInRepo(c.Request.Context(), repoDir, 5*time.Minute, "pull", "--ff-only")
	if err != nil {
		writeStatusError(c, http.StatusConflict, err)
		return
	}
	branch := readGitBranch(filepath.Join(repoDir, ".git"))
	c.JSON(http.StatusOK, gin.H{"id": id, "branch": branch, "output": strings.TrimSpace(out)})
}

func (s *Server) handleAdminRepoCheckout(c *gin.Context) {
	if !s.requireProjectScope(c, "checkout") {
		return
	}
	id := strings.TrimSpace(c.Param("repoId"))
	repoDir, status, err := s.resolveRepoDir(id)
	if err != nil {
		writeStatusError(c, status, err)
		return
	}
	var body struct {
		Branch string `json:"branch"`
	}
	if !bindJSON(c, &body) {
		return
	}
	branch := strings.TrimSpace(body.Branch)
	if err := validateGitBranchName(branch); err != nil {
		writeStatusError(c, http.StatusBadRequest, err)
		return
	}
	if _, err := runGitInRepo(c.Request.Context(), repoDir, 60*time.Second, "checkout", branch); err != nil {
		writeStatusError(c, http.StatusConflict, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "branch": readGitBranch(filepath.Join(repoDir, ".git")), "hasGit": true})
}

// resolveRepoDir validates the repo id and returns the on-disk directory it maps
// to (following symlinks). It returns the HTTP status to use on failure so the
// caller can surface 400 (bad id) vs 404 (missing) consistently.
func (s *Server) resolveRepoDir(id string) (string, int, error) {
	if id == "" || !isSafeSkillID(id) {
		return "", http.StatusBadRequest, fmt.Errorf("invalid repo id %q", id)
	}
	dir := filepath.Join(s.cfg.RepoRootDir, id)
	// os.Stat follows symlinks, so symlink-registered repos resolve to their
	// real directory; a dangling symlink or missing entry becomes 404.
	fi, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", http.StatusNotFound, fmt.Errorf("repo %q not found", id)
		}
		return "", http.StatusInternalServerError, err
	}
	if !fi.IsDir() {
		return "", http.StatusBadRequest, fmt.Errorf("repo %q is not a directory", id)
	}
	return dir, http.StatusOK, nil
}

// validateGitBranchName keeps `git checkout <branch>` from being turned into
// option/argument injection. Conservative allowlist that still covers normal
// branch names like feature/foo or release-1.2; rejects leading '-', control
// chars/space (not in the charset), and git's special ref sequences.
func validateGitBranchName(name string) error {
	if name == "" {
		return fmt.Errorf("branch is required")
	}
	if len(name) > 255 {
		return fmt.Errorf("branch name is too long")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("branch name must not start with '-'")
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") {
		return fmt.Errorf("invalid branch name")
	}
	if strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "@{") {
		return fmt.Errorf("invalid branch name")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '/' || r == '.' || r == '_' || r == '-' || r == '+':
		default:
			return fmt.Errorf("branch name has invalid character %q", string(r))
		}
	}
	return nil
}

// gitCommandEnv pins a non-interactive, transport-restricted git environment for
// pull/checkout/branch listing (same hardening as cloneRepo).
func gitCommandEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=https:http:ssh:git",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes",
	)
}

// runGitInRepo runs `git -C <repoDir> <args...>` with a timeout, returning the
// combined output. git args are passed as an explicit array (no shell), so the
// only injection surface is a leading '-' arg, which callers validate.
func runGitInRepo(ctx context.Context, repoDir string, timeout time.Duration, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := append([]string{"-C", repoDir}, args...)
	cmd := exec.CommandContext(cctx, "git", full...)
	cmd.Env = gitCommandEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return string(output), fmt.Errorf("git %s timed out", args[0])
		}
		detail := lastNonEmptyLine(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return string(output), fmt.Errorf("git %s failed: %s", args[0], detail)
	}
	return string(output), nil
}

// gitBranchList returns the current branch plus the set of checkout-able short
// branch names (local heads + remote-tracking branches with their remote prefix
// stripped, HEAD pointers dropped, deduped and sorted).
func gitBranchList(ctx context.Context, repoDir string) (string, []string, error) {
	current := readGitBranch(filepath.Join(repoDir, ".git"))
	out, err := runGitInRepo(ctx, repoDir, 30*time.Second, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return current, nil, err
	}
	seen := map[string]struct{}{}
	branches := make([]string, 0, 16)
	for _, line := range strings.Split(out, "\n") {
		ref := strings.TrimSpace(line)
		var name string
		switch {
		case strings.HasPrefix(ref, "refs/heads/"):
			name = strings.TrimPrefix(ref, "refs/heads/")
		case strings.HasPrefix(ref, "refs/remotes/"):
			rest := strings.TrimPrefix(ref, "refs/remotes/")
			if idx := strings.Index(rest, "/"); idx >= 0 {
				name = rest[idx+1:]
			}
		}
		if name == "" || name == "HEAD" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		branches = append(branches, name)
	}
	sort.Strings(branches)
	return current, branches, nil
}

// validateRepoCloneURL rejects anything that could turn `git clone` into command
// execution or local-file exfiltration. Layered defense: no leading dash (option
// injection), no control chars, an explicit transport allowlist, and an explicit
// blocklist for git's command-capable transports (ext::, file://, fd::). The
// clone itself additionally pins GIT_ALLOW_PROTOCOL.
func validateRepoCloneURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("repo url is required")
	}
	if len(raw) > 2048 {
		return fmt.Errorf("repo url is too long")
	}
	if strings.HasPrefix(raw, "-") {
		return fmt.Errorf("repo url must not start with '-'")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("repo url contains control characters")
		}
	}
	lower := strings.ToLower(raw)
	for _, bad := range []string{"ext::", "ext:", "file://", "fd::", "file:"} {
		if strings.HasPrefix(lower, bad) {
			return fmt.Errorf("unsupported repo url transport")
		}
	}
	switch {
	case strings.HasPrefix(lower, "https://"),
		strings.HasPrefix(lower, "http://"),
		strings.HasPrefix(lower, "ssh://"),
		strings.HasPrefix(lower, "git://"):
		return nil
	}
	if isSCPLikeRepoURL(raw) {
		return nil
	}
	return fmt.Errorf("unsupported repo url; use https://, http://, ssh://, git:// or user@host:path")
}

// isSCPLikeRepoURL matches the scp-like git form user@host:path. It rejects
// whitespace and anything containing a scheme separator, so transports like
// "ext::sh -c ..." can never be mistaken for scp-like.
func isSCPLikeRepoURL(raw string) bool {
	if strings.ContainsAny(raw, " \t\r\n") {
		return false
	}
	if strings.Contains(raw, "://") {
		return false
	}
	at := strings.Index(raw, "@")
	colon := strings.Index(raw, ":")
	if at <= 0 || colon <= at+1 || colon+1 >= len(raw) {
		return false
	}
	return true
}

func inferRepoIDFromURL(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	trimmed = strings.TrimSuffix(trimmed, ".git")
	if idx := strings.LastIndexAny(trimmed, "/:"); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	return strings.TrimSpace(trimmed)
}

// cloneRepo clones url into repoRoot/id atomically (clone into a temp dir, then
// rename into place) so a half-finished clone never shows up in the repo list.
func cloneRepo(ctx context.Context, repoRoot, id, url string) (string, error) {
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(repoRoot, id)
	rel, err := filepath.Rel(repoRoot, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.ContainsRune(rel, filepath.Separator) {
		return "", fmt.Errorf("invalid repo target path")
	}
	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("repo %q already exists", id)
	} else if !os.IsNotExist(err) {
		return "", err
	}

	tempRoot, err := os.MkdirTemp(repoRoot, ".repo-clone-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tempRoot)
	tempTarget := filepath.Join(tempRoot, id)

	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	// Keep clones shallow for speed, but still fetch all branch tips so the repo
	// management UI can continue listing and checking out non-default branches.
	cmd := exec.CommandContext(cloneCtx, "git", "clone", "--depth=1", "--no-single-branch", "--", url, tempTarget)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=https:http:ssh:git",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if cloneCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("git clone timed out after 5m")
		}
		detail := lastNonEmptyLine(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("git clone failed: %s", detail)
	}
	if err := os.Rename(tempTarget, target); err != nil {
		return "", err
	}
	return readGitBranch(filepath.Join(target, ".git")), nil
}

func lastNonEmptyLine(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

func readGitBranch(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(data))
	const prefix = "ref: refs/heads/"
	if strings.HasPrefix(head, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(head, prefix))
	}
	if len(head) >= 7 {
		return head[:7]
	}
	return ""
}

func (s *Server) authorizeSessionRef(c *gin.Context) (conversation.Ref, bool) {
	scope, ok := adminScopeFromContext(c)
	if !ok {
		writeStatusError(c, http.StatusUnauthorized, fmt.Errorf("missing auth scope"))
		return conversation.Ref{}, false
	}
	ref := conversation.Ref{Provider: strings.TrimSpace(c.Param("provider")), ConversationID: strings.TrimSpace(c.Param("conversationId"))}
	if ref.Provider == "" || ref.ConversationID == "" {
		writeStatusError(c, http.StatusBadRequest, fmt.Errorf("provider and conversationId are required"))
		return conversation.Ref{}, false
	}
	if !scopeAllowsRef(scope, ref) {
		writeStatusError(c, http.StatusForbidden, fmt.Errorf("session token can only access its own session"))
		return conversation.Ref{}, false
	}
	return ref, true
}

func scopeAllowsRef(scope *accesstoken.Scope, ref conversation.Ref) bool {
	if scope == nil {
		return false
	}
	if scope.Kind != accesstoken.ScopeSession {
		return true
	}
	return scope.Ref.Provider == ref.Provider && scope.Ref.ConversationID == ref.ConversationID
}

func buildSessionAgentsPayload(workspacePath string, settings workspace.Settings) (gin.H, error) {
	agentsPath := filepath.Join(workspacePath, workspace.AgentsFileName)
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		return nil, err
	}
	resolvedPath := agentsPath
	if resolved, err := filepath.EvalSymlinks(agentsPath); err == nil && strings.TrimSpace(resolved) != "" {
		resolvedPath = resolved
	}
	mode := workspace.NormalizeAgentsMode(settings.Agent.AgentsMode)
	return gin.H{
		"path":         workspace.AgentsFileName,
		"resolvedPath": resolvedPath,
		"content":      string(content),
		"mode":         mode,
		"readOnly":     mode != workspace.AgentsModeCustom,
	}, nil
}

func (s *Server) resolveDisplayNamesBatch(ctx context.Context, refs []conversation.Ref) []adminSessionSummary {
	if len(refs) == 0 {
		return []adminSessionSummary{}
	}
	results := make([]adminSessionSummary, len(refs))
	var wait sync.WaitGroup
	sema := make(chan struct{}, 6)
	for index, ref := range refs {
		index := index
		ref := ref
		wait.Add(1)
		go func() {
			defer wait.Done()
			sema <- struct{}{}
			defer func() { <-sema }()
			lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			displayInfo, _ := s.resolveSessionDisplayInfo(lookupCtx, ref)
			results[index] = adminSessionSummary{
				Summary: session.Summary{
					Provider:       ref.Provider,
					ConversationID: ref.ConversationID,
				},
				DisplayName: strings.TrimSpace(displayInfo.DisplayName),
				ChatMode:    strings.TrimSpace(displayInfo.ChatMode),
			}
		}()
	}
	wait.Wait()
	return results
}

func (s *Server) resolveSessionDisplayInfo(ctx context.Context, ref conversation.Ref) (providerapi.ChatDisplayInfo, error) {
	if s.providers == nil {
		return providerapi.ChatDisplayInfo{}, nil
	}
	client, err := s.providers(s.cfg, ref.Provider)
	if err != nil {
		return providerapi.ChatDisplayInfo{}, err
	}
	resolver, ok := client.(providerapi.ChatDisplayInfoResolver)
	if !ok {
		return providerapi.ChatDisplayInfo{}, nil
	}
	info, err := resolver.GetChatDisplayInfo(ctx, ref.ConversationID)
	if err != nil {
		return providerapi.ChatDisplayInfo{}, err
	}
	info.DisplayName = strings.TrimSpace(info.DisplayName)
	info.ChatMode = strings.TrimSpace(info.ChatMode)
	return info, nil
}

func adminScopeFromContext(c *gin.Context) (*accesstoken.Scope, bool) {
	value, ok := c.Get(adminScopeKey)
	if !ok {
		return nil, false
	}
	scope, ok := value.(*accesstoken.Scope)
	return scope, ok
}

func writeStatusError(c *gin.Context, status int, err error) {
	c.JSON(status, gin.H{"error": err.Error()})
}

func listWorkspaceFiles(workspacePath, kind string) ([]adminFileItem, error) {
	root, err := workspaceFileRoot(workspacePath, kind)
	if err != nil {
		return nil, err
	}
	if kind == "hooks" {
		items := make([]adminFileItem, 0, len(allowedHookFiles))
		for _, path := range []string{"before_message.py", "on_reaction.py", "after_reply.py"} {
			item := adminFileItem{Path: path}
			info, err := os.Stat(filepath.Join(root, path))
			if err == nil {
				item.Exists = true
				item.Size = info.Size()
				item.UpdatedAt = info.ModTime().UTC()
			} else if !os.IsNotExist(err) {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	}

	entries := []adminFileItem{}
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, adminFileItem{
			Path:      filepath.ToSlash(rel),
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

func readWorkspaceFile(workspacePath, kind, relPath string) (string, bool, error) {
	resolved, err := resolveWorkspaceFile(workspacePath, kind, relPath)
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(resolved)
	if os.IsNotExist(err) && kind == "hooks" {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(data), true, nil
}

func writeWorkspaceFile(workspacePath, kind, relPath, content string) error {
	resolved, err := resolveWorkspaceFile(workspacePath, kind, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return err
	}
	return os.WriteFile(resolved, []byte(content), 0o644)
}

func workspaceFileRoot(workspacePath, kind string) (string, error) {
	switch kind {
	case "memory":
		return filepath.Join(workspacePath, ".agents", "memory"), nil
	case "hooks":
		return filepath.Join(workspacePath, ".agents", "hooks"), nil
	default:
		return "", fmt.Errorf("unsupported file kind %q", kind)
	}
}

func resolveWorkspaceFile(workspacePath, kind, relPath string) (string, error) {
	root, err := workspaceFileRoot(workspacePath, kind)
	if err != nil {
		return "", err
	}
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", fmt.Errorf("path is required")
	}
	if kind == "hooks" {
		relPath = filepath.ToSlash(filepath.Clean(relPath))
		if _, ok := allowedHookFiles[relPath]; !ok {
			return "", fmt.Errorf("unsupported hook file %q", relPath)
		}
	}
	cleaned := filepath.Clean(relPath)
	if filepath.IsAbs(cleaned) || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid relative path %q", relPath)
	}
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

func listSkillSummaries(skillRoot string) ([]gin.H, error) {
	entries, err := os.ReadDir(skillRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return []gin.H{}, nil
		}
		return nil, err
	}
	items := make([]gin.H, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		skillFile := filepath.Join(skillRoot, id, "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		title := id
		if parsed := firstMarkdownHeading(string(data)); parsed != "" {
			title = parsed
		}
		items = append(items, gin.H{"id": id, "title": title, "hasSkillFile": true})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i]["id"].(string) < items[j]["id"].(string)
	})
	return items, nil
}

func firstMarkdownHeading(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
	}
	return ""
}

func inferSkillID(reader *zip.Reader) (string, error) {
	roots := map[string]struct{}{}
	hasSkillFile := false
	for _, file := range reader.File {
		name := strings.TrimSpace(file.Name)
		if name == "" {
			continue
		}
		name = strings.TrimPrefix(name, "./")
		parts := strings.Split(name, "/")
		root := strings.TrimSpace(parts[0])
		if root == "" || root == "__MACOSX" {
			continue
		}
		if !isSafeSkillID(root) {
			return "", fmt.Errorf("invalid skill id %q from zip root", root)
		}
		roots[root] = struct{}{}
		if name == root+"/SKILL.md" || name == root+"/SKILL.MD" {
			hasSkillFile = true
		}
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("zip does not contain a skill root directory")
	}
	if len(roots) > 1 {
		return "", fmt.Errorf("zip must contain exactly one top-level skill directory")
	}
	if !hasSkillFile {
		return "", fmt.Errorf("zip root must contain SKILL.md")
	}
	for root := range roots {
		return root, nil
	}
	return "", fmt.Errorf("zip does not contain a skill root directory")
}

func extractSkillZip(skillRoot, skillID string, reader *zip.Reader) error {
	targetDir := filepath.Join(skillRoot, skillID)
	if _, err := os.Stat(targetDir); err == nil {
		return fmt.Errorf("skill %q already exists", skillID)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		return err
	}
	tempRoot, err := os.MkdirTemp(skillRoot, ".skill-upload-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempRoot)
	tempSkillDir := filepath.Join(tempRoot, skillID)
	if err := os.MkdirAll(tempSkillDir, 0o755); err != nil {
		return err
	}
	for _, file := range reader.File {
		name := strings.TrimPrefix(strings.TrimSpace(file.Name), "./")
		if name == "" {
			continue
		}
		parts := strings.Split(name, "/")
		if len(parts) == 0 || parts[0] != skillID {
			continue
		}
		rel := strings.TrimPrefix(name, skillID)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			continue
		}
		resolved, err := resolveZipExtractPath(tempSkillDir, rel)
		if err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(resolved, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			return err
		}
		fileReader, err := file.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(resolved, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, file.Mode())
		if err != nil {
			fileReader.Close()
			return err
		}
		if _, err := io.Copy(out, fileReader); err != nil {
			_ = out.Close()
			fileReader.Close()
			return err
		}
		if err := out.Close(); err != nil {
			fileReader.Close()
			return err
		}
		if err := fileReader.Close(); err != nil {
			return err
		}
	}
	return os.Rename(tempSkillDir, targetDir)
}

func resolveZipExtractPath(root, rel string) (string, error) {
	cleaned := filepath.Clean(rel)
	if cleaned == "." || cleaned == ".." || filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid zip path %q", rel)
	}
	resolved := filepath.Join(root, cleaned)
	finalRel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if finalRel == ".." || strings.HasPrefix(finalRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("zip path escapes root")
	}
	return resolved, nil
}

func isSafeSkillID(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}
