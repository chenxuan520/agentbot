package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReconcileRepoLinksMountsConfiguredRepos(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	workspace := t.TempDir()

	repoDir := filepath.Join(repoRoot, "service-a")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	if err := reconcileRepoLinks(repoRoot, workspace, []string{"service-a"}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	linkPath := filepath.Join(workspace, "service-a")
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink at %s", linkPath)
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != repoDir {
		t.Fatalf("link target = %q, want %q", target, repoDir)
	}
}

func TestReconcileRepoLinksMountsSymlinkedSource(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	workspace := t.TempDir()
	external := t.TempDir()

	// agents/repos/<id> is itself a symlink into an external source tree (e.g.
	// a GOPATH checkout). reconcile must still mount it because os.Stat follows
	// the symlink, and a later removal must clean the workspace link too.
	externalRepo := filepath.Join(external, "service-a")
	if err := os.MkdirAll(filepath.Join(externalRepo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir external repo: %v", err)
	}
	repoEntry := filepath.Join(repoRoot, "service-a")
	if err := os.Symlink(externalRepo, repoEntry); err != nil {
		t.Fatalf("symlink repo entry: %v", err)
	}

	if err := reconcileRepoLinks(repoRoot, workspace, []string{"service-a"}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	linkPath := filepath.Join(workspace, "service-a")
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != repoEntry {
		t.Fatalf("link target = %q, want %q", target, repoEntry)
	}
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	if wantResolved, _ := filepath.EvalSymlinks(externalRepo); resolved != wantResolved {
		t.Fatalf("resolved = %q, want %q", resolved, wantResolved)
	}

	if err := reconcileRepoLinks(repoRoot, workspace, nil); err != nil {
		t.Fatalf("reconcile removal: %v", err)
	}
	if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
		t.Fatalf("expected symlinked-source link removed, err = %v", err)
	}
}

func TestReconcileRepoLinksSkipsMissingSource(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	workspace := t.TempDir()

	if err := reconcileRepoLinks(repoRoot, workspace, []string{"ghost"}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "ghost")); !os.IsNotExist(err) {
		t.Fatalf("expected no link for missing source, err = %v", err)
	}
}

func TestReconcileRepoLinksRemovesStaleManagedLink(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	workspace := t.TempDir()

	for _, name := range []string{"keep", "drop"} {
		if err := os.MkdirAll(filepath.Join(repoRoot, name), 0o755); err != nil {
			t.Fatalf("mkdir repo %s: %v", name, err)
		}
	}

	if err := reconcileRepoLinks(repoRoot, workspace, []string{"keep", "drop"}); err != nil {
		t.Fatalf("reconcile initial: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "drop")); err != nil {
		t.Fatalf("expected drop link before removal: %v", err)
	}

	if err := reconcileRepoLinks(repoRoot, workspace, []string{"keep"}); err != nil {
		t.Fatalf("reconcile after removal: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "drop")); !os.IsNotExist(err) {
		t.Fatalf("expected stale drop link removed, err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "keep")); err != nil {
		t.Fatalf("expected keep link retained: %v", err)
	}
}

func TestReconcileRepoLinksPreservesNonManagedEntries(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	workspace := t.TempDir()

	// A plain template file and a runtime alias symlink that points at .agents,
	// neither of which resolves into repoRoot, must survive reconcile.
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("# agents"), 0o644); err != nil {
		t.Fatalf("write agents: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".agents"), 0o755); err != nil {
		t.Fatalf("mkdir .agents: %v", err)
	}
	if err := os.Symlink(".agents", filepath.Join(workspace, ".opencode")); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "service-a"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	if err := reconcileRepoLinks(repoRoot, workspace, []string{"service-a"}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workspace, "AGENTS.md")); err != nil {
		t.Fatalf("AGENTS.md should survive: %v", err)
	}
	aliasTarget, err := os.Readlink(filepath.Join(workspace, ".opencode"))
	if err != nil || aliasTarget != ".agents" {
		t.Fatalf(".opencode alias should survive, target=%q err=%v", aliasTarget, err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "service-a")); err != nil {
		t.Fatalf("repo link should exist: %v", err)
	}
}

func TestReconcileRepoLinksDoesNotClobberReservedName(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	workspace := t.TempDir()

	originalContent := []byte(`{"original":true}`)
	if err := os.WriteFile(filepath.Join(workspace, "opencode.json"), originalContent, 0o644); err != nil {
		t.Fatalf("write opencode.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "opencode.json"), 0o755); err != nil {
		t.Fatalf("mkdir repo collision: %v", err)
	}

	if err := reconcileRepoLinks(repoRoot, workspace, []string{"opencode.json"}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	info, err := os.Lstat(filepath.Join(workspace, "opencode.json"))
	if err != nil {
		t.Fatalf("lstat opencode.json: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("reserved name must not become a symlink")
	}
	got, err := os.ReadFile(filepath.Join(workspace, "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}
	if string(got) != string(originalContent) {
		t.Fatalf("reserved file content changed: %q", string(got))
	}
}

func TestReconcileRepoLinksRejectsInvalidID(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	workspace := t.TempDir()

	if err := reconcileRepoLinks(repoRoot, workspace, []string{"../escape"}); err == nil {
		t.Fatalf("expected error for unsafe repo id")
	}
}

func TestPathWithin(t *testing.T) {
	t.Parallel()

	base := filepath.Join("/tmp", "repos")
	cases := []struct {
		path string
		want bool
	}{
		{filepath.Join(base, "a"), true},
		{base, true},
		{filepath.Join("/tmp", "other"), false},
		{filepath.Join("/tmp", "reposX"), false},
		{filepath.Join(base, "..", "evil"), false},
	}
	for _, tc := range cases {
		if got := pathWithin(filepath.Clean(tc.path), base); got != tc.want {
			t.Fatalf("pathWithin(%q, %q) = %v, want %v", tc.path, base, got, tc.want)
		}
	}
}
