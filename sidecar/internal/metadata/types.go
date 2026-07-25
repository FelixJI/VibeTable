// Package metadata owns the allowlisted product channel for internal
// application metadata. It never accepts a raw PocketBase collection name.
package metadata

import (
	"encoding/json"
	"errors"
)

type Namespace string

const (
	NamespaceSharedSettings     Namespace = "shared_settings"
	NamespaceDashboards         Namespace = "dashboards"
	NamespacePanels             Namespace = "panels"
	NamespacePresets            Namespace = "presets"
	NamespaceIdentifierMappings Namespace = "identifier_mappings"
	NamespaceContentVersions    Namespace = "content_versions"

	StatusApplied  = "applied"
	StatusReplayed = "replayed"
)

var namespaceOrder = []Namespace{
	NamespaceSharedSettings,
	NamespaceDashboards,
	NamespacePanels,
	NamespacePresets,
	NamespaceIdentifierMappings,
	NamespaceContentVersions,
}

func Namespaces() []Namespace {
	return append([]Namespace(nil), namespaceOrder...)
}

type Item struct {
	Namespace Namespace       `json:"namespace"`
	LogicalID string          `json:"logicalId"`
	Payload   json.RawMessage `json:"payload"`
	Revision  string          `json:"revision"`
}

type UpsertRequest struct {
	Namespace        Namespace
	LogicalID        string
	Payload          json.RawMessage
	ExpectedRevision string
	IdempotencyKey   string
}

type DeleteRequest struct {
	Namespace        Namespace
	LogicalID        string
	ExpectedRevision string
	IdempotencyKey   string
}

type ReceiptTrace struct {
	Status        string   `json:"status"`
	ChangeSetID   string   `json:"changeSetId"`
	EmittedEvents []string `json:"emittedEvents"`
}

type MutationReceipt struct {
	ReceiptTrace
	Item Item `json:"item"`
}

type DeleteReceipt struct {
	ReceiptTrace
	Namespace Namespace `json:"namespace"`
	LogicalID string    `json:"logicalId"`
	Deleted   bool      `json:"deleted"`
}

type ItemMutation struct {
	LogicalID        string          `json:"logicalId"`
	Payload          json.RawMessage `json:"payload"`
	ExpectedRevision string          `json:"expectedRevision"`
}

type ItemDelete struct {
	LogicalID        string `json:"logicalId"`
	ExpectedRevision string `json:"expectedRevision"`
}

type DashboardCommitRequest struct {
	IdempotencyKey string         `json:"idempotencyKey"`
	Dashboard      ItemMutation   `json:"dashboard"`
	Panels         []ItemMutation `json:"panels"`
	DeletePanels   []ItemDelete   `json:"deletePanels"`
}

type DashboardCommitReceipt struct {
	ReceiptTrace
	Dashboard       Item     `json:"dashboard"`
	Panels          []Item   `json:"panels"`
	DeletedPanelIDs []string `json:"deletedPanelIds"`
}

type Error struct {
	Code      string         `json:"code"`
	Path      string         `json:"path,omitempty"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	Retryable bool           `json:"retryable"`
}

func (err *Error) Error() string {
	return err.Code + ": " + err.Message
}

func IsError(err error, code string) bool {
	var productErr *Error
	return errors.As(err, &productErr) && productErr.Code == code
}
