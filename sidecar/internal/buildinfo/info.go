// Package buildinfo exposes immutable, non-secret release metadata.
package buildinfo

const (
	PocketBaseVersion = "0.39.9"
	CELVersion        = "0.26.1"
	ContractVersion   = "v1"
	SchemaVersion     = "4"
)

// These values are populated with -ldflags in release builds.
var (
	Version   = "0.1.0-dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

type Info struct {
	Version           string `json:"version"`
	Commit            string `json:"commit"`
	BuildTime         string `json:"buildTime"`
	GoContractVersion string `json:"contractVersion"`
	PocketBaseVersion string `json:"pocketBaseVersion"`
	CELVersion        string `json:"celVersion"`
	SchemaVersion     string `json:"schemaVersion"`
	MigrationHash     string `json:"migrationHash"`
}

func Current(migrationHash string) Info {
	return Info{
		Version:           Version,
		Commit:            Commit,
		BuildTime:         BuildTime,
		GoContractVersion: ContractVersion,
		PocketBaseVersion: PocketBaseVersion,
		CELVersion:        CELVersion,
		SchemaVersion:     SchemaVersion,
		MigrationHash:     migrationHash,
	}
}
