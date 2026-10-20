package app

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/auth"
	"github.com/vibetable/vibetable/sidecar/internal/diagnostics"
)

func TestStartupFailureReportsSanitizedCause(t *testing.T) {
	const probeEnvironment = "VIBETABLE_TEST_STARTUP_DIAGNOSTIC_DATA"
	if dataDir := os.Getenv(probeEnvironment); dataDir != "" {
		secret, _, err := auth.Generate()
		if err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		_, err = New(Options{DataDir: dataDir, Session: secret,
			Logger: slog.New(diagnostics.NewJSONHandler(&output, slog.LevelInfo)), ReadyWriter: io.Discard})
		if err == nil {
			t.Fatal("invalid synthetic startup unexpectedly succeeded")
		}
		_, _ = os.Stderr.Write(output.Bytes())
		// The process boundary releases the existing failed factory's ledger.
		os.Exit(0)
	}
	for _, phase := range []string{"bootstrap", "migrations"} {
		t.Run(phase, func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "private-workspace")
			if err := os.MkdirAll(dataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			expectedCode := "sqlite.result_26"
			if phase == "bootstrap" {
				if err := os.WriteFile(filepath.Join(dataDir, "data.db"), []byte("private-snapshot-payload"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				database := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: dataDir, HideStartBanner: true})
				if err := database.Bootstrap(); err != nil {
					t.Fatal(err)
				}
				collection := core.NewBaseCollection("vibetable_tables")
				collection.Fields.Add(&core.TextField{Name: "table_id", Required: true, Max: 10000})
				if err := database.Save(collection); err != nil {
					t.Fatal(err)
				}
				if err := database.ResetBootstrapState(); err != nil {
					t.Fatal(err)
				}
				expectedCode = "sidecar.start_failed"
			}
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStartupFailureReportsSanitizedCause$")
			command.Env = append(os.Environ(), probeEnvironment+"="+dataDir)
			// Keep the structured probe separate from PocketBase's stdout notices.
			var output bytes.Buffer
			command.Stdout, command.Stderr = io.Discard, &output
			err := command.Run()
			if err != nil {
				t.Fatalf("startup probe failed: %v", err)
			}
			var event struct {
				Event     string `json:"event"`
				ErrorCode string `json:"errorCode"`
			}
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
				t.Fatalf("missing structured startup failure: %v", err)
			}
			if event.Event != "sidecar."+phase+"_failed" || event.ErrorCode != expectedCode {
				t.Fatalf("startup diagnostic=%+v", event)
			}
			for _, private := range []string{dataDir, "private-workspace", "private-snapshot-payload", "data.db", "vibetable_tables"} {
				if strings.Contains(output.String(), private) {
					t.Fatal("startup diagnostic leaked synthetic private data")
				}
			}
		})
	}
}
