package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/extensions/skills"
	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pickSkill returns a default-installed skill name, failing the test if none
// ship. Attest playbooks are opt-in, so lifecycle tests must not accidentally
// select a skill the default installer intentionally leaves absent.
func pickSkill(t *testing.T) string {
	t.Helper()
	skills, err := skills.Selected("0.8.0", nil)
	require.NoError(t, err)
	require.NotEmpty(t, skills, "at least one default skill must ship for these tests to mean anything")
	return skills[0].Name
}

// --- A. Install lifecycle / stamp placement ---

// TestHandleInstallSkills_ManifestOwnership verifies native frontmatter stays
// byte-clean while ownership moves to the project lockfile.
func TestHandleInstallSkills_ManifestOwnership(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)

	resp, err := handleInstallSkills(adapterprotocol.SkillsParams{
		RepoRoot: dir,
		Version:  "0.8.0",
	})
	require.NoError(t, err)
	assert.True(t, resp.Installed)
	assert.Contains(t, resp.FilesWritten, filepath.Join(".claude", "skills", name, skillFileName),
		"FilesWritten must be repo-relative per the adapterprotocol contract")

	data, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", name, skillFileName))
	require.NoError(t, err, "SKILL.md must exist on disk after install")

	lines := strings.Split(string(data), "\n")
	require.NotEmpty(t, lines)
	assert.Equal(t, "---", strings.TrimRight(lines[0], "\r"),
		"line 1 must be the frontmatter opener so Claude can parse name/description")

	assert.NotContains(t, string(data), agentx.StampComment(oxSkillStampPrefix),
		"new native skills use manifest ownership; inline stamps are migration-only")
	assert.FileExists(t, filepath.Join(dir, ".sageox", "skills.lock.json"))
}

// TestHandleInstallSkills_Idempotent verifies installing the same version twice
// succeeds without error (repeated primes / doctor runs must not break).
// Failure prevented: a second install errors or corrupts the first.
func TestHandleInstallSkills_Idempotent(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	r1, err := handleInstallSkills(params)
	require.NoError(t, err)
	assert.True(t, r1.Installed)

	r2, err := handleInstallSkills(params)
	require.NoError(t, err)
	assert.True(t, r2.Installed)
}

// TestHandleInstallSkills_NoOpReportsNothingWritten is the honesty contract that
// keeps `ox init` from claiming "Installed N skills" on an already-current repo.
// A first install writes every embedded skill; a second install on the same
// version writes nothing, so FilesWritten MUST be empty. init.go gates its
// "Installed N skills" vs "skills already up to date" message on
// len(FilesWritten), so a non-empty slice here would make init lie.
// Failure prevented: a no-op re-install reports files written, and `ox init`
// on a current repo falsely claims it installed skills.
func TestHandleInstallSkills_NoOpReportsNothingWritten(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	first, err := handleInstallSkills(params)
	require.NoError(t, err)
	require.True(t, first.Installed)
	require.NotEmpty(t, first.FilesWritten,
		"precondition: the first install must write every embedded skill")

	second, err := handleInstallSkills(params)
	require.NoError(t, err)
	assert.True(t, second.Installed, "a no-op re-install still succeeds")
	assert.Empty(t, second.FilesWritten,
		"a re-install of an already-current skill set must write nothing — "+
			"FilesWritten drives init's honest 'already up to date' message")
}

// TestHandleInstallSkills_OptInAttestSkillsStayOutOfDefaultInstall keeps a
// team that uses normal ox integration from receiving Attest playbooks it did
// not choose. An explicit capability install is the only path that writes them.
func TestHandleInstallSkills_OptInAttestSkillsStayOutOfDefaultInstall(t *testing.T) {
	dir := t.TempDir()
	defaultInstall, err := handleInstallSkills(adapterprotocol.SkillsParams{
		RepoRoot: dir,
		Version:  "0.8.0",
	})
	require.NoError(t, err)
	assert.NotContains(t, defaultInstall.FilesWritten,
		filepath.Join(".claude", "skills", "ox-attest-goal", skillFileName))

	optIn, err := handleInstallSkills(adapterprotocol.SkillsParams{
		RepoRoot: dir,
		Version:  "0.8.0",
		Names:    []string{"ox-attest-goal", "ox-attest-create"},
	})
	require.NoError(t, err)
	assert.Contains(t, optIn.FilesWritten,
		filepath.Join(".claude", "skills", "ox-attest-goal", skillFileName))
	assert.Contains(t, optIn.FilesWritten,
		filepath.Join(".claude", "skills", "ox-attest-create", skillFileName))
}

func TestHandleInstallSkills_RejectsUnknownOptInSkill(t *testing.T) {
	_, err := handleInstallSkills(adapterprotocol.SkillsParams{
		RepoRoot: t.TempDir(),
		Version:  "0.8.0",
		Names:    []string{"does-not-exist"},
	})
	require.EqualError(t, err, "unknown ox skill(s): [does-not-exist]")
}

// --- B. Check lifecycle ---

// TestHandleCheckSkills_FreshInstall verifies a freshly installed skill set
// reports Installed and is not stale.
// Failure prevented: doctor flags a clean install as broken (a no-op --fix loop)
// or reports a fresh repo as already installed.
func TestHandleCheckSkills_FreshInstall(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	// precondition: nothing installed yet -> not installed.
	pre, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.False(t, pre.Installed, "a fresh repo must not report skills as installed")
	assert.NotEmpty(t, pre.Missing, "all embedded skills should be reported missing")

	_, err = handleInstallSkills(params)
	require.NoError(t, err)

	post, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.True(t, post.Installed, "a clean install must report Installed")
	assert.Empty(t, post.Missing)
	assert.Empty(t, post.Stale)
}

// TestHandleCheckSkills_BodyEditedBelowStamp_ReportsStale is the core drift test:
// editing the body BELOW the stamp (leaving frontmatter and stamp line intact)
// must be detected as stale, because the stamp hash covers only the body. A
// first-line-only check would miss this entirely. --fix (reinstall) must restore.
// Failure prevented: a tampered SKILL.md drifts from the live binary forever with
// no detection, teaching the agent stale guidance.
func TestHandleCheckSkills_BodyEditedBelowStamp_ReportsStale(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	clean, err := handleCheckSkills(params)
	require.NoError(t, err)
	require.NotContains(t, clean.Stale, name, "precondition: fresh install must not be stale")

	skillPath := filepath.Join(dir, ".claude", "skills", name, skillFileName)
	orig, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	// append below the stamp — the stamp line and frontmatter stay byte-identical.
	require.NoError(t, os.WriteFile(skillPath, append(orig, []byte("\n\nhand-edited drift\n")...), 0o644))

	stale, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.Contains(t, stale.Conflicts, name, "a user-modified managed body must be reported as a conflict")
	assert.False(t, stale.Installed, "Installed must be false when a skill has drifted")

	// Reinstall preserves the edit instead of silently destroying user work.
	reinstall, err := handleInstallSkills(params)
	require.NoError(t, err)
	assert.False(t, reinstall.Installed)
	assert.NotEmpty(t, reinstall.Conflicts)
	fixed, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.Contains(t, fixed.Conflicts, name)
	assert.False(t, fixed.Installed)

	restored, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	assert.Contains(t, string(restored), "hand-edited drift", "reinstall must preserve a user edit")
}

// TestHandleCheckSkills_FrontmatterEdited_ReportsStale guards the gap CodeRabbit
// flagged: the drift stamp's hash covers ONLY the body below it, so editing the
// YAML frontmatter (name/description) of a stamped skill leaves the body hash and
// stamp line byte-identical — a body-only staleness check sees nothing wrong and
// the agent silently reads tampered metadata forever. The check must catch a
// frontmatter-only edit and --fix (reinstall) must restore the original.
// Failure prevented: a hand-edited skill description drifts from the live binary
// undetected because the stamp doesn't cover the frontmatter.
func TestHandleCheckSkills_FrontmatterEdited_ReportsStale(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	clean, err := handleCheckSkills(params)
	require.NoError(t, err)
	require.NotContains(t, clean.Stale, name, "precondition: fresh install must not be stale")

	skillPath := filepath.Join(dir, ".claude", "skills", name, skillFileName)
	orig, err := os.ReadFile(skillPath)
	require.NoError(t, err)

	// edit ONLY the frontmatter: inject a description line into the YAML block
	// above the closing fence, leaving the stamp line and body untouched. The
	// frontmatter ends at the first "\n---\n" (closing fence) of the file.
	fence := "\n---\n"
	fenceIdx := strings.Index(string(orig), fence)
	require.GreaterOrEqual(t, fenceIdx, 0, "installed skill must have a closing frontmatter fence")
	tampered := string(orig[:fenceIdx]) + "\ndescription: hand-edited frontmatter drift" + string(orig[fenceIdx:])
	require.NotEqual(t, string(orig), tampered, "the edit must actually change the frontmatter")
	require.NoError(t, os.WriteFile(skillPath, []byte(tampered), 0o644))

	stale, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.Contains(t, stale.Conflicts, name, "a frontmatter-only edit must be reported as a conflict")
	assert.False(t, stale.Installed, "Installed must be false when a skill's frontmatter drifted")

	// reinstall (the --fix path) must rewrite the file and clear staleness.
	resp, err := handleInstallSkills(params)
	require.NoError(t, err)
	assert.False(t, resp.Installed)
	assert.NotEmpty(t, resp.Conflicts)
	assert.NotContains(t, resp.FilesWritten, filepath.Join(".claude", "skills", name, skillFileName),
		"--fix must preserve a user-edited skill")

	fixed, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.Contains(t, fixed.Conflicts, name)
	assert.False(t, fixed.Installed)

	restored, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	assert.Contains(t, string(restored), "hand-edited frontmatter drift",
		"reinstall must preserve the tampered frontmatter for manual resolution")
}

// TestHandleCheckSkills_UnstampedFrontmatterDiff_NotOverwritten verifies the
// frontmatter check is gated on the file being ox-stamped: a user-authored
// (unstamped) SKILL.md whose frontmatter differs from the embedded skill must
// NEVER be flagged stale or overwritten. Without the stamp gate, the frontmatter
// diff would clobber a user's own skill.
// Failure prevented: install destroys a user's unstamped skill because its
// frontmatter happens to differ from the shipped one.
func TestHandleCheckSkills_UnstampedFrontmatterDiff_NotOverwritten(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	skillDir := filepath.Join(dir, ".claude", "skills", name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	// deliberately different frontmatter (and no ox stamp) from the shipped skill.
	userContent := "---\nname: " + name + "\ndescription: totally different, user-owned, no stamp\n---\nuser body\n"
	skillPath := filepath.Join(skillDir, skillFileName)
	require.NoError(t, os.WriteFile(skillPath, []byte(userContent), 0o644))

	resp, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.NotContains(t, resp.Stale, name,
		"an unstamped user skill must not be flagged stale even when its frontmatter differs")

	_, err = handleInstallSkills(params)
	require.NoError(t, err)
	after, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	assert.Equal(t, userContent, string(after),
		"install must not overwrite a user-authored skill on a frontmatter diff alone")
}

// TestHandleCheckSkills_UnstampedSkillNotStale verifies a user-authored SKILL.md
// (no ox stamp) is never flagged stale — ox only manages files it stamped.
// Failure prevented: doctor flags a user-owned skill forever and --fix never
// converges, or worse overwrites the user's content.
func TestHandleCheckSkills_UnstampedSkillNotStale(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	skillDir := filepath.Join(dir, ".claude", "skills", name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	userContent := "---\nname: " + name + "\ndescription: my own skill, no stamp\n---\nuser body\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, skillFileName), []byte(userContent), 0o644))

	resp, err := handleCheckSkills(params)
	require.NoError(t, err)
	assert.NotContains(t, resp.Stale, name, "unstamped user-authored skill must never be flagged stale")

	// install must not clobber the user's unstamped file.
	_, err = handleInstallSkills(params)
	require.NoError(t, err)
	after, err := os.ReadFile(filepath.Join(skillDir, skillFileName))
	require.NoError(t, err)
	assert.Equal(t, userContent, string(after), "install must not overwrite a user-authored skill")
}

// --- C. Uninstall lifecycle ---

// TestHandleUninstallSkills_RemovesStampedDirs verifies uninstall removes the
// stamped skill directories (the whole <name>/ tree, not just SKILL.md).
// Failure prevented: uninstall leaves stamped skill dirs behind, so
// "uninstall and reinstall to fix" silently fails.
func TestHandleUninstallSkills_RemovesStampedDirs(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	skillDir := filepath.Join(dir, ".claude", "skills", name)
	_, err = os.Stat(skillDir)
	require.NoError(t, err, "precondition: skill dir must exist before uninstall")

	resp, err := handleUninstallSkills(params)
	require.NoError(t, err)
	assert.True(t, resp.Uninstalled, "expected at least one stamped skill removed")
	assert.Contains(t, resp.FilesRemoved, filepath.Join(".claude", "skills", name, skillFileName),
		"FilesRemoved must reference the removed SKILL.md")

	_, err = os.Stat(filepath.Join(skillDir, skillFileName))
	assert.True(t, os.IsNotExist(err), "stamped SKILL.md must be removed by uninstall")
}

// TestHandleUninstallSkills_PreservesUnstampedSkill verifies a user-authored
// (unstamped) skill is left intact by uninstall.
// Failure prevented: uninstall destroys a user's own skill that happens to live
// under .claude/skills/.
func TestHandleUninstallSkills_PreservesUnstampedSkill(t *testing.T) {
	dir := t.TempDir()
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	userDir := filepath.Join(dir, ".claude", "skills", "my-skill")
	require.NoError(t, os.MkdirAll(userDir, 0o755))
	userFile := filepath.Join(userDir, skillFileName)
	require.NoError(t, os.WriteFile(userFile, []byte("---\nname: my-skill\n---\nno stamp\n"), 0o644))

	_, err = handleUninstallSkills(params)
	require.NoError(t, err)

	_, err = os.Stat(userFile)
	assert.NoError(t, err, "user-authored unstamped skill must NOT be removed by uninstall")
}

// --- D. Command→skill migration cleanup ---

// TestHandleInstallSkills_RemovesStampedLegacyCommand verifies the self-cleaning
// migration: when a surface moves from a slash command to a skill, an existing
// install's stale ox-stamped .claude/commands/<id>.md is pruned on skill install
// so the agent isn't left with a duplicate slash-invocable Layer-2 surface.
// Failure prevented: ox-plan / ox-session-review remain slash-invocable as stale
// commands alongside the new skill after a command→skill migration.
func TestHandleInstallSkills_RemovesStampedLegacyCommand(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	// simulate a prior install that wrote the surface as a slash command, stamped
	// the same way the command installer stamps (ox prefix, stamp on line 1).
	commandsDir := filepath.Join(dir, ".claude", "commands")
	require.NoError(t, os.MkdirAll(commandsDir, 0o755))
	legacyPath := filepath.Join(commandsDir, name+".md")
	stamped := agentx.StampedContent([]byte("# legacy "+name+" command body\n"), "0.7.0", oxSkillStampPrefix)
	require.NoError(t, os.WriteFile(legacyPath, stamped, 0o644))

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	_, err = os.Stat(legacyPath)
	assert.True(t, os.IsNotExist(err),
		"a stamped legacy command file superseded by an embedded skill must be removed on install")

	// the new skill must exist after migration.
	_, err = os.Stat(filepath.Join(dir, ".claude", "skills", name, skillFileName))
	assert.NoError(t, err, "the superseding skill must be installed")
}

// TestHandleInstallSkills_PreservesUnstampedLegacyCommand verifies the cleanup is
// defensive: a user-authored (unstamped) .claude/commands/<id>.md sharing an id
// with an embedded skill is NEVER deleted. We only prune files ox itself stamped.
// Failure prevented: install destroys a user's own slash command that happens to
// share a name with a shipped skill.
func TestHandleInstallSkills_PreservesUnstampedLegacyCommand(t *testing.T) {
	dir := t.TempDir()
	name := pickSkill(t)
	params := adapterprotocol.SkillsParams{RepoRoot: dir, Version: "0.8.0"}

	commandsDir := filepath.Join(dir, ".claude", "commands")
	require.NoError(t, os.MkdirAll(commandsDir, 0o755))
	legacyPath := filepath.Join(commandsDir, name+".md")
	userContent := "# my own " + name + " command, no ox stamp\n"
	require.NoError(t, os.WriteFile(legacyPath, []byte(userContent), 0o644))

	_, err := handleInstallSkills(params)
	require.NoError(t, err)

	data, err := os.ReadFile(legacyPath)
	require.NoError(t, err, "unstamped user-authored command file must survive install")
	assert.Equal(t, userContent, string(data), "user-authored command content must be untouched")
}
