package attachments

import "io"

const ContractVersion = "1.0"

type Thumbnail struct {
	Variant            string `json:"variant"`
	DownloadCapability string `json:"downloadCapability"`
}

type Ref struct {
	ContractVersion    string      `json:"contractVersion"`
	TableID            string      `json:"tableId"`
	RecordID           string      `json:"recordId"`
	FieldID            string      `json:"fieldId"`
	StoredName         string      `json:"storedName"`
	OriginalName       string      `json:"originalName"`
	MIMEType           string      `json:"mimeType"`
	Size               int64       `json:"size"`
	SHA256             string      `json:"sha256"`
	DownloadCapability string      `json:"downloadCapability"`
	Thumbnails         []Thumbnail `json:"thumbnails"`
}

type Download struct {
	Reader      io.ReadSeekCloser
	Name        string
	ContentType string
	Size        int64
}

type IntegrityIssue struct {
	Code       string `json:"code"`
	TableID    string `json:"tableId"`
	RecordID   string `json:"recordId"`
	FieldID    string `json:"fieldId"`
	StoredName string `json:"storedName"`
}

type IntegrityReport struct {
	CheckedMetadata int              `json:"checkedMetadata"`
	CheckedVersions int              `json:"checkedVersions"`
	MissingFiles    []IntegrityIssue `json:"missingFiles"`
	MissingMetadata []IntegrityIssue `json:"missingMetadata"`
	CorruptFiles    []IntegrityIssue `json:"corruptFiles"`
	OrphanFiles     []IntegrityIssue `json:"orphanFiles"`
	OrphanVersions  []IntegrityIssue `json:"orphanVersions"`
	Valid           bool             `json:"valid"`
}

type RestoreItem struct {
	Source           string `json:"source"`
	VersionID        string `json:"versionId,omitempty"`
	SourceStoredName string `json:"sourceStoredName"`
	OriginalName     string `json:"originalName"`
	MIMEType         string `json:"mimeType"`
	Size             int64  `json:"size"`
	SHA256           string `json:"sha256"`
}

type RestorePlan struct {
	TableID      string        `json:"tableId"`
	RecordID     string        `json:"recordId"`
	FieldID      string        `json:"fieldId"`
	CurrentNames []string      `json:"currentNames"`
	Items        []RestoreItem `json:"items"`
}
