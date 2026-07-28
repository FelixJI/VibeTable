// Package buildinfo exposes immutable, non-secret release metadata.
package buildinfo

const (
	PocketBaseVersion = "0.39.9"
	CELVersion        = "0.26.1"
	ContractVersion   = "v1"
	ProtocolV2Version = "2.0"
	SchemaVersion     = "5"
	WorkspaceFormat   = "1"
	RepositoryFormat  = "kopia-v3"
	SnapshotFormat    = "2"
	PackageFormat     = "2"
	KopiaVersion      = "v0.23.1"
	AgeVersion        = "v1.3.1"
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
	ProtocolV2Version string `json:"protocolV2Version"`
	WorkspaceFormat   string `json:"workspaceFormat"`
	RepositoryFormat  string `json:"repositoryFormat"`
	SnapshotFormat    string `json:"snapshotFormat"`
	PackageFormat     string `json:"packageFormat"`
	KopiaVersion      string `json:"kopiaVersion"`
	AgeVersion        string `json:"ageVersion"`
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
		ProtocolV2Version: ProtocolV2Version,
		WorkspaceFormat:   WorkspaceFormat,
		RepositoryFormat:  RepositoryFormat,
		SnapshotFormat:    SnapshotFormat,
		PackageFormat:     PackageFormat,
		KopiaVersion:      KopiaVersion,
		AgeVersion:        AgeVersion,
	}
}
