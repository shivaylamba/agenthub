package gitrepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnbundleWithRelativeBareRepoPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir to temp dir: %v", err)
	}
	defer func() {
		_ = os.Chdir(oldWD)
	}()

	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	runGit(t, worktree, "init")
	runGit(t, worktree, "config", "user.name", "Test Bot")
	runGit(t, worktree, "config", "user.email", "test-bot@example.com")

	filePath := filepath.Join(worktree, "demo.txt")
	if err := os.WriteFile(filePath, []byte("hello from bundle test\n"), 0o644); err != nil {
		t.Fatalf("write demo file: %v", err)
	}
	runGit(t, worktree, "add", "demo.txt")
	runGit(t, worktree, "commit", "-m", "test bundle import")

	headHash := strings.TrimSpace(runGitOutput(t, worktree, "rev-parse", "HEAD"))
	bundlePath := filepath.Join(root, "push.bundle")
	runGit(t, worktree, "bundle", "create", bundlePath, "HEAD")

	repo, err := Init("hub.git")
	if err != nil {
		t.Fatalf("init bare repo: %v", err)
	}

	hashes, err := repo.Unbundle(bundlePath)
	if err != nil {
		t.Fatalf("unbundle bundle into bare repo: %v", err)
	}
	if len(hashes) != 1 || hashes[0] != headHash {
		t.Fatalf("unexpected imported hashes: got %v want [%s]", hashes, headHash)
	}
	if !repo.CommitExists(headHash) {
		t.Fatalf("expected commit %s to exist in bare repo", headHash)
	}

	parents, message, err := repo.GetCommitInfo(headHash)
	if err != nil {
		t.Fatalf("get commit info: %v", err)
	}
	if len(parents) != 0 {
		t.Fatalf("expected root commit with no parents, got %v", parents)
	}
	if message != "test bundle import" {
		t.Fatalf("unexpected commit message: got %q", message)
	}
}

func TestGetCommitInfoReturnsAllMergeParents(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	runGit(t, worktree, "init")
	runGit(t, worktree, "config", "user.name", "Test Bot")
	runGit(t, worktree, "config", "user.email", "test-bot@example.com")

	rootFilePath := filepath.Join(worktree, "root.txt")
	if err := os.WriteFile(rootFilePath, []byte("root\n"), 0o644); err != nil {
		t.Fatalf("write demo file: %v", err)
	}
	runGit(t, worktree, "add", "root.txt")
	runGit(t, worktree, "commit", "-m", "root")

	runGit(t, worktree, "switch", "-c", "alpha")
	alphaFilePath := filepath.Join(worktree, "alpha.txt")
	if err := os.WriteFile(alphaFilePath, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write alpha file: %v", err)
	}
	runGit(t, worktree, "add", "alpha.txt")
	runGit(t, worktree, "commit", "-m", "alpha")
	alphaHash := strings.TrimSpace(runGitOutput(t, worktree, "rev-parse", "HEAD"))

	runGit(t, worktree, "switch", "-c", "beta", "HEAD~1")
	betaFilePath := filepath.Join(worktree, "beta.txt")
	if err := os.WriteFile(betaFilePath, []byte("beta\n"), 0o644); err != nil {
		t.Fatalf("write beta file: %v", err)
	}
	runGit(t, worktree, "add", "beta.txt")
	runGit(t, worktree, "commit", "-m", "beta")
	betaHash := strings.TrimSpace(runGitOutput(t, worktree, "rev-parse", "HEAD"))

	runGit(t, worktree, "switch", "alpha")
	runGit(t, worktree, "merge", "--no-ff", "beta", "-m", "merge alpha beta")
	mergeHash := strings.TrimSpace(runGitOutput(t, worktree, "rev-parse", "HEAD"))

	repoPath := filepath.Join(root, "hub.git")
	repo, err := Init(repoPath)
	if err != nil {
		t.Fatalf("init bare repo: %v", err)
	}
	bundlePath := filepath.Join(root, "merge.bundle")
	runGit(t, worktree, "bundle", "create", bundlePath, "HEAD")
	if _, err := repo.Unbundle(bundlePath); err != nil {
		t.Fatalf("unbundle merge bundle: %v", err)
	}

	parents, message, err := repo.GetCommitInfo(mergeHash)
	if err != nil {
		t.Fatalf("get merge commit info: %v", err)
	}
	if len(parents) != 2 || parents[0] != alphaHash || parents[1] != betaHash {
		t.Fatalf("unexpected merge parents: got %v want [%s %s]", parents, alphaHash, betaHash)
	}
	if message != "merge alpha beta" {
		t.Fatalf("unexpected merge message: got %q", message)
	}
}

func runGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func runGitOutput(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}
