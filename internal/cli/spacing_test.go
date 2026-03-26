package cli

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrintSectionBreak(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintSectionBreak()

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	// should print a newline
	assert.Equal(t, "\n", buf.String())
}

func TestPrintSubsectionBreak(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	PrintSubsectionBreak()

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)

	// intentional no-op
	assert.Empty(t, buf.String())
}

func TestSpacingConstants(t *testing.T) {
	assert.Equal(t, "\n", SectionBreak)
	assert.Equal(t, "", SubsectionBreak)
}
