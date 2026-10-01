package workspacev2

// Opt-in, test-only producer of legal near-limit and maximum-depth
// FileHistory workspaces that the real Host can naturally reopen.
//
// Host lease contract (read from writecoordinator.openPersistentStore,
// DesktopWorkspaceAuthorityStore, and WorkspaceSessionManager): the persisted
// coordination_state is resumed with the exact same workspace/fence/claim
// identity, and the real Host discovers that identity from
// .vibetable/coordination/desktop-runtime-authority.json inside the
// workspace. Prepare/TryRead reuse the file's claim/fence, Reserve advances
// LastSessionEpoch strictly, and WorkspaceSessionManager raises its next
// epoch from ReadLastSessionEpoch after acquiring the writer lease. This
// producer therefore creates a fresh synthetic workspace through the real
// Runtime path and writes that authority file with the exact lease identity
// it persisted, so the next real Host session inherits everything without
// manual sidecar parameters.
//
// filehistory.Document/Revision are reused directly; only the unexported
// three-field root payload wrapper and the five-field desktop authority
// record are mirrored as plain JSON in their exact consumer shapes.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pocketbase/pocketbase"
	"github.com/vibetable/vibetable/sidecar/internal/audit"
	"github.com/vibetable/vibetable/sidecar/internal/auditledger"
	"github.com/vibetable/vibetable/sidecar/internal/filehistory"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
	"github.com/vibetable/vibetable/sidecar/internal/objectrepo"
	"github.com/vibetable/vibetable/sidecar/internal/writecoordinator"
)

const (
	capacityHostFixtureEnv        = "VIBETABLE_415_CAPACITY_HOST_FIXTURE_ROOT"
	capacityHostSeedDatabase      = "write-coordinator.db"
	capacityHostAuthorityFileName = "desktop-runtime-authority.json"
	capacityHostTotalAllocLimit   = 256 << 20
	capacityHostPageLimit         = 2 * time.Second
	capacityHostSaveOpenLimit     = 30 * time.Second
	// capacityHostRootFormatVersion mirrors filehistory's unexported
	// rootFormatVersion (3); the verified Open enforces the exact value.
	capacityHostRootFormatVersion = 3
	// capacityHostAuthorityFormatVersion mirrors the desktop
	// DesktopWorkspaceAuthorityStore FormatVersion (1).
	capacityHostAuthorityFormatVersion = 1
)

// capacityHostFixtureBudgetsEnabled is only true in non-race builds; under
// -race this producer still verifies correctness but applies no wall budget.
var capacityHostFixtureBudgetsEnabled bool

// capacityHostRootPayload mirrors filehistory's unexported rootPayload
// wrapper. The document and revision types are the real exported contracts.
type capacityHostRootPayload struct {
	FormatVersion int                    `json:"formatVersion"`
	WorkspaceID   string                 `json:"workspaceId"`
	Documents     []filehistory.Document `json:"documents"`
}

type capacityHostSpec struct {
	name    string
	singles int
	multis  int
	chain   int
	grow    bool
	// profile captures allocs before/after around the runtimeReopen and
	// save measured windows so `go tool pprof -alloc_space -base` can
	// attribute the operation delta without the producer's total profile.
	profile bool
}

func (spec capacityHostSpec) documentTotal() int {
	return spec.singles + spec.multis
}

func (spec capacityHostSpec) revisionTotal() int64 {
	return int64(spec.singles) + int64(spec.multis)*int64(spec.chain)
}

type capacityHostIdentity struct {
	WorkspaceID  string `json:"workspaceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
	FenceEpoch   uint64 `json:"fenceEpoch"`
	ClaimID      string `json:"claimId"`
}

// capacityHostAuthorityFile mirrors the desktop DesktopWorkspaceAuthority
// record exactly as WorkspaceV2Json.StrictOptions serializes it (Web
// camelCase, case-sensitive, no unmapped members): the real Host reads this
// file to inherit fence/claim and the session floor, so only the exact
// five-field shape is ever written or accepted here.
type capacityHostAuthorityFile struct {
	FormatVersion    int    `json:"formatVersion"`
	WorkspaceID      string `json:"workspaceId"`
	FenceEpoch       uint64 `json:"fenceEpoch"`
	ClaimID          string `json:"claimId"`
	LastSessionEpoch uint64 `json:"lastSessionEpoch"`
}

type capacityHostOperation struct {
	Fixture         string  `json:"fixture"`
	Operation       string  `json:"operation"`
	Documents       int     `json:"documents"`
	Revisions       int64   `json:"revisions"`
	ElapsedMillis   float64 `json:"elapsedMillis"`
	TotalAllocBytes uint64  `json:"totalAllocBytes"`
	HeapAllocBefore uint64  `json:"heapAllocBefore"`
	HeapAllocAfter  uint64  `json:"heapAllocAfter"`
	ProfileBefore   string  `json:"profileBefore,omitempty"`
	ProfileAfter    string  `json:"profileAfter,omitempty"`
	Detail          string  `json:"detail"`
}

type capacityHostFixtureEntry struct {
	Name                             string               `json:"name"`
	WorkspaceRoot                    string               `json:"workspaceRoot"`
	DataDir                          string               `json:"dataDir"`
	FilesRoot                        string               `json:"filesRoot"`
	ManifestPath                     string               `json:"manifestPath"`
	AuthorityFile                    string               `json:"authorityFile"`
	Identity                         capacityHostIdentity `json:"identity"`
	StorageMode                      string               `json:"storageMode"`
	EncryptionMode                   string               `json:"encryptionMode"`
	RepositoryFormat                 string               `json:"repositoryFormat"`
	Documents                        int                  `json:"documents"`
	Revisions                        int64                `json:"revisions"`
	SelectedMultiDocumentID          string               `json:"selectedMultiDocumentId"`
	SelectedMultiFirstRevisionID     string               `json:"selectedMultiFirstRevisionId"`
	SelectedMultiEffectiveRevisionID string               `json:"selectedMultiEffectiveRevisionId"`
	SelectedMultiRevisionCount       int                  `json:"selectedMultiRevisionCount"`
	SelectedMultiRelativePath        string               `json:"selectedMultiRelativePath"`
	SelectedMultiStatus              string               `json:"selectedMultiStatus"`
	HeadRoot                         string               `json:"headRoot"`
	HeadRevision                     uint64               `json:"headRevision"`
	HeadMutationRevision             uint64               `json:"headMutationRevision"`
	SeedRoot                         string               `json:"seedRoot"`
	SeedDocumentID                   string               `json:"seedDocumentId"`
	SeedRevisionID                   string               `json:"seedRevisionId"`
	SeedObjectID                     string               `json:"seedObjectId"`
	SeedContentHash                  string               `json:"seedContentHash"`
	SeedMaterializedFile             string               `json:"seedMaterializedFile"`
	SeedMaterializedBytes            int64                `json:"seedMaterializedBytes"`
}

type capacityHostFixtureMetadata struct {
	GeneratedAt      string                     `json:"generatedAt"`
	GoVersion        string                     `json:"goVersion"`
	Producer         string                     `json:"producer"`
	EnvVariable      string                     `json:"envVariable"`
	OutputDirectory  string                     `json:"outputDirectory"`
	BudgetsApplied   bool                       `json:"budgetsApplied"`
	Budgets          map[string]string          `json:"budgets"`
	HostRequirements []string                   `json:"hostRequirements"`
	Boundaries       []string                   `json:"boundaries"`
	Fixtures         []capacityHostFixtureEntry `json:"fixtures"`
	Operations       []capacityHostOperation    `json:"operations"`
}

type capacityHostRecorder struct {
	operations []capacityHostOperation
}

// writeCapacityHostAllocProfile snapshots the cumulative allocs profile.
// Callers place the writes so the run window stays inside the memstats
// delta while the serialization cost itself stays outside it.
func writeCapacityHostAllocProfile(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := pprof.Lookup("allocs").WriteTo(file, 0); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func (recorder *capacityHostRecorder) measure(
	t *testing.T,
	fixture string,
	operation string,
	documents int,
	revisions int64,
	wallBudget time.Duration,
	profileDir string,
	detail string,
	run func() error,
) {
	t.Helper()
	runtime.GC()
	profileBefore := ""
	profileAfter := ""
	if profileDir != "" {
		// Settle sampled allocation bookkeeping so the before snapshot does
		// not lag behind recent allocations.
		runtime.GC()
		profileBefore = filepath.Join(
			profileDir, fixture+"-"+operation+"-before.pprof",
		)
		writeCapacityHostAllocProfile(t, profileBefore)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	runErr := run()
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if profileDir != "" {
		// The TotalAlloc/wall window closed at ReadMemStats(after); two GCs
		// before the snapshot keep the allocs profile from lagging without
		// touching the measured window itself.
		runtime.GC()
		runtime.GC()
		profileAfter = filepath.Join(
			profileDir, fixture+"-"+operation+"-after.pprof",
		)
		writeCapacityHostAllocProfile(t, profileAfter)
	}
	if runErr != nil {
		t.Fatalf("%s %s failed: %v", fixture, operation, runErr)
	}
	totalAlloc := after.TotalAlloc - before.TotalAlloc
	if capacityHostFixtureBudgetsEnabled {
		if elapsed > wallBudget {
			t.Errorf(
				"%s %s elapsed = %v, want <= %v",
				fixture, operation, elapsed, wallBudget,
			)
		}
		if totalAlloc > capacityHostTotalAllocLimit {
			t.Errorf(
				"%s %s TotalAlloc = %d MiB, want <= %d MiB",
				fixture, operation, totalAlloc>>20,
				capacityHostTotalAllocLimit>>20,
			)
		}
	}
	record := capacityHostOperation{
		Fixture: fixture, Operation: operation,
		Documents: documents, Revisions: revisions,
		ElapsedMillis:   float64(elapsed.Microseconds()) / 1000,
		TotalAllocBytes: totalAlloc,
		HeapAllocBefore: before.HeapAlloc,
		HeapAllocAfter:  after.HeapAlloc,
		ProfileBefore:   profileBefore,
		ProfileAfter:    profileAfter,
		Detail:          detail,
	}
	recorder.operations = append(recorder.operations, record)
	t.Logf(
		"capacity-host %s %s: docs=%d revisions=%d elapsed=%.3fms totalAlloc=%dMiB heap=%dMiB->%dMiB",
		fixture, operation, documents, revisions,
		record.ElapsedMillis, totalAlloc>>20,
		before.HeapAlloc>>20, after.HeapAlloc>>20,
	)
	if profileBefore != "" {
		t.Logf(
			"capacity-host %s %s profiles: before=%s after=%s",
			fixture, operation, profileBefore, profileAfter,
		)
	}
}

func capacityHostRepositoryRoot() (string, error) {
	working, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root := filepath.Clean(filepath.Join(working, "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "sidecar", "go.mod")); err != nil {
		return "", fmt.Errorf("repository root not located from %s", working)
	}
	return root, nil
}

// resolveCapacityHostFixtureTarget only ever accepts a fresh (non-existing)
// path strictly inside allowedBase, so the producer can never overwrite real
// data.
func resolveCapacityHostFixtureTarget(configured, allowedBase string) (string, error) {
	if strings.TrimSpace(configured) == "" {
		return "", errors.New("capacity host fixture root is required")
	}
	absolute, err := filepath.Abs(configured)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	base, err := filepath.Abs(allowedBase)
	if err != nil {
		return "", err
	}
	base = filepath.Clean(base)
	relative, err := filepath.Rel(base, absolute)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf(
			"capacity host fixture root %q must be a fresh path inside %q",
			configured, base,
		)
	}
	if _, statErr := os.Lstat(absolute); !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf(
			"capacity host fixture target already exists: %s", absolute,
		)
	}
	return absolute, nil
}

// TestCapacityHostFixtureProducerGuardContract runs in every normal test
// mode and proves the opt-in guard cannot be pointed at real or existing
// data, and that the authority-file serialization stays exactly what the
// desktop StrictOptions consumer reads. Generation itself stays skipped
// without the env variable.
func TestCapacityHostFixtureProducerGuardContract(t *testing.T) {
	base := t.TempDir()
	fresh := filepath.Join(base, "run-a")
	resolved, err := resolveCapacityHostFixtureTarget(fresh, base)
	if err != nil || resolved != filepath.Clean(fresh) {
		t.Fatalf("fresh in-base target rejected: %q %v", resolved, err)
	}
	existing := filepath.Join(base, "already-there")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	// A second t.TempDir is a sibling of base and therefore provably outside
	// it, without leaving an untracked directory in the system temp.
	outsideSibling := t.TempDir()
	for name, configured := range map[string]string{
		"empty":          "   ",
		"base itself":    base,
		"existing child": existing,
		"parent escape":  filepath.Join(base, "..", "escape"),
		"outside base":   filepath.Join(outsideSibling, "run"),
	} {
		if _, err := resolveCapacityHostFixtureTarget(configured, base); err == nil {
			t.Fatalf("%s target was accepted: %s", name, configured)
		}
	}

	// The authority-file serialization must stay exactly what the desktop
	// StrictOptions consumer reads: five camelCase fields, nothing else.
	authoritySample := capacityHostAuthorityFile{
		FormatVersion:    capacityHostAuthorityFormatVersion,
		WorkspaceID:      "11111111-1111-4111-8111-111111111111",
		FenceEpoch:       3,
		ClaimID:          "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		LastSessionEpoch: 7,
	}
	authorityRaw, err := json.Marshal(authoritySample)
	if err != nil {
		t.Fatal(err)
	}
	var authorityFields map[string]any
	if err := json.Unmarshal(authorityRaw, &authorityFields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"formatVersion", "workspaceId", "fenceEpoch", "claimId", "lastSessionEpoch",
	} {
		if _, exists := authorityFields[field]; !exists {
			t.Fatalf("authority JSON missing field %s: %s", field, authorityRaw)
		}
	}
	if len(authorityFields) != 5 {
		t.Fatalf("authority JSON fields = %v, want exactly five", authorityFields)
	}
	if _, err := decodeCapacityHostAuthorityStrict(
		append(authorityRaw[:len(authorityRaw)-1], []byte(`,"extra":1}`)...),
	); err == nil {
		t.Fatal("authority strict decode accepted an unmapped member")
	}
	decoded, err := decodeCapacityHostAuthorityStrict(authorityRaw)
	if err != nil || decoded != authoritySample {
		t.Fatalf("authority round trip = %#v err = %v", decoded, err)
	}
}

// decodeCapacityHostAuthorityStrict mirrors the desktop StrictOptions
// behavior: case-sensitive exact five camelCase fields, no unmapped members.
func decodeCapacityHostAuthorityStrict(raw []byte) (capacityHostAuthorityFile, error) {
	var authority capacityHostAuthorityFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&authority); err != nil {
		return capacityHostAuthorityFile{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return capacityHostAuthorityFile{}, errors.New("trailing JSON")
		}
		return capacityHostAuthorityFile{}, err
	}
	return authority, nil
}

// ensureCapacityHostAuthority binds the workspace's desktop authority record
// to the exact lease identity the producer persisted. It writes the file in
// the exact consumer format when absent and validates it in place whenever
// present; it never overwrites an existing file.
func ensureCapacityHostAuthority(
	t *testing.T,
	workspaceRoot string,
	identity capacityHostIdentity,
) string {
	t.Helper()
	path := filepath.Join(
		workspaceRoot, ".vibetable", "coordination", capacityHostAuthorityFileName,
	)
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		if !errors.Is(readErr, os.ErrNotExist) {
			t.Fatal(readErr)
		}
		authority := capacityHostAuthorityFile{
			FormatVersion:    capacityHostAuthorityFormatVersion,
			WorkspaceID:      identity.WorkspaceID,
			FenceEpoch:       identity.FenceEpoch,
			ClaimID:          identity.ClaimID,
			LastSessionEpoch: identity.SessionEpoch,
		}
		payload, err := json.Marshal(authority)
		if err != nil {
			t.Fatal(err)
		}
		// Mirror DesktopWorkspaceAuthorityStore.Write: temp file, then move.
		temporary := path + ".producer-tmp"
		if err := os.WriteFile(temporary, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(temporary, path); err != nil {
			t.Fatal(err)
		}
		raw = payload
	}
	authority, err := decodeCapacityHostAuthorityStrict(raw)
	if err != nil {
		t.Fatalf("authority file %s is not strict-consumer compatible: %v", path, err)
	}
	if authority.FormatVersion != capacityHostAuthorityFormatVersion ||
		authority.WorkspaceID != identity.WorkspaceID ||
		authority.FenceEpoch != identity.FenceEpoch ||
		authority.ClaimID != identity.ClaimID ||
		authority.LastSessionEpoch != identity.SessionEpoch {
		t.Fatalf(
			"authority file identity mismatch: %#v want workspace %s fence %d claim %s session floor %d",
			authority, identity.WorkspaceID, identity.FenceEpoch,
			identity.ClaimID, identity.SessionEpoch,
		)
	}
	return path
}

// ensureCapacityHostAuditOutbox mirrors createAuditOutbox but is idempotent
// so repeated bootstrap/reopen cycles over the same pb_data succeed.
func ensureCapacityHostAuditOutbox(t *testing.T, app *pocketbase.PocketBase) {
	t.Helper()
	if _, err := app.DB().NewQuery(`
		CREATE TABLE IF NOT EXISTS vibetable_audit_outbox (
			event_id TEXT PRIMARY KEY,
			source_epoch TEXT NOT NULL,
			source_sequence INTEGER NOT NULL,
			mutation_identity TEXT NOT NULL,
			payload_hash TEXT NOT NULL,
			payload_json BLOB NOT NULL,
			occurred_at TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			UNIQUE(source_epoch, source_sequence)
		)
	`).Execute(); err != nil {
		t.Fatal(err)
	}
}

type capacityHostServices struct {
	app    *pocketbase.PocketBase
	ledger *auditledger.Ledger
	audit  *audit.Service
}

func newCapacityHostServices(t *testing.T, dataDir string) *capacityHostServices {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  dataDir,
		HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	ensureCapacityHostAuditOutbox(t, app)
	ledger, err := auditledger.Open(
		filepath.Join(filepath.Dir(dataDir), "audit"),
	)
	if err != nil {
		t.Fatal(err)
	}
	auditHistory, err := audit.New(
		app,
		mutation.New(app, mutation.MetadataSchemaSource{}),
		audit.WithLedgerHistory(ledger),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &capacityHostServices{app: app, ledger: ledger, audit: auditHistory}
}

func (services *capacityHostServices) close(t *testing.T) {
	t.Helper()
	if err := services.ledger.Close(); err != nil {
		t.Errorf("close capacity host ledger: %v", err)
	}
	if err := services.app.ResetBootstrapState(); err != nil {
		t.Errorf("reset capacity host pocketbase state: %v", err)
	}
}

func openCapacityHostRuntime(
	t *testing.T,
	services *capacityHostServices,
	dataDir string,
	identity capacityHostIdentity,
) *Runtime {
	t.Helper()
	runtimeInstance, err := Open(context.Background(), Options{
		App: services.app, DataDir: dataDir,
		WorkspaceID:  identity.WorkspaceID,
		SessionEpoch: identity.SessionEpoch,
		FenceEpoch:   identity.FenceEpoch,
		ClaimID:      identity.ClaimID,
		Ledger:       services.ledger, Audit: services.audit,
		DeferBackgroundWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtimeInstance
}

func closeCapacityHostRuntime(
	t *testing.T,
	runtimeInstance *Runtime,
	services *capacityHostServices,
) {
	t.Helper()
	if err := runtimeInstance.Close(context.Background()); err != nil {
		t.Errorf("close capacity host runtime: %v", err)
	}
	services.close(t)
}

// readCapacityHostCoordinationIdentity reads the persisted coordination
// identity with the same query Runtime.openCoordinator uses.
func readCapacityHostCoordinationIdentity(
	t *testing.T,
	workspaceRoot string,
) capacityHostIdentity {
	t.Helper()
	databasePath := filepath.Join(
		workspaceRoot, ".vibetable", "coordination", capacityHostSeedDatabase,
	)
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var identity capacityHostIdentity
	queryErr := database.QueryRowContext(
		context.Background(),
		`SELECT workspace_id, session_epoch, fence_epoch, claim_id
		 FROM coordination_state WHERE singleton = 1`,
	).Scan(
		&identity.WorkspaceID,
		&identity.SessionEpoch,
		&identity.FenceEpoch,
		&identity.ClaimID,
	)
	if queryErr != nil {
		t.Fatalf("read coordination identity: %v", queryErr)
	}
	if !validUUID(identity.WorkspaceID) || !validUUID(identity.ClaimID) ||
		identity.SessionEpoch == 0 || identity.FenceEpoch == 0 {
		t.Fatalf("coordination identity invalid: %#v", identity)
	}
	return identity
}

func capacityHostUint64Pointer(value uint64) *uint64 { return &value }

func appendCapacityHostDocument(
	documents []filehistory.Document,
	base filehistory.Document,
	chain int,
	relativePath string,
) []filehistory.Document {
	documentID := uuid.NewString()
	baseRevision := base.Revisions[0]
	revisions := make([]filehistory.Revision, chain)
	var parentID *string
	for chainIndex := range chain {
		revision := baseRevision
		revision.RevisionID = uuid.NewString()
		revision.DocumentID = documentID
		revision.ParentRevisionID = parentID
		revision.RestoredFromRevisionID = nil
		revision.LocalSequence = nil
		revision.Comment = nil
		revision.RevisionOrdinal = uint64(chainIndex + 1)
		revision.FormalVersion = capacityHostUint64Pointer(uint64(chainIndex + 1))
		revision.CreatedAt = baseRevision.CreatedAt.Add(
			time.Duration(chainIndex) * time.Nanosecond,
		)
		revisionID := revision.RevisionID
		parentID = &revisionID
		revisions[chainIndex] = revision
	}
	document := base
	document.DocumentID = documentID
	document.RelativePath = relativePath
	document.EffectiveRevisionID = revisions[chain-1].RevisionID
	document.NextRevisionOrdinal = uint64(chain) + 1
	document.NextFormalVersion = uint64(chain) + 1
	document.Revisions = revisions
	return append(documents, document)
}

// createCapacityHostWorkspace lays out a fresh synthetic workspace with the
// real binding manifest schema and persists the coordination lease through
// one real Runtime open/close, using natural first-writer lease values.
func createCapacityHostWorkspace(
	t *testing.T,
	workspaceRoot string,
) capacityHostIdentity {
	t.Helper()
	metadata := filepath.Join(workspaceRoot, ".vibetable")
	for _, name := range []string{
		"data", "topology", "objects", "audit", "snapshots",
		"coordination", "quarantine", "temp",
	} {
		if err := os.MkdirAll(filepath.Join(metadata, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(workspaceRoot, "files"), 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceID := uuid.NewString()
	manifest := map[string]any{
		"contractVersion":         "2.0",
		"formatVersion":           3,
		"workspaceId":             workspaceID,
		"displayName":             "Task415 Capacity Host Fixture",
		"createdAt":               time.Now().UTC().Format(time.RFC3339),
		"storageMode":             "direct",
		"encryptionMode":          "convenient",
		"repositoryFormat":        "kopia-v4",
		"topologySchemaVersion":   1,
		"businessSchemaVersion":   1,
		"importedFromWorkspaceId": nil,
		"sourceSnapshotId":        nil,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(metadata, "workspace.json"), raw, 0o600,
	); err != nil {
		t.Fatal(err)
	}
	// Natural first-writer lease: fence 1, fresh claim, session 1 — the same
	// shape DesktopWorkspaceAuthorityStore.Prepare/Reserve produces.
	identity := capacityHostIdentity{
		WorkspaceID:  workspaceID,
		SessionEpoch: 1,
		FenceEpoch:   1,
		ClaimID:      uuid.NewString(),
	}
	dataDir := filepath.Join(metadata, "data")
	services := newCapacityHostServices(t, dataDir)
	runtimeInstance := openCapacityHostRuntime(t, services, dataDir, identity)
	closeCapacityHostRuntime(t, runtimeInstance, services)
	persisted := readCapacityHostCoordinationIdentity(t, workspaceRoot)
	if persisted != identity {
		t.Fatalf("persisted coordination identity %#v != %#v", persisted, identity)
	}
	return identity
}

func produceCapacityHostFixture(
	t *testing.T,
	spec capacityHostSpec,
	fixtureDir string,
) capacityHostFixtureEntry {
	t.Helper()
	recorder := &capacityHostRecorder{}
	workspaceRoot := filepath.Join(fixtureDir, "workspace")
	identity := createCapacityHostWorkspace(t, workspaceRoot)
	authorityPath := ensureCapacityHostAuthority(t, workspaceRoot, identity)
	dataDir := filepath.Join(workspaceRoot, ".vibetable", "data")
	ctx := context.Background()
	seedContent := []byte("task415-capacity-host-fixture-seed")

	// Phase 1: same-lease resume and one real Save through the genuine
	// Runtime product path.
	seedServices := newCapacityHostServices(t, dataDir)
	seedRuntime := openCapacityHostRuntime(t, seedServices, dataDir, identity)
	token, _ := seedRuntime.coordinator.Current()
	seedDocumentID := uuid.NewString()
	deviceID := uuid.NewString()
	seed, err := seedRuntime.history.Save(ctx, filehistory.SaveRequest{
		Token: token, DocumentID: seedDocumentID,
		Path:    "capacity/source/seed.txt",
		Kind:    filehistory.RevisionFormal,
		Content: seedContent, MimeType: "text/plain",
		CreatedBy: "capacity-fixture-producer", DeviceID: deviceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := seedRuntime.repository.GetManifest(ctx, seed.Root)
	if err != nil {
		t.Fatal(err)
	}
	var sourceRoot capacityHostRootPayload
	if err := json.Unmarshal(record.Payload, &sourceRoot); err != nil {
		t.Fatal(err)
	}
	if len(sourceRoot.Documents) != 1 {
		t.Fatalf("seed root documents = %d", len(sourceRoot.Documents))
	}
	base := sourceRoot.Documents[0]

	documents := make([]filehistory.Document, 0, spec.documentTotal())
	multiDocumentID := ""
	multiFirstRevisionID := ""
	multiEffectiveRevisionID := ""
	for index := range spec.multis {
		documents = appendCapacityHostDocument(
			documents, base, spec.chain,
			fmt.Sprintf("capacity/%s/multi-%08d.txt", spec.name, index),
		)
		if index == 0 {
			document := documents[len(documents)-1]
			multiDocumentID = document.DocumentID
			multiFirstRevisionID = document.Revisions[0].RevisionID
			multiEffectiveRevisionID = document.EffectiveRevisionID
		}
	}
	for index := range spec.singles {
		documents = appendCapacityHostDocument(
			documents, base, 1,
			fmt.Sprintf("capacity/%s/single-%08d.txt", spec.name, index),
		)
	}
	payload, err := json.Marshal(capacityHostRootPayload{
		FormatVersion: capacityHostRootFormatVersion,
		WorkspaceID:   identity.WorkspaceID,
		Documents:     documents,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := seedRuntime.repository.Commit(ctx, objectrepo.CommitRequest{
		Authority: token.Authority(),
		Manifests: []objectrepo.ManifestInput{{
			Name: "filehistory-root",
			Labels: map[string]string{
				"type":        "filehistory-root",
				"workspaceId": identity.WorkspaceID,
			},
			Payload: payload,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	derivedRoot := receipt.Manifests["filehistory-root"]
	if derivedRoot == "" {
		t.Fatal("derived capacity root manifest missing")
	}

	// Phase 2: publish the derived root exactly the way Service.commit does,
	// all outside any measured window: inside one coordinator write intent
	// the real Materializer prepares/applies the complete derived file state,
	// the audited head CAS runs with the intent's identity, and finalize
	// removes the journal after the head is durable. No extra roots, saves,
	// direct files/ writes, or forged intents are involved.
	head, found, err := seedRuntime.headStore.Load(ctx, identity.WorkspaceID)
	if err != nil || !found {
		t.Fatalf("seed head missing: found=%v err=%v", found, err)
	}
	seedMaterialized := filepath.Join(
		workspaceRoot, "files",
		filepath.FromSlash("capacity/source/seed.txt"),
	)
	if info, statErr := os.Stat(seedMaterialized); statErr != nil ||
		info.Size() != int64(len(seedContent)) {
		t.Fatalf(
			"seed materialized file = %q stat=%v size=%d",
			seedMaterialized, statErr, info.Size(),
		)
	}
	currentDocuments := make(
		map[string]filehistory.Document, len(sourceRoot.Documents),
	)
	for _, document := range sourceRoot.Documents {
		currentDocuments[document.DocumentID] = document
	}
	derivedDocuments := make(
		map[string]filehistory.Document, len(documents),
	)
	for _, document := range documents {
		derivedDocuments[document.DocumentID] = document
	}
	if seedRuntime.materializer == nil {
		t.Fatal("runtime materializer missing for derived materialization")
	}
	var derivedHead filehistory.CurrentHead
	expandReceipt, err := seedRuntime.coordinator.Write(
		ctx, token,
		func(ctx context.Context, intent writecoordinator.WriteIntent) error {
			next := head
			next.Root = derivedRoot
			next.Revision = head.Revision + 1
			next.MutationRevision = intent.MutationRevision
			next.SessionEpoch = intent.Token.SessionEpoch
			next.FenceEpoch = intent.Token.FenceEpoch
			next.ClaimID = intent.Token.ClaimID
			if err := seedRuntime.materializer.PrepareAndApply(
				ctx, intent, currentDocuments, derivedDocuments,
			); err != nil {
				return err
			}
			auditPayload, err := json.Marshal(map[string]any{
				"type":             "fileHistory.headPublished",
				"workspaceId":      intent.Token.WorkspaceID,
				"mutationRevision": intent.MutationRevision,
				"previousRoot":     head.Root,
				"root":             next.Root,
				"headRevision":     next.Revision,
				"documentCount":    spec.documentTotal(),
			})
			if err != nil {
				return err
			}
			envelope, err := auditledger.NewEnvelope(
				fmt.Sprintf(
					"filehistory-head:%s:%020d",
					intent.Token.WorkspaceID, next.Revision,
				),
				"filehistory:"+intent.Token.WorkspaceID,
				next.Revision,
				fmt.Sprintf(
					"filehistory:%s:%d:%d:%s:%d",
					intent.Token.WorkspaceID,
					next.SessionEpoch, next.FenceEpoch,
					next.ClaimID, intent.MutationRevision,
				),
				auditPayload,
				time.Now().UTC(),
			)
			if err != nil {
				return err
			}
			if _, err := seedRuntime.headStore.CompareAndSwapWithAudit(
				ctx, head, next, envelope,
			); err != nil {
				return errors.Join(
					err,
					seedRuntime.materializer.Rollback(intent.MutationRevision),
				)
			}
			derivedHead = next
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedRuntime.materializer.Finalize(
		expandReceipt.MutationRevision,
	); err != nil {
		t.Fatal(err)
	}
	// The expansion must leave a fully materialized stable root: journal
	// gone, stale seed leaf removed, every derived document on disk with the
	// expected size (the materializer itself verified content against the
	// authoritative objects; no extra hashing here).
	journalPath := filepath.Join(
		workspaceRoot, ".vibetable", "coordination",
		"file-materializer", "journal.json",
	)
	if _, statErr := os.Lstat(journalPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("materializer journal still present: %v", statErr)
	}
	if _, statErr := os.Lstat(seedMaterialized); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stale seed leaf still present: %v", statErr)
	}
	expectedLeafSize := documents[0].Revisions[0].Size
	materializedFiles := 0
	for _, document := range documents {
		leafPath := filepath.Join(
			workspaceRoot, "files",
			filepath.FromSlash(document.RelativePath),
		)
		info, statErr := os.Stat(leafPath)
		if statErr != nil || !info.Mode().IsRegular() ||
			info.Size() != expectedLeafSize {
			t.Fatalf(
				"derived leaf %q missing or wrong size: stat=%v",
				leafPath, statErr,
			)
		}
		materializedFiles++
	}
	if materializedFiles != spec.documentTotal() {
		t.Fatalf(
			"%s materialized files = %d, want %d",
			spec.name, materializedFiles, spec.documentTotal(),
		)
	}
	sampleLeaf := filepath.Join(
		workspaceRoot, "files",
		filepath.FromSlash(documents[0].RelativePath),
	)
	sampleBytes, err := os.ReadFile(sampleLeaf)
	if err != nil || !bytes.Equal(sampleBytes, seedContent) {
		t.Fatalf("derived leaf %q bytes mismatch: err=%v", sampleLeaf, err)
	}
	closeCapacityHostRuntime(t, seedRuntime, seedServices)

	// Phase 3: measured verified cold reopen at the derived head, then the
	// real readTree projection + JSON serialization and page seams.
	profileDir := ""
	if spec.profile {
		repositoryRoot, rootErr := capacityHostRepositoryRoot()
		if rootErr != nil {
			t.Fatal(rootErr)
		}
		// Bind profiles to the single fresh run: fixtureDir is
		// <run-target>/<fixture-name>, so its parent basename is the unique
		// run directory. Stage names may repeat inside a run, but a different
		// run can never overwrite another run's raw profiles.
		runName := filepath.Base(filepath.Dir(fixtureDir))
		profileDir = filepath.Join(
			repositoryRoot, "build", "qa", "415-capacity", "profiles", runName,
		)
		if err := os.MkdirAll(profileDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	reopenServices := newCapacityHostServices(t, dataDir)
	var reopened *Runtime
	recorder.measure(t, spec.name, "runtimeReopen",
		spec.documentTotal(), spec.revisionTotal(),
		capacityHostSaveOpenLimit,
		profileDir,
		"verified cold workspacev2.Open at the derived legal head (same-lease resume)",
		func() error {
			runtimeInstance, openErr := Open(ctx, Options{
				App: reopenServices.app, DataDir: dataDir,
				WorkspaceID:            identity.WorkspaceID,
				SessionEpoch:           identity.SessionEpoch,
				FenceEpoch:             identity.FenceEpoch,
				ClaimID:                identity.ClaimID,
				Ledger:                 reopenServices.ledger,
				Audit:                  reopenServices.audit,
				DeferBackgroundWorkers: true,
			})
			reopened = runtimeInstance
			return openErr
		},
	)
	if listed := len(reopened.history.List()); listed != spec.documentTotal() {
		t.Fatalf(
			"%s reopened documents = %d, want %d",
			spec.name, listed, spec.documentTotal(),
		)
	}
	reopenedHead, found, err := reopened.headStore.Load(ctx, identity.WorkspaceID)
	if err != nil || !found ||
		reopenedHead.Root != derivedRoot ||
		reopenedHead.Revision != derivedHead.Revision {
		t.Fatalf(
			"%s reopened head = %#v found=%v err=%v, want root %s revision %d",
			spec.name, reopenedHead, found, err, derivedRoot, derivedHead.Revision,
		)
	}
	multiDocument, err := reopened.history.Inspect(multiDocumentID)
	if err != nil || len(multiDocument.Revisions) != spec.chain ||
		multiDocument.EffectiveRevisionID != multiEffectiveRevisionID ||
		multiDocument.Revisions[0].RevisionID != multiFirstRevisionID {
		t.Fatalf(
			"%s reopened multi document = %#v, err = %v",
			spec.name, multiDocument, err,
		)
	}

	params, err := json.Marshal(map[string]string{
		"documentId": multiDocumentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var treeJSON []byte
	recorder.measure(t, spec.name, "readTreeProjection+JSON",
		spec.documentTotal(), spec.revisionTotal(),
		capacityHostPageLimit,
		"",
		"workspacev2 readFileTree projection plus json.Marshal; in-process only, excludes RPC transport and UI rendering",
		func() error {
			tree, treeErr := reopened.readFileTree(ctx, nil, params)
			if treeErr != nil {
				return treeErr
			}
			treeJSON, treeErr = json.Marshal(tree)
			return treeErr
		},
	)
	var treeProjection struct {
		Revisions []map[string]any `json:"revisions"`
		Effective *string          `json:"effectiveRevisionId"`
	}
	if err := json.Unmarshal(treeJSON, &treeProjection); err != nil {
		t.Fatal(err)
	}
	if len(treeProjection.Revisions) != spec.chain ||
		treeProjection.Effective == nil ||
		*treeProjection.Effective != multiEffectiveRevisionID {
		t.Fatalf(
			"%s readTree projection revisions=%d effective=%v",
			spec.name, len(treeProjection.Revisions), treeProjection.Effective,
		)
	}

	expectedPage := min(50, spec.documentTotal())
	recorder.measure(t, spec.name, "queryDocumentsPage",
		spec.documentTotal(), spec.revisionTotal(),
		capacityHostPageLimit,
		"",
		"in-process filehistory Service.QueryDocuments first page; excludes RPC framing",
		func() error {
			page, pageErr := reopened.history.QueryDocuments(
				filehistory.DocumentQueryRequest{Logic: "and", Limit: 50},
			)
			if pageErr != nil {
				return pageErr
			}
			if len(page.Documents) != expectedPage ||
				(spec.documentTotal() > 50 && page.NextCursor == nil) {
				return fmt.Errorf(
					"page contract: documents=%d cursor=%v",
					len(page.Documents), page.NextCursor,
				)
			}
			return nil
		},
	)

	finalRoot := derivedRoot
	finalHead := derivedHead
	if spec.grow {
		grownDocumentID := uuid.NewString()
		var grown filehistory.SaveResult
		recorder.measure(t, spec.name, "save",
			spec.documentTotal(), spec.revisionTotal(),
			capacityHostSaveOpenLimit,
			profileDir,
			"real filehistory Service.Save at the near-limit head through the coordinator write gate",
			func() error {
				growToken, _ := reopened.coordinator.Current()
				result, growErr := reopened.history.Save(ctx, filehistory.SaveRequest{
					Token: growToken, DocumentID: grownDocumentID,
					Path:      fmt.Sprintf("capacity/%s/grown.txt", spec.name),
					Kind:      filehistory.RevisionFormal,
					Content:   []byte("task415-capacity-host-fixture-grown"),
					MimeType:  "text/plain",
					CreatedBy: "capacity-fixture-producer", DeviceID: deviceID,
				})
				grown = result
				return growErr
			},
		)
		finalRoot = grown.Root
	} else {
		recorder.measure(t, spec.name, "saveRejected",
			spec.documentTotal(), spec.revisionTotal(),
			capacityHostSaveOpenLimit,
			"",
			"over-chain-depth save rejected with filehistory.resource_limit before any repository commit",
			func() error {
				rejectToken, _ := reopened.coordinator.Current()
				_, rejectErr := reopened.history.Save(ctx, filehistory.SaveRequest{
					Token: rejectToken, DocumentID: multiDocumentID,
					Kind:      filehistory.RevisionFormal,
					Content:   []byte("capacity-overdepth-revision"),
					MimeType:  "text/plain",
					CreatedBy: "capacity-fixture-producer", DeviceID: deviceID,
				})
				if !errors.Is(rejectErr, filehistory.ErrResourceLimit) {
					return fmt.Errorf(
						"over-depth save error = %v, want filehistory.resource_limit",
						rejectErr,
					)
				}
				return nil
			},
		)
	}
	closeCapacityHostRuntime(t, reopened, reopenServices)

	// Phase 4: unmeasured final-state cold reopen proves the fixture the
	// Host will consume opens cleanly and is left fully closed afterwards.
	verifyServices := newCapacityHostServices(t, dataDir)
	verifyRuntime := openCapacityHostRuntime(
		t, verifyServices, dataDir, identity,
	)
	finalDocuments := spec.documentTotal()
	if spec.grow {
		finalDocuments++
	}
	if listed := len(verifyRuntime.history.List()); listed != finalDocuments {
		t.Fatalf(
			"%s final documents = %d, want %d",
			spec.name, listed, finalDocuments,
		)
	}
	finalHead, found, err = verifyRuntime.headStore.Load(ctx, identity.WorkspaceID)
	if err != nil || !found || finalHead.Root != finalRoot {
		t.Fatalf(
			"%s final head = %#v found=%v err=%v, want root %s",
			spec.name, finalHead, found, err, finalRoot,
		)
	}
	closeCapacityHostRuntime(t, verifyRuntime, verifyServices)

	// The shipped workspace must still bind the exact lease the Host will
	// inherit: coordination identity and authority file unchanged.
	if finalIdentity := readCapacityHostCoordinationIdentity(
		t, workspaceRoot,
	); finalIdentity != identity {
		t.Fatalf(
			"%s coordination identity drifted after expansion: %#v",
			spec.name, finalIdentity,
		)
	}
	if path := ensureCapacityHostAuthority(t, workspaceRoot, identity); path != authorityPath {
		t.Fatalf("%s authority file moved: %s", spec.name, path)
	}

	finalRevisions := spec.revisionTotal()
	if spec.grow {
		finalRevisions++
	}
	entry := capacityHostFixtureEntry{
		Name:                             spec.name,
		WorkspaceRoot:                    workspaceRoot,
		DataDir:                          dataDir,
		FilesRoot:                        filepath.Join(workspaceRoot, "files"),
		ManifestPath:                     filepath.Join(workspaceRoot, ".vibetable", "workspace.json"),
		AuthorityFile:                    authorityPath,
		Identity:                         identity,
		StorageMode:                      "direct",
		EncryptionMode:                   "convenient",
		RepositoryFormat:                 "kopia-v4",
		Documents:                        finalDocuments,
		Revisions:                        finalRevisions,
		SelectedMultiDocumentID:          multiDocumentID,
		SelectedMultiFirstRevisionID:     multiFirstRevisionID,
		SelectedMultiEffectiveRevisionID: multiEffectiveRevisionID,
		SelectedMultiRevisionCount:       spec.chain,
		SelectedMultiRelativePath:        multiDocument.RelativePath,
		SelectedMultiStatus:              string(multiDocument.Status),
		HeadRoot:                         string(finalRoot),
		HeadRevision:                     finalHead.Revision,
		HeadMutationRevision:             finalHead.MutationRevision,
		SeedRoot:                         string(seed.Root),
		SeedDocumentID:                   seedDocumentID,
		SeedRevisionID:                   seed.Revision.RevisionID,
		SeedObjectID:                     string(seed.Revision.ObjectID),
		SeedContentHash:                  seed.Revision.ContentHash,
		SeedMaterializedFile:             seedMaterialized,
		SeedMaterializedBytes:            int64(len(seedContent)),
	}
	t.Logf(
		"capacity-host fixture %s: workspace=%s documents=%d revisions=%d head=%s",
		spec.name, workspaceRoot, entry.Documents, entry.Revisions, entry.HeadRoot,
	)
	writeCapacityHostFixtureMetadata(t, fixtureDir, entry, recorder.operations)
	return entry
}

// writeCapacityHostFixtureMetadata gives every fixture its own independent
// JSON root so the near-limit and depth-4096 workspaces can be regenerated
// and consumed separately by the real-Host qualification.
func writeCapacityHostFixtureMetadata(
	t *testing.T,
	fixtureDir string,
	entry capacityHostFixtureEntry,
	operations []capacityHostOperation,
) {
	t.Helper()
	metadata := capacityHostFixtureMetadata{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		GoVersion:   runtime.Version(),
		Producer: "go test -count=1 -run TestCapacityHostFixtureProduction ./internal/workspacev2 " +
			"(" + capacityHostFixtureEnv + "=<fresh path under build/qa/415-capacity>)",
		EnvVariable:     capacityHostFixtureEnv,
		OutputDirectory: fixtureDir,
		BudgetsApplied:  capacityHostFixtureBudgetsEnabled,
		Budgets: map[string]string{
			"readTreeProjectionAndPageWall": capacityHostPageLimit.String(),
			"runtimeReopenAndSaveWall":      capacityHostSaveOpenLimit.String(),
			"perOperationTotalAlloc": fmt.Sprintf(
				"%d bytes", capacityHostTotalAllocLimit,
			),
		},
		HostRequirements: []string{
			"the workspace ships .vibetable/coordination/desktop-runtime-authority.json binding the exact fence/claim and the LastSessionEpoch floor of the persisted coordination lease; the real Host reads it (DesktopWorkspaceAuthorityStore.TryRead/Prepare), reuses the same claim/fence, advances the session epoch strictly (WorkspaceSessionManager raises its floor via ReadLastSessionEpoch), and the sidecar resumes the coordination database with that identity — no manual identity parameters are involved",
			"the real sidecar bootstraps PocketBase inside the fixture dataDir and applies migrations; the producer only pre-created an idempotent IF NOT EXISTS audit outbox table",
			"first screen reads fileHistory.queryDocuments / fileHistory.readTree over the same RPC surface the product uses",
		},
		Boundaries: []string{
			"no actual WPF/WebView2 Host was started; cold-open compatibility is proven only by the verified same-lease Runtime reopen inside this producer, not by the real shell",
			"the lease was created by this Go producer with natural first-writer values (fence 1, fresh claim, session 1) plus the consumer-format authority file; Host takeover follows the same file-based contract but remains unverified with the actual WPF process",
			"RPC transport, WebView rendering, and real migration application timing are not measured here",
			"readTreeProjection+JSON measures the workspacev2 projection and serialization only",
			"the derived root expands one real validated save; the real-save accumulation path to the cap was not executed",
			"the depth chain is all-formal (V1..V4096) for the real-Host all-formal first-screen case",
		},
		Fixtures:   []capacityHostFixtureEntry{entry},
		Operations: operations,
	}
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(fixtureDir, "fixture.json")
	if err := os.WriteFile(metadataPath, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("capacity host fixture metadata written to %s", metadataPath)
}

// TestCapacityHostFixtureProduction is the explicit opt-in producer. A normal
// `go test` run skips it; set VIBETABLE_415_CAPACITY_HOST_FIXTURE_ROOT to a
// fresh path under build/qa/415-capacity/ to generate the synthetic Host
// fixture workspaces and their per-fixture metadata JSON.
func TestCapacityHostFixtureProduction(t *testing.T) {
	configured := strings.TrimSpace(os.Getenv(capacityHostFixtureEnv))
	if configured == "" {
		t.Skipf(
			"opt-in only: set %s to a fresh path under build/qa/415-capacity/",
			capacityHostFixtureEnv,
		)
	}
	repositoryRoot, err := capacityHostRepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	allowedBase := filepath.Join(repositoryRoot, "build", "qa", "415-capacity")
	target, err := resolveCapacityHostFixtureTarget(configured, allowedBase)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	produceCapacityHostFixture(
		t,
		capacityHostSpec{
			name: "near-limit-9980x9990",
			// One multi-version document (11 revisions) plus single-revision
			// documents share the root quota: 9_979 documents / 9_989
			// revisions before the final real save ships the fixture at
			// exactly 9_980 documents / 9_990 revisions.
			singles: 9_978, multis: 1, chain: 11, grow: true, profile: true,
		},
		filepath.Join(target, "near-limit-9980x9990"),
	)
	produceCapacityHostFixture(
		t,
		capacityHostSpec{
			name:    "depth-4096",
			singles: 0, multis: 1, chain: filehistory.MaxRevisionChainDepth,
		},
		filepath.Join(target, "depth-4096"),
	)
}
