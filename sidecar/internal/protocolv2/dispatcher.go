package protocolv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"

	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
)

var (
	ErrMethodNotFound    = errors.New("workspace.method_not_found")
	ErrScopeRequired     = errors.New("workspace.scope_required")
	ErrStaleSession      = errors.New("workspace.session_epoch_stale")
	ErrSequenceStale     = errors.New("workspace.sequence_stale")
	ErrOperationConflict = errors.New("workspace.operation_conflict")
)

var publicErrorCode = regexp.MustCompile(`^[a-z][a-z0-9]*(?:\.[a-z0-9_]+)+$`)

type ScopeKind string

const (
	GlobalScope    ScopeKind = "global"
	WorkspaceScope ScopeKind = "workspace"
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Wire    json.RawMessage `json:"wire"`
	Params  json.RawMessage `json:"params"`
}

type Handler func(context.Context, json.RawMessage, json.RawMessage) (any, error)

type registration struct {
	scope   ScopeKind
	handler Handler
}

type Session struct {
	WorkspaceID string
	Epoch       uint64
	Sequence    uint64
}

type Dispatcher struct {
	mu                     sync.RWMutex
	methods                map[string]registration
	session                Session
	commitSequence         func(context.Context, Session) error
	loadOperation          func(context.Context, string, string) (OperationReceipt, bool, error)
	loadAuthorityOperation func(
		context.Context,
		string,
		string,
	) (OperationReceipt, bool, error)
	commitOperation    func(context.Context, Session, OperationReceipt) error
	workspaceSuspended atomic.Bool
}

type OperationReceipt struct {
	OperationID string
	WorkspaceID string
	Method      string
	Scope       ScopeKind
	RequestHash string
	Result      json.RawMessage
}

// OperationContext is the immutable identity of a dispatched mutating RPC.
// Authorities use it to persist the exact result in the same transaction (or
// durable journal) that publishes their visible side effect. The generic
// workspace receipt database is only a replay cache and must not be the sole
// exactly-once boundary.
type OperationContext struct {
	OperationID string
	WorkspaceID string
	Method      string
	Scope       ScopeKind
	RequestHash string
	Session     Session
}

type operationContextKey struct{}

// OperationFromContext returns the identity injected by Dispatcher before a
// handler runs. It is intentionally unavailable to calls that bypass Dispatch.
func OperationFromContext(ctx context.Context) (OperationContext, bool) {
	value, ok := ctx.Value(operationContextKey{}).(OperationContext)
	return value, ok
}

// BuildContextOperationReceipt builds the exact receipt an authority must
// commit with its visible mutation.
func BuildContextOperationReceipt(
	ctx context.Context,
	result any,
) (OperationReceipt, error) {
	operation, ok := OperationFromContext(ctx)
	if !ok {
		return OperationReceipt{}, errors.New("workspace.operation_context_required")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return OperationReceipt{}, err
	}
	return OperationReceipt{
		OperationID: operation.OperationID,
		WorkspaceID: operation.WorkspaceID,
		Method:      operation.Method,
		Scope:       operation.Scope,
		RequestHash: operation.RequestHash,
		Result:      raw,
	}, nil
}

func New() *Dispatcher {
	return &Dispatcher{methods: map[string]registration{}}
}

func (dispatcher *Dispatcher) BindSession(session Session) {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	dispatcher.session = session
}

// SetSequenceCommit installs the durable publication boundary for a successful
// workspace-scoped request. The callback runs before the in-memory high-water
// mark advances, so a persistence failure never falsely acknowledges a
// sequence.
func (dispatcher *Dispatcher) SetSequenceCommit(
	commit func(context.Context, Session) error,
) {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	dispatcher.commitSequence = commit
}

func (dispatcher *Dispatcher) SetOperationReceiptStore(
	load func(context.Context, string, string) (OperationReceipt, bool, error),
	commit func(context.Context, Session, OperationReceipt) error,
) {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	dispatcher.loadOperation = load
	dispatcher.commitOperation = commit
}

// SetAuthorityOperationReceiptStore installs the recovery lookup for receipts
// committed by the actual authority (for example the file-history head,
// snapshot catalog, or a restore journal). It is consulted only when the
// generic replay cache misses.
func (dispatcher *Dispatcher) SetAuthorityOperationReceiptStore(
	load func(
		context.Context,
		string,
		string,
	) (OperationReceipt, bool, error),
) {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	dispatcher.loadAuthorityOperation = load
}

// SuspendWorkspace fails closed for every subsequent workspace-scoped RPC.
// It is lock-free so a handler may suspend dispatch after durably preparing a
// process-restart operation while Dispatch owns the registry mutex.
func (dispatcher *Dispatcher) SuspendWorkspace() {
	dispatcher.workspaceSuspended.Store(true)
}

func (dispatcher *Dispatcher) Register(method string, scope ScopeKind, handler Handler) {
	dispatcher.mu.Lock()
	defer dispatcher.mu.Unlock()
	if _, exists := dispatcher.methods[method]; exists {
		panic("duplicate protocol v2 method: " + method)
	}
	dispatcher.methods[method] = registration{scope: scope, handler: handler}
}

type Method struct {
	Name  string    `json:"method"`
	Scope ScopeKind `json:"scope"`
}

func (dispatcher *Dispatcher) Methods() []Method {
	dispatcher.mu.RLock()
	defer dispatcher.mu.RUnlock()
	result := make([]Method, 0, len(dispatcher.methods))
	for name, registration := range dispatcher.methods {
		result = append(result, Method{Name: name, Scope: registration.scope})
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Name < result[right].Name
	})
	return result
}

func (dispatcher *Dispatcher) Dispatch(ctx context.Context, raw []byte) (any, error) {
	var request Request
	if err := decodeStrict(raw, &request); err != nil || request.JSONRPC != "2.0" ||
		request.ID == "" || request.Method == "" || len(request.Wire) == 0 ||
		len(request.Params) == 0 {
		return nil, errors.New("workspace.request_invalid")
	}
	dispatcher.mu.Lock()
	method, ok := dispatcher.methods[request.Method]
	session := dispatcher.session
	if !ok {
		dispatcher.mu.Unlock()
		return nil, ErrMethodNotFound
	}
	switch method.scope {
	case GlobalScope:
		var scope contractsv2.GlobalWireScope
		if err := decodeStrict(request.Wire, &scope); err != nil || scope.Validate() != nil {
			dispatcher.mu.Unlock()
			return nil, ErrScopeRequired
		}
		receipt, replay, err := dispatcher.lookupOperation(
			ctx,
			request,
			method.scope,
			session.WorkspaceID,
			scope.OperationID,
			0,
		)
		if err != nil {
			dispatcher.mu.Unlock()
			return nil, err
		}
		if replay {
			if dispatcher.commitOperation != nil {
				if err := dispatcher.commitOperation(
					ctx,
					session,
					receipt,
				); err != nil {
					dispatcher.mu.Unlock()
					return nil, err
				}
			}
			dispatcher.mu.Unlock()
			return decodeOperationResult(receipt.Result)
		}
		handlerContext, err := dispatcher.operationContext(
			ctx,
			request,
			method.scope,
			session,
			scope.OperationID,
		)
		if err != nil {
			dispatcher.mu.Unlock()
			return nil, err
		}
		result, err := method.handler(
			handlerContext,
			request.Wire,
			request.Params,
		)
		if err == nil {
			receipt, receiptErr := operationReceipt(
				request,
				method.scope,
				session.WorkspaceID,
				scope.OperationID,
				0,
				result,
			)
			if receiptErr != nil {
				dispatcher.mu.Unlock()
				return nil, receiptErr
			}
			if dispatcher.commitOperation != nil {
				err = dispatcher.commitOperation(ctx, session, receipt)
			}
		}
		dispatcher.mu.Unlock()
		return result, err
	case WorkspaceScope:
		if dispatcher.workspaceSuspended.Load() {
			dispatcher.mu.Unlock()
			return nil, errors.New("workspace.restore_pending")
		}
		var scope contractsv2.WorkspaceWireScope
		if err := decodeStrict(request.Wire, &scope); err != nil || scope.Validate() != nil {
			dispatcher.mu.Unlock()
			return nil, ErrScopeRequired
		}
		if scope.WorkspaceID != session.WorkspaceID || scope.SessionEpoch != session.Epoch {
			dispatcher.mu.Unlock()
			return nil, ErrStaleSession
		}
		receipt, replay, err := dispatcher.lookupOperation(
			ctx,
			request,
			method.scope,
			scope.WorkspaceID,
			scope.OperationID,
			scope.SessionEpoch,
		)
		if err != nil {
			dispatcher.mu.Unlock()
			return nil, err
		}
		if replay {
			if scope.Sequence > session.Sequence {
				next := dispatcher.session
				next.Sequence = scope.Sequence
				if dispatcher.commitOperation != nil {
					if err := dispatcher.commitOperation(
						ctx,
						next,
						receipt,
					); err != nil {
						dispatcher.mu.Unlock()
						return nil, err
					}
				} else if dispatcher.commitSequence != nil {
					if err := dispatcher.commitSequence(
						ctx,
						next,
					); err != nil {
						dispatcher.mu.Unlock()
						return nil, err
					}
				}
				dispatcher.session = next
			}
			dispatcher.mu.Unlock()
			return decodeOperationResult(receipt.Result)
		}
		if scope.Sequence <= session.Sequence {
			dispatcher.mu.Unlock()
			return nil, ErrSequenceStale
		}
		nextSession := session
		nextSession.Sequence = scope.Sequence
		handlerContext, err := dispatcher.operationContext(
			ctx,
			request,
			method.scope,
			nextSession,
			scope.OperationID,
		)
		if err != nil {
			dispatcher.mu.Unlock()
			return nil, err
		}
		result, err := method.handler(
			handlerContext,
			request.Wire,
			request.Params,
		)
		if err == nil {
			next := dispatcher.session
			next.Sequence = scope.Sequence
			receipt, receiptErr := operationReceipt(
				request,
				method.scope,
				scope.WorkspaceID,
				scope.OperationID,
				scope.SessionEpoch,
				result,
			)
			if receiptErr != nil {
				dispatcher.mu.Unlock()
				return nil, receiptErr
			}
			if dispatcher.commitOperation != nil {
				if persistErr := dispatcher.commitOperation(
					ctx,
					next,
					receipt,
				); persistErr != nil {
					dispatcher.mu.Unlock()
					return nil, persistErr
				}
			} else if dispatcher.commitSequence != nil {
				if persistErr := dispatcher.commitSequence(ctx, next); persistErr != nil {
					dispatcher.mu.Unlock()
					return nil, persistErr
				}
			}
			dispatcher.session = next
		}
		dispatcher.mu.Unlock()
		return result, err
	default:
		dispatcher.mu.Unlock()
		return nil, ErrScopeRequired
	}
}

func (dispatcher *Dispatcher) lookupOperation(
	ctx context.Context,
	request Request,
	scope ScopeKind,
	workspaceID string,
	operationID string,
	sessionEpoch uint64,
) (OperationReceipt, bool, error) {
	if dispatcher.loadOperation == nil &&
		dispatcher.loadAuthorityOperation == nil {
		return OperationReceipt{}, false, nil
	}
	expectedHash, err := operationRequestHash(
		request.Method,
		scope,
		workspaceID,
		sessionEpoch,
		request.Params,
	)
	if err != nil {
		return OperationReceipt{}, false, err
	}
	var (
		receipt OperationReceipt
		found   bool
	)
	if dispatcher.loadOperation != nil {
		receipt, found, err = dispatcher.loadOperation(
			ctx,
			workspaceID,
			operationID,
		)
		if err != nil {
			return OperationReceipt{}, false, err
		}
	}
	if !found && dispatcher.loadAuthorityOperation != nil {
		receipt, found, err = dispatcher.loadAuthorityOperation(
			ctx,
			workspaceID,
			operationID,
		)
		if err != nil {
			return OperationReceipt{}, false, err
		}
	}
	if !found {
		return OperationReceipt{}, false, nil
	}
	if receipt.OperationID != operationID ||
		receipt.WorkspaceID != workspaceID ||
		receipt.Method != request.Method ||
		receipt.Scope != scope ||
		receipt.RequestHash != expectedHash {
		return OperationReceipt{}, false, ErrOperationConflict
	}
	return receipt, true, nil
}

func (dispatcher *Dispatcher) operationContext(
	ctx context.Context,
	request Request,
	scope ScopeKind,
	session Session,
	operationID string,
) (context.Context, error) {
	sessionEpoch := session.Epoch
	if scope == GlobalScope {
		sessionEpoch = 0
	}
	hash, err := operationRequestHash(
		request.Method,
		scope,
		session.WorkspaceID,
		sessionEpoch,
		request.Params,
	)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, operationContextKey{}, OperationContext{
		OperationID: operationID,
		WorkspaceID: session.WorkspaceID,
		Method:      request.Method,
		Scope:       scope,
		RequestHash: hash,
		Session:     session,
	}), nil
}

func operationReceipt(
	request Request,
	scope ScopeKind,
	workspaceID string,
	operationID string,
	sessionEpoch uint64,
	result any,
) (OperationReceipt, error) {
	hash, err := operationRequestHash(
		request.Method,
		scope,
		workspaceID,
		sessionEpoch,
		request.Params,
	)
	if err != nil {
		return OperationReceipt{}, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return OperationReceipt{}, err
	}
	return OperationReceipt{
		OperationID: operationID,
		WorkspaceID: workspaceID,
		Method:      request.Method,
		Scope:       scope,
		RequestHash: hash,
		Result:      raw,
	}, nil
}

// BuildOperationReceipt lets a durable authority journal persist the exact
// result before a restart-bound handler acknowledges success.
func BuildOperationReceipt(
	method string,
	scope ScopeKind,
	workspaceID string,
	operationID string,
	params json.RawMessage,
	result any,
) (OperationReceipt, error) {
	return BuildOperationReceiptForSession(
		method,
		scope,
		workspaceID,
		operationID,
		0,
		params,
		result,
	)
}

// BuildOperationReceiptForSession is retained for durable authorities that
// prepare a restart journal outside the dispatcher handler context.
func BuildOperationReceiptForSession(
	method string,
	scope ScopeKind,
	workspaceID string,
	operationID string,
	sessionEpoch uint64,
	params json.RawMessage,
	result any,
) (OperationReceipt, error) {
	return operationReceipt(Request{
		Method: method,
		Params: params,
	}, scope, workspaceID, operationID, sessionEpoch, result)
}

func operationRequestHash(
	method string,
	scope ScopeKind,
	workspaceID string,
	sessionEpoch uint64,
	params json.RawMessage,
) (string, error) {
	var value any
	if err := json.Unmarshal(params, &value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(struct {
		Method       string    `json:"method"`
		Scope        ScopeKind `json:"scope"`
		WorkspaceID  string    `json:"workspaceId"`
		SessionEpoch uint64    `json:"sessionEpoch"`
		Params       any       `json:"params"`
	}{
		Method:       method,
		Scope:        scope,
		WorkspaceID:  workspaceID,
		SessionEpoch: sessionEpoch,
		Params:       value,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func decodeOperationResult(raw json.RawMessage) (any, error) {
	var result any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

type ErrorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details"`
	Retryable bool           `json:"retryable"`
}

type ResponseEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Wire    json.RawMessage `json:"wire"`
	Result  any             `json:"result,omitempty"`
	Error   *ErrorBody      `json:"error,omitempty"`
}

// DispatchEnvelope always echoes the request id and wire when the outer JSON
// object contains well-formed values for them. Strict Dispatch validation still
// decides whether the request is accepted.
func (dispatcher *Dispatcher) DispatchEnvelope(
	ctx context.Context,
	raw []byte,
) ResponseEnvelope {
	id, wire := responseIdentity(raw)
	result, err := dispatcher.Dispatch(ctx, raw)
	response := ResponseEnvelope{
		JSONRPC: "2.0",
		ID:      id,
		Wire:    wire,
	}
	if err == nil {
		response.Result = result
		return response
	}
	response.Error = errorBody(err)
	return response
}

func responseIdentity(raw []byte) (string, json.RawMessage) {
	var partial struct {
		ID   string          `json:"id"`
		Wire json.RawMessage `json:"wire"`
	}
	if json.Unmarshal(raw, &partial) != nil {
		return "", json.RawMessage("null")
	}
	if len(partial.Wire) == 0 {
		partial.Wire = json.RawMessage("null")
	}
	return partial.ID, partial.Wire
}

func errorBody(err error) *ErrorBody {
	code := err.Error()
	retryable := false
	switch {
	case errors.Is(err, ErrMethodNotFound):
		code = ErrMethodNotFound.Error()
	case errors.Is(err, ErrScopeRequired):
		code = ErrScopeRequired.Error()
	case errors.Is(err, ErrStaleSession):
		code = ErrStaleSession.Error()
	case errors.Is(err, ErrSequenceStale):
		code = ErrSequenceStale.Error()
	case errors.Is(err, ErrOperationConflict):
		code = ErrOperationConflict.Error()
	}
	if !publicErrorCode.MatchString(code) {
		code = "workspace.internal_failed"
		retryable = true
	}
	return &ErrorBody{
		Code:      code,
		Message:   "workspace v2 request failed",
		Details:   map[string]any{},
		Retryable: retryable,
	}
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}
