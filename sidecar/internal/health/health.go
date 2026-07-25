// Package health builds the sidecar readiness response.
package health

import (
	"net/http"
	"os"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/buildinfo"
)

type Response struct {
	Status          string         `json:"status"`
	PocketBase      string         `json:"pocketBase"`
	SchemaReady     bool           `json:"schemaReady"`
	StorageWritable bool           `json:"storageWritable"`
	StartedAt       time.Time      `json:"startedAt"`
	CheckedAt       time.Time      `json:"checkedAt"`
	Build           buildinfo.Info `json:"build"`
	Error           *Error         `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func Check(
	dataDir string,
	startedAt time.Time,
	build buildinfo.Info,
	checkedAt time.Time,
	databaseProbe func() error,
) (Response, int) {
	response := Response{
		Status:          "ok",
		PocketBase:      "ok",
		SchemaReady:     true,
		StorageWritable: true,
		StartedAt:       startedAt,
		CheckedAt:       checkedAt,
		Build:           build,
	}

	if err := ProbeWritable(dataDir); err != nil {
		response.Status = "degraded"
		response.StorageWritable = false
		response.Error = &Error{
			Code:    "health.storage_unwritable",
			Message: "sidecar data directory is not writable",
		}
		return response, http.StatusServiceUnavailable
	}
	if databaseProbe == nil || databaseProbe() != nil {
		response.Status = "degraded"
		response.PocketBase = "degraded"
		response.SchemaReady = false
		response.Error = &Error{
			Code:    "health.database_unavailable",
			Message: "PocketBase database or VibeTable schema is unavailable",
		}
		return response, http.StatusServiceUnavailable
	}
	return response, http.StatusOK
}

func ProbeWritable(dataDir string) error {
	file, err := os.CreateTemp(dataDir, ".vibetable-health-*")
	if err != nil {
		return err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}
