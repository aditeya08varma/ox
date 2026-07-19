package plan

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Deterministic session link on rendered plans ---

// TestRenderHTML_SessionURLFooterLink verifies the renderer stamps the /c/
// conversation link into the footer when the producing recording is known,
// and omits it entirely when not.
// Failure prevented: committed plan artifacts losing their deterministic
// backlink to the session that produced them (the zero-agent-token surface).
func TestRenderHTML_SessionURLFooterLink(t *testing.T) {
	in := Input{Raw: "# Plan\n\nDo the thing."}

	withURL, err := RenderHTMLOpts(in, Result{}, RenderOptions{
		SessionURL: "https://sageox.ai/c/ses_01890a5d-ac96-774b-bcce-b302099a8057",
	})
	require.NoError(t, err)
	html := string(withURL)
	assert.Contains(t, html, `href="https://sageox.ai/c/ses_01890a5d-ac96-774b-bcce-b302099a8057"`)
	assert.Contains(t, html, "Session recording")

	withoutURL, err := RenderHTMLOpts(in, Result{}, RenderOptions{})
	require.NoError(t, err)
	assert.NotContains(t, string(withoutURL), "Session recording",
		"no session → no dangling footer link")
}

// TestLintSessionLink verifies the advisory that keeps agent-authored renders
// honest: a plan whose provenance names a session must link back to that
// exact session.
// Failure prevented: ox-plan-skill renders (agent-authored, fallible)
// silently shipping without the session backlink — or with a link to the
// wrong session, which is as bad as none.
func TestLintSessionLink(t *testing.T) {
	sesID := "ses_01890a5d-ac96-774b-bcce-b302099a8057"
	page := func(link string) []byte {
		return []byte("<html><body><a href=\"" + link + "\">x</a></body></html>")
	}

	t.Run("link present is clean", func(t *testing.T) {
		assert.Empty(t, LintSessionLink(page("https://sageox.ai/c/"+sesID), sesID))
	})
	t.Run("missing link warns", func(t *testing.T) {
		findings := LintSessionLink(page("https://example.com"), sesID)
		require.Len(t, findings, 1)
		assert.Equal(t, "session.link-missing", findings[0].Rule)
		assert.True(t, strings.Contains(findings[0].Message, sesID))
	})
	t.Run("wrong session link still warns", func(t *testing.T) {
		findings := LintSessionLink(page("https://sageox.ai/c/ses_74738ff5-5367-5958-9aee-98fffdcd1876"), sesID)
		require.Len(t, findings, 1, "a link to a different session is as wrong as none")
	})
	t.Run("no session id is fail-open", func(t *testing.T) {
		assert.Empty(t, LintSessionLink(page("https://example.com"), ""))
	})
	t.Run("empty page is fail-open", func(t *testing.T) {
		assert.Empty(t, LintSessionLink(nil, sesID))
	})
}
