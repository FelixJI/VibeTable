package app

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"sync"
	"time"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

// importPlanTTL keeps the frozen public contract: a previewed import plan may
// be applied for ten minutes after preview and the window cannot be extended.
const importPlanTTL = 600 * time.Second

const maxImportPlanRows = 1000

var importPlanTokenPattern = regexp.MustCompile(`^imp1\.[A-Za-z0-9_-]{30,48}$`)

// importPlanContract is the frozen wire contract for the plan lifecycle ports.
const importPlanContract = "vibetable.import-plans.v1"

func importPlanError(code, message string, details map[string]any) error {
	return &v2.ProductError{Code: code, Message: message, Details: details}
}

type importPlanMintRequest struct {
	Contract       string            `json:"contract"`
	Collection     string            `json:"collection"`
	GrantID        string            `json:"grantId"`
	SchemaRevision string            `json:"schemaRevision"`
	CapabilityHash string            `json:"capabilityHash"`
	SourceHash     string            `json:"sourceHash"`
	Mode           string            `json:"mode"`
	UpsertKey      *string           `json:"upsertKey"`
	Rows           []json.RawMessage `json:"rows"`
}

type importPlanTokenReply struct {
	Token     string  `json:"token"`
	ExpiresAt float64 `json:"expiresAt"`
	Consumed  bool    `json:"consumed"`
}

type importPlanStageRequest struct {
	Contract       string `json:"contract"`
	Token          string `json:"token"`
	GrantID        string `json:"grantId"`
	Collection     string `json:"collection"`
	Mode           string `json:"mode"`
	CapabilityHash string `json:"capabilityHash"`
}

type importPlanStagedReply struct {
	Collection     string            `json:"collection"`
	SchemaRevision string            `json:"schemaRevision"`
	SourceHash     string            `json:"sourceHash"`
	Mode           string            `json:"mode"`
	UpsertKey      *string           `json:"upsertKey"`
	Rows           []json.RawMessage `json:"rows"`
	// Attempt is the lease identity of this claim; the settling apply must
	// echo it so stale attempts cannot settle a newer claim.
	Attempt int64 `json:"attempt"`
}

type importPlanBindRequest struct {
	Contract          string `json:"contract"`
	Token             string `json:"token"`
	IdempotencyPrefix string `json:"idempotencyPrefix"`
	// Attempt is the lease identity returned by stage; binding is claim-scoped
	// so a delayed bind from an earlier attempt cannot touch a newer claim.
	Attempt int64 `json:"attempt"`
}

type importPlanSettleRequest struct {
	Contract string `json:"contract"`
	Token    string `json:"token"`
	Outcome  string `json:"outcome"`
	// Attempt is the lease identity returned by stage; only the current
	// claim's attempt may release itself with rejected/unknown.
	Attempt int64 `json:"attempt"`
}

type importStoredPlan struct {
	collection     string
	grantID        string
	schemaRevision string
	capabilityHash string
	sourceHash     string
	mode           string
	upsertKey      *string
	rows           []json.RawMessage
	workspaceID    string
	expiresAt      time.Time
	consumed       bool
	inFlight       bool
	// attempt identifies the current exclusive claim and is incremented on
	// every stage, so a delayed settle from an earlier attempt can neither
	// release nor overwrite a newer claim on the same token.
	attempt           int64
	idempotencyPrefix string
}

// importPlanOwner is the single authority for import plan tokens: minting,
// single-use consumption, concurrent staging and the idempotency-prefix
// binding. Python previews container files and executes one staged apply as a
// short-lived worker context; it never retains a parallel writable plan copy.
type importPlanOwner struct {
	workspaceID string
	now         func() time.Time
	mu          sync.Mutex
	plans       map[string]*importStoredPlan
}

func newImportPlanOwner(workspaceID string) *importPlanOwner {
	return &importPlanOwner{workspaceID: workspaceID, now: time.Now, plans: map[string]*importStoredPlan{}}
}

func importPlanToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "imp1." + base64.RawURLEncoding.EncodeToString(raw), nil
}

func (owner *importPlanOwner) mint(request importPlanMintRequest) (importPlanTokenReply, error) {
	var reply importPlanTokenReply
	if owner.workspaceID == "" {
		return reply, importPlanError("import_plan_invalid", "workspace identity is required", nil)
	}
	if request.Contract != importPlanContract {
		return reply, importPlanError("import_plan_invalid", "unknown import plan contract", nil)
	}
	if request.Collection == "" || request.GrantID == "" || request.SchemaRevision == "" ||
		request.CapabilityHash == "" || request.SourceHash == "" || request.Mode == "" {
		return reply, importPlanError("import_plan_invalid", "import plan binding is incomplete", nil)
	}
	if len(request.Rows) > maxImportPlanRows {
		return reply, importPlanError("import_plan_invalid", "import plan exceeds the atomic row limit", nil)
	}
	token, err := importPlanToken()
	if err != nil {
		return reply, err
	}
	expires := owner.now().Add(importPlanTTL)
	owner.mu.Lock()
	// Retired plans stay for the workspace process lifetime, exactly like the
	// pre-migration Python store: an expired token keeps answering
	// import_token_expired instead of collapsing to unknown.
	owner.plans[token] = &importStoredPlan{
		collection:     request.Collection,
		grantID:        request.GrantID,
		schemaRevision: request.SchemaRevision,
		capabilityHash: request.CapabilityHash,
		sourceHash:     request.SourceHash,
		mode:           request.Mode,
		upsertKey:      request.UpsertKey,
		rows:           request.Rows,
		workspaceID:    owner.workspaceID,
		expiresAt:      expires,
	}
	owner.mu.Unlock()
	return importPlanTokenReply{Token: token, ExpiresAt: float64(expires.UnixNano()) / 1e9, Consumed: false}, nil
}

// stage claims one apply of a plan. The claim is exclusive and carries a
// fresh attempt lease: a concurrent or abandoned in-flight stage cannot
// double-submit the same token, and only bind/settle carrying the current
// attempt (or the terminal committed truth) can change the claim. Validation,
// the inFlight/attempt transition and the staged reply snapshot happen in one
// critical section so no bind/settle can observe a half-issued claim.
func (owner *importPlanOwner) stage(request importPlanStageRequest) (importPlanStagedReply, error) {
	var reply importPlanStagedReply
	if request.Contract != importPlanContract {
		return reply, importPlanError("import_plan_invalid", "unknown import plan contract", nil)
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	stored := owner.plans[request.Token]
	if stored == nil || stored.workspaceID != owner.workspaceID {
		return reply, importPlanError("import_token_unknown", "import token not found", nil)
	}
	if !importPlanTokenPattern.MatchString(request.Token) {
		return reply, importPlanError("import_token_unknown", "import token not found", nil)
	}
	if !owner.now().Before(stored.expiresAt) {
		return reply, importPlanError("import_token_expired", "import token expired", nil)
	}
	if stored.consumed {
		return reply, importPlanError("import_token_consumed", "import token already used", nil)
	}
	if stored.inFlight {
		return reply, importPlanError("import_token_busy", "import token is already being applied", nil)
	}
	if stored.grantID != request.GrantID {
		return reply, importPlanError("import_grant_mismatch", "import token belongs to another grant", nil)
	}
	if stored.collection != request.Collection || stored.mode != request.Mode {
		return reply, importPlanError("import_plan_mismatch", "import target or mode changed since preview", nil)
	}
	if stored.capabilityHash != request.CapabilityHash {
		return reply, importPlanError("schema_mismatch", "schema changed since preview", nil)
	}
	stored.inFlight = true
	stored.attempt++
	return importPlanStagedReply{
		Collection:     stored.collection,
		SchemaRevision: stored.schemaRevision,
		SourceHash:     stored.sourceHash,
		Mode:           stored.mode,
		UpsertKey:      stored.upsertKey,
		Rows:           stored.rows,
		Attempt:        stored.attempt,
	}, nil
}

// bind fixes the idempotency prefix of a staged plan on first use; a later
// apply of the same token cannot swap it for a different prefix. The call is
// claim-scoped: only the current attempt lease may bind.
func (owner *importPlanOwner) bind(request importPlanBindRequest) (string, error) {
	if request.Contract != importPlanContract {
		return "", importPlanError("import_plan_invalid", "unknown import plan contract", nil)
	}
	if request.IdempotencyPrefix == "" {
		return "", importPlanError("import_plan_invalid", "idempotency prefix is required", nil)
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	stored := owner.plans[request.Token]
	if stored == nil || stored.workspaceID != owner.workspaceID || !importPlanTokenPattern.MatchString(request.Token) {
		return "", importPlanError("import_token_unknown", "import token not found", nil)
	}
	if !owner.now().Before(stored.expiresAt) {
		return "", importPlanError("import_token_expired", "import token expired", nil)
	}
	if stored.consumed {
		return "", importPlanError("import_token_consumed", "import token already used", nil)
	}
	if !stored.inFlight {
		return "", importPlanError("import_token_busy", "import token is not staged for apply", nil)
	}
	if stored.attempt != request.Attempt {
		return "", importPlanError(
			"import_plan_stale",
			"stale attempt cannot bind the current import plan claim",
			nil,
		)
	}
	if stored.idempotencyPrefix == "" {
		stored.idempotencyPrefix = request.IdempotencyPrefix
	} else if stored.idempotencyPrefix != request.IdempotencyPrefix {
		return "", importPlanError(
			"import_idempotency_mismatch",
			"import token is bound to a different idempotency prefix",
			nil,
		)
	}
	return stored.idempotencyPrefix + "-0", nil
}

// settle records the authoritative outcome of a staged apply:
//   - committed: terminal truth from any attempt of this token (the business
//     write is idempotent under the plan-level prefix); the plan is consumed
//     and can never be applied again. Repeating committed is idempotent.
//   - rejected / unknown: claim-scoped and require the current attempt lease;
//     a delayed settle from an earlier attempt cannot release a newer claim.
//     An unknown outcome is never replayed here — any retry must restage the
//     same token and prefix so the MutationKernel receipt decides.
func (owner *importPlanOwner) settle(request importPlanSettleRequest) (importPlanTokenReply, error) {
	var reply importPlanTokenReply
	if request.Contract != importPlanContract {
		return reply, importPlanError("import_plan_invalid", "unknown import plan contract", nil)
	}
	if request.Outcome != "committed" && request.Outcome != "rejected" && request.Outcome != "unknown" {
		return reply, importPlanError("import_plan_invalid", "unknown import plan settle outcome", nil)
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	reply.Token = request.Token
	stored := owner.plans[request.Token]
	if stored == nil || stored.workspaceID != owner.workspaceID || !importPlanTokenPattern.MatchString(request.Token) {
		return reply, importPlanError("import_token_unknown", "import token not found", nil)
	}
	reply.ExpiresAt = float64(stored.expiresAt.UnixNano()) / 1e9
	reply.Consumed = stored.consumed
	switch request.Outcome {
	case "committed":
		// A valid committed settle comes from an attempt that was actually
		// issued and had bound the plan-level idempotency prefix before the
		// business submission (bind always precedes submit in the frozen flow).
		// Pseudo-committed requests referencing an unissued attempt (0, or
		// beyond the current counter) or an unbound plan are rejected without
		// consuming the token; genuinely late committed settles from any issued
		// attempt remain terminal truth.
		if request.Attempt < 1 || request.Attempt > stored.attempt || stored.idempotencyPrefix == "" {
			return reply, importPlanError(
				"import_plan_invalid",
				"committed settle must reference an issued, bound import plan claim",
				nil,
			)
		}
		stored.consumed = true
		stored.inFlight = false
		reply.Consumed = true
	case "rejected", "unknown":
		if stored.consumed {
			// The terminal committed truth already wins; a late claim-scoped
			// settle cannot resurrect the plan.
			return reply, nil
		}
		if stored.inFlight && stored.attempt != request.Attempt {
			return reply, importPlanError(
				"import_plan_stale",
				"stale attempt cannot settle the current import plan claim",
				nil,
			)
		}
		stored.inFlight = false
	}
	return reply, nil
}
