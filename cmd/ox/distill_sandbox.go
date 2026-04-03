package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/ox/internal/config"
)


// distillSandbox holds the isolated environment for a sandbox distill run.
// All git operations (commit, push) target the sandbox clones, leaving the
// real team context and ledger repos untouched.
type distillSandbox struct {
	// Root is the temp directory containing all sandbox state.
	Root string

	// TC is the sandboxed team context (clone + local bare remote).
	TC *config.TeamContext

	// Repos overrides resolveDistillRepos — each ledger is cloned with
	// its own bare remote so push succeeds without hitting the real remote.
	Repos []distillRepo
}

// createDistillSandbox builds an isolated distill environment:
//
//  1. git clone --local the real team context to a temp dir
//  2. git clone --local each ledger to temp dirs
//  3. Create local bare repos as push targets for each clone
//  4. Swap remotes so push goes to the bare repos, not the real remotes
//  5. Optionally override guidance files (EXTRACT.md, DISTILL.md)
//
// The caller must call cleanup() when done.
func createDistillSandbox(
	realTC *config.TeamContext,
	realRepos []distillRepo,
	extractOverride, distillOverride string,
) (*distillSandbox, func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	root, err := os.MkdirTemp("", "ox-distill-sandbox-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create sandbox dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(root) }

	// --- sandbox team context ---
	sandboxTCPath := filepath.Join(root, "team-context")
	if err := localCloneWithBareRemote(ctx, realTC.Path, sandboxTCPath, filepath.Join(root, "remote-tc.git")); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("sandbox team context: %w", err)
	}

	sandboxTC := &config.TeamContext{
		TeamID:   realTC.TeamID,
		TeamName: realTC.TeamName,
		Path:     sandboxTCPath,
	}

	// --- apply guidance overrides ---
	if extractOverride != "" {
		if err := overrideGuidance(sandboxTCPath, "EXTRACT.md", extractOverride); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("override EXTRACT.md: %w", err)
		}
	}
	if distillOverride != "" {
		if err := overrideGuidance(sandboxTCPath, "DISTILL.md", distillOverride); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("override DISTILL.md: %w", err)
		}
	}

	// --- sandbox ledgers ---
	sandboxRepos := make([]distillRepo, 0, len(realRepos))
	for i, repo := range realRepos {
		if repo.LedgerPath == "" {
			// no ledger for this repo — keep entry with empty path
			sandboxRepos = append(sandboxRepos, repo)
			continue
		}

		sandboxLedger := filepath.Join(root, fmt.Sprintf("ledger-%d", i))
		bareRemote := filepath.Join(root, fmt.Sprintf("remote-ledger-%d.git", i))
		if err := localCloneWithBareRemote(ctx, repo.LedgerPath, sandboxLedger, bareRemote); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("sandbox ledger %s: %w", repo.RepoID, err)
		}

		sandboxRepos = append(sandboxRepos, distillRepo{
			ProjectRoot: repo.ProjectRoot,
			RepoID:      repo.RepoID,
			LedgerPath:  sandboxLedger,
			CodeDBDir:   repo.CodeDBDir, // read-only, shared with real repo
		})
	}

	sb := &distillSandbox{
		Root:  root,
		TC:    sandboxTC,
		Repos: sandboxRepos,
	}
	return sb, cleanup, nil
}

// localCloneWithBareRemote creates a hard-linked clone of srcRepo and points
// its origin at a new local bare repo so push works without network access.
//
// The source repo may be a partial clone (--filter=blob:none) with sparse
// checkout. In that case, the git index contains entries for files outside
// the sparse cone whose blob objects may only exist on the remote (promisor
// objects). Since the sandbox remote is a local bare repo with no promisor
// relationship, `git commit` would fail with "invalid object" when building
// trees that reference these missing blobs.
//
// Fix: after checkout, remove index entries for files not in the working
// tree (i.e., outside the sparse cone). This only affects the sandbox clone.
func localCloneWithBareRemote(ctx context.Context, srcRepo, cloneDir, bareDir string) error {
	// step 1: local clone without checkout (hardlinks pack files, near-instant)
	cmd := exec.CommandContext(ctx, "git", "clone", "--local", "--no-checkout", srcRepo, cloneDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone --local: %s: %w", string(out), err)
	}

	// step 2: immediately sever the link to the source repo so no
	// subsequent git operation can accidentally reach it.
	cmd = exec.CommandContext(ctx, "git", "-C", cloneDir, "remote", "remove", "origin")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remove origin: %s: %w", string(out), err)
	}

	// step 3: copy sparse-checkout config from source if present
	srcSparse := filepath.Join(srcRepo, ".git", "info", "sparse-checkout")
	if data, err := os.ReadFile(srcSparse); err == nil {
		destInfo := filepath.Join(cloneDir, ".git", "info")
		if err := os.MkdirAll(destInfo, 0o755); err != nil {
			return fmt.Errorf("create .git/info: %w", err)
		}
		if err := os.WriteFile(filepath.Join(destInfo, "sparse-checkout"), data, 0o644); err != nil {
			return fmt.Errorf("write sparse-checkout: %w", err)
		}
		cfgCmd := exec.CommandContext(ctx, "git", "-C", cloneDir, "config", "core.sparseCheckout", "true")
		if out, err := cfgCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("enable sparse checkout: %s: %w", string(out), err)
		}
	}

	// step 4: checkout (respects sparse-checkout rules)
	cmd = exec.CommandContext(ctx, "git", "-C", cloneDir, "checkout")
	if out, err := cmd.CombinedOutput(); err != nil {
		// checkout may warn about missing promisor objects but still succeed
		slog.Debug("checkout warnings", "output", string(out))
	}

	// step 5: remove index entries for files not in the working tree.
	//
	// In a partial clone with sparse checkout, the index has entries for ALL
	// tracked files, but blobs outside the sparse cone may only exist on the
	// remote (promisor packs). The sandbox bare remote has no promisor
	// relationship, so git commit fails with "invalid object" for these
	// entries. Removing them from the index is safe because the files aren't
	// in the working tree anyway.
	rmCmd := exec.CommandContext(ctx, "git", "-C", cloneDir, "ls-files", "--sparse", "--full-name", "-z")
	lsOut, err := rmCmd.Output()
	if err == nil {
		var toRemove []string
		for _, entry := range strings.Split(string(lsOut), "\x00") {
			if entry == "" {
				continue
			}
			if _, statErr := os.Lstat(filepath.Join(cloneDir, entry)); os.IsNotExist(statErr) {
				toRemove = append(toRemove, entry)
			}
		}
		if len(toRemove) > 0 {
			args := append([]string{"-C", cloneDir, "rm", "--cached", "--sparse", "--quiet", "--"}, toRemove...)
			rmCached := exec.CommandContext(ctx, "git", args...)
			if out, err := rmCached.CombinedOutput(); err != nil {
				slog.Warn("failed to remove non-sparse index entries — git commit may fail with 'invalid object'", "error", string(out))
			}
		}
	}

	// step 6: create bare repo as the sandbox push target.
	initCmd := exec.CommandContext(ctx, "git", "init", "--bare", bareDir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init --bare: %s: %w", string(out), err)
	}

	// step 7: add the bare repo as the new origin
	cmd = exec.CommandContext(ctx, "git", "-C", cloneDir, "remote", "add", "origin", bareDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git remote add origin: %s: %w", string(out), err)
	}

	// step 8: commit the index cleanup and tag the pre-distill state.
	// The commit records the clean index so subsequent distill commits
	// don't reference missing promisor objects. The tag marks the baseline
	// for printSandboxResults to diff against.
	commitCmd := exec.CommandContext(ctx, "git", "-C", cloneDir, "commit", "--allow-empty", "-m", "sandbox: clean index for distill")
	if out, err := commitCmd.CombinedOutput(); err != nil {
		slog.Debug("sandbox index commit", "output", string(out))
	}
	tagCmd := exec.CommandContext(ctx, "git", "-C", cloneDir, "tag", "sandbox-baseline")
	if out, err := tagCmd.CombinedOutput(); err != nil {
		slog.Debug("sandbox baseline tag", "output", string(out))
	}

	// set git identity and push defaults for commits
	for _, kv := range [][2]string{
		{"user.name", "ox-sandbox"},
		{"user.email", "sandbox@ox.local"},
		{"push.autoSetupRemote", "true"},
	} {
		cmd = exec.CommandContext(ctx, "git", "-C", cloneDir, "config", kv[0], kv[1])
		if out, err := cmd.CombinedOutput(); err != nil {
			slog.Debug("git config in sandbox", "key", kv[0], "error", string(out), "err", err)
		}
	}

	return nil
}

// overrideGuidance replaces a guidance file in the sandbox team context.
// sourcePath is the path to the override file on disk.
func overrideGuidance(tcPath, filename, sourcePath string) error {
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read override file %s: %w", sourcePath, err)
	}

	guidanceDir := filepath.Join(tcPath, "memory", "guidance")
	if err := os.MkdirAll(guidanceDir, 0o755); err != nil {
		return fmt.Errorf("create guidance dir: %w", err)
	}

	dest := filepath.Join(guidanceDir, filename)
	if err := os.WriteFile(dest, content, 0o644); err != nil {
		return fmt.Errorf("write guidance file: %w", err)
	}

	return nil
}

// printSandboxResults shows a colored diff of everything the sandbox
// distill run produced. Compares HEAD against the sandbox-baseline tag
// (created before distill ran).
func printSandboxResults(sandboxTCPath string) {
	base := "sandbox-baseline"

	// stat summary first
	statCmd := exec.Command("git", "-C", sandboxTCPath, "diff", "--stat", base+"..HEAD")
	statOut, err := statCmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(statOut)) == "" {
		fmt.Println("\nSandbox: no new files generated.")
		return
	}

	fmt.Println("\n── Sandbox results ──")
	fmt.Println(strings.TrimSpace(string(statOut)))

	// full colored diff — git produces ANSI colors with --color=always
	diffCmd := exec.Command("git", "-C", sandboxTCPath,
		"diff", "--color=always", base+"..HEAD",
		"--", "memory/daily", "memory/weekly", "memory/monthly",
		"memory/.discussion-facts", "memory/.github-facts", "memory/.session-facts",
	)
	diffOut, err := diffCmd.CombinedOutput()
	if err == nil && len(diffOut) > 0 {
		fmt.Println()
		fmt.Print(string(diffOut))
	}

	fmt.Printf("\nSandbox directory: %s\n", sandboxTCPath)
}
