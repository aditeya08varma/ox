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

// cacheFileFormat is the on-disk structure: a map from normalized endpoint URL
// to the last-fetched CLISettingsResponse for that endpoint.
//
// Multiple daemons (one per ledger today, one per host in future) may write
// concurrently — last atomic rename wins, which is safe because all writers
// for the same endpoint receive identical server-evaluated flags.
type cacheFileFormat = map[string]CLISettingsResponse

// LoadCachedSettings reads the cached CLISettingsResponse for the given endpoint.
// Returns nil with no error if the file doesn't exist or the entry is absent —
// callers treat nil as "no cached settings, use defaults".
func LoadCachedSettings(endpointURL string) (*CLISettingsResponse, error) {
	key := endpoint.NormalizeEndpoint(endpointURL)
	filePath := paths.CLISettingsFile()

	data, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		// unreadable file (permissions, I/O error) — treat as empty cache
		slog.Warn("cli settings cache unreadable, using defaults", "path", filePath, "err", err)
		return nil, nil
	}

	var cache cacheFileFormat
	if err := json.Unmarshal(data, &cache); err != nil {
		// corrupt JSON — treat as empty cache rather than hard-failing startup
		slog.Warn("cli settings cache corrupt, using defaults", "path", filePath, "err", err)
		return nil, nil
	}

	entry, ok := cache[key]
	if !ok {
		return nil, nil
	}
	return &entry, nil
}

// SaveCachedSettings atomically writes r into the cache file under the
// normalized endpoint key. Creates the cache directory if needed.
// Called by the daemon after each successful /api/v1/cli/settings fetch.
func SaveCachedSettings(endpointURL string, r *CLISettingsResponse) error {
	key := endpoint.NormalizeEndpoint(endpointURL)
	filePath := paths.CLISettingsFile()

	if err := os.MkdirAll(filepath.Dir(filePath), 0700); err != nil {
		return err
	}

	// read existing cache so we can merge rather than overwrite other endpoints
	cache := make(cacheFileFormat)
	if data, err := os.ReadFile(filePath); err == nil {
		// ignore unmarshal errors — treat corrupt file as empty and overwrite
		_ = json.Unmarshal(data, &cache)
	}

	cache[key] = *r

	return fileutil.AtomicWriteJSON(filePath, cache, 0600)
}
