package workspace

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func copyDir(srcDir, dstDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, dstPath); err != nil {
				return err
			}
			continue
		}

		if err := copyFile(srcPath, dstPath, info.Mode().Perm()); err != nil {
			return err
		}
	}

	return nil
}

// copyDirOverlay copies srcDir into dstDir in place: it merges into existing
// directories and overwrites existing files instead of requiring an empty
// destination. Entries whose path relative to srcDir is present in skipRel are
// preserved when they already exist in the destination (this is how an in-place
// rebuild protects the session-owned state dirs memory/hooks/session-skills/
// runtime), but are still seeded from the template when entirely absent — which
// matches the legacy destroy-and-recopy that re-created a missing hooks dir.
//
// Before writing a file or symlink it removes any existing destination entry
// (without following it), so a fresh regular file can replace a stale symlink —
// and we never accidentally truncate a symlink's target.
func copyDirOverlay(srcDir, dstDir string, skipRel map[string]struct{}) error {
	return copyDirOverlayRel(srcDir, dstDir, "", skipRel)
}

func copyDirOverlayRel(srcDir, dstDir, rel string, skipRel map[string]struct{}) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}

	for _, entry := range entries {
		entryRel := filepath.Join(rel, entry.Name())
		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())

		if _, preserve := skipRel[entryRel]; preserve {
			// Keep the session's own copy when present; only seed from the
			// template when the whole entry is missing.
			if _, err := os.Lstat(dstPath); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}

		if entry.IsDir() {
			if err := copyDirOverlayRel(srcPath, dstPath, entryRel, skipRel); err != nil {
				return err
			}
			continue
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		if err := os.Remove(dstPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, dstPath); err != nil {
				return err
			}
			continue
		}

		if err := copyFile(srcPath, dstPath, info.Mode().Perm()); err != nil {
			return err
		}
	}

	return nil
}

func copyFile(srcPath, dstPath string, perm os.FileMode) error {
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

func reconcileSkillLinks(skillRootDir, sessionSkillRootDir, workspaceDir string, skillIDs []string) error {
	targetRoot := filepath.Join(workspaceDir, ".agents", "skills")
	if err := os.RemoveAll(targetRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return err
	}

	for _, skillID := range uniqueStrings(skillIDs) {
		if !isSafeMountID(skillID) {
			return fmt.Errorf("invalid skill id %q", skillID)
		}
		sourceDir := filepath.Join(skillRootDir, skillID)
		if _, err := os.Stat(sourceDir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}

		if err := os.Symlink(sourceDir, filepath.Join(targetRoot, skillID)); err != nil {
			return err
		}
	}

	entries, err := os.ReadDir(sessionSkillRootDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillID := entry.Name()
		if !isSafeMountID(skillID) {
			continue
		}
		linkPath := filepath.Join(targetRoot, skillID)
		_ = os.RemoveAll(linkPath)
		if err := os.Symlink(filepath.Join(sessionSkillRootDir, skillID), linkPath); err != nil {
			return err
		}
	}

	return nil
}

func reconcileSubagentLinks(subagentRootDir, legacySubagentRootDir, workspaceDir string, subagentIDs []string) error {
	targetRoot := filepath.Join(workspaceDir, ".agents", "agents")
	if err := os.RemoveAll(targetRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return err
	}

	for _, subagentID := range uniqueStrings(subagentIDs) {
		if !isSafeMountID(subagentID) {
			return fmt.Errorf("invalid subagent id %q", subagentID)
		}
		sourcePath, err := resolveSubagentSourcePath(subagentRootDir, legacySubagentRootDir, subagentID)
		if err != nil {
			return err
		}
		if err := os.Symlink(sourcePath, filepath.Join(targetRoot, filepath.Base(sourcePath))); err != nil {
			return err
		}
	}

	return nil
}

func resolveSubagentSourcePath(subagentRootDir, legacySubagentRootDir, subagentID string) (string, error) {
	for _, root := range subagentSourceRoots(subagentRootDir, legacySubagentRootDir) {
		sourcePath := filepath.Join(root, subagentID+".md")
		if _, err := os.Stat(sourcePath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		return sourcePath, nil
	}
	return "", fmt.Errorf("subagent %q not found: %w", subagentID, os.ErrNotExist)
}

func subagentSourceRoots(subagentRootDir, legacySubagentRootDir string) []string {
	roots := []string{}
	seen := map[string]struct{}{}
	for _, root := range []string{subagentRootDir, legacySubagentRootDir} {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	return roots
}

// reservedRootLinkNames are workspace-root entries that repo links must never
// clobber. Repo links live at the workspace root next to these, so a repo id
// colliding with one of them is skipped rather than overwriting platform files.
var reservedRootLinkNames = map[string]struct{}{
	AgentsFileName:   {},
	"CLAUDE.md":      {},
	"opencode.json":  {},
	SettingsFileName: {},
	".agents":        {},
	".opencode":      {},
	".git":           {},
}

// IsReservedRepoMountID reports whether a repo id collides with a workspace-root
// entry that repo mounting must never clobber. Shared by reconcile (skip) and the
// clone endpoint (reject up front) so both agree on the same reserved set.
func IsReservedRepoMountID(id string) bool {
	_, ok := reservedRootLinkNames[id]
	return ok
}

// reconcileRepoLinks mounts shared git repos into the workspace root as symlinks
// pointing into repoRootDir. Unlike skills/subagents (which own a dedicated
// directory we can wipe), repo links share the workspace root with template and
// runtime files, so we only remove our own stale links (symlinks resolving into
// repoRootDir) and never touch other entries or the real repo data.
func reconcileRepoLinks(repoRootDir, workspaceDir string, repoIDs []string) error {
	cleanRepoRoot := filepath.Clean(repoRootDir)

	entries, err := os.ReadDir(workspaceDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		linkPath := filepath.Join(workspaceDir, entry.Name())
		info, err := os.Lstat(linkPath)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(linkPath)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(workspaceDir, target)
		}
		if pathWithin(filepath.Clean(target), cleanRepoRoot) {
			if err := os.Remove(linkPath); err != nil {
				return err
			}
		}
	}

	for _, repoID := range uniqueStrings(repoIDs) {
		if !isSafeMountID(repoID) {
			return fmt.Errorf("invalid repo id %q", repoID)
		}
		if _, reserved := reservedRootLinkNames[repoID]; reserved {
			continue
		}
		sourceDir := filepath.Join(cleanRepoRoot, repoID)
		if _, err := os.Stat(sourceDir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		linkPath := filepath.Join(workspaceDir, repoID)
		if _, err := os.Lstat(linkPath); err == nil {
			// Something already occupies this name (template/runtime file or a
			// non-managed symlink); never clobber it.
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Symlink(sourceDir, linkPath); err != nil {
			return err
		}
	}

	return nil
}

func pathWithin(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isSafeMountID(value string) bool {
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

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
