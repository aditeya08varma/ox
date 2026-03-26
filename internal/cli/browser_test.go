package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOpenInBrowser_SkipBrowserEnv(t *testing.T) {
	t.Setenv("SKIP_BROWSER", "1")

	err := OpenInBrowser("https://example.com")
	assert.NoError(t, err, "OpenInBrowser should silently succeed when SKIP_BROWSER=1")
}

func TestOpenInBrowser_HeadlessReturnsError(t *testing.T) {
	t.Setenv("SKIP_BROWSER", "")
	t.Setenv("SSH_CLIENT", "192.168.1.1 54321 22")

	err := OpenInBrowser("https://example.com")
	assert.ErrorIs(t, err, ErrHeadless)
}
