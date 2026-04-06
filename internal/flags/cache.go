package flags

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
)

// LoadCachedSettings reads the cached CLISettingsResponse for the given endpoint.
// Returns nil with no error if the file doesn't exist or is corrupt —
// callers treat nil as "no cached settings, use defaults".
func LoadCachedSettings(endpointURL string) (*CLISettingsResponse, error) {
	slug := endpoint.NormalizeSlug(endpointURL)
	filePath := paths.CLISettingsFileForEndpoint(slug)

	data, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		slog.Warn("cli settings cache unreadable, using defaults", "path", filePath, "err", err)
		return nil, nil
	}

	var resp CLISettingsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		slog.Warn("cli settings cache corrupt, using defaults", "path", filePath, "err", err)
		return nil, nil
	}

	return &resp, nil
}

// SaveCachedSettings atomically writes r into a per-endpoint cache file.
// Creates the cache directory if needed.
// Each endpoint writes its own file, so concurrent writes for different
// endpoints cannot race or lose each other's data.
func SaveCachedSettings(endpointURL string, r *CLISettingsResponse) error {
	slug := endpoint.NormalizeSlug(endpointURL)
	filePath := paths.CLISettingsFileForEndpoint(slug)

	if err := os.MkdirAll(filepath.Dir(filePath), 0700); err != nil {
		return err
	}

	return fileutil.AtomicWriteJSON(filePath, r, 0600)
}
