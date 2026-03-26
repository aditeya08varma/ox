package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWordmark(t *testing.T) {
	result := Wordmark()

	// wordmark should contain both parts
	assert.Contains(t, result, "Sage")
	assert.Contains(t, result, "Ox")
	assert.NotEmpty(t, result)
}
