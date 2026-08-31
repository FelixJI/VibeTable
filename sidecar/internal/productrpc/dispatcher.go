// Package productrpc implements the private Product JSON-RPC seam.
package productrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/contracts/productcapabilities"
	contractsv2 "github.com/vibetable/vibetable/sidecar/internal/contracts/v2"
)

const (
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

type Identity struct {
	WorkspaceID  string
	SessionEpoch uint64
	FenceEpoch   uint64
	ClaimID      string
}

type ParamsValidator func(json.RawMessage) error

type Handler func(context.Context, json.RawMessage) (any, error)

type Registration struct {
	Method         string
	Scope          productcapabilities.Scope
	ValidateParams ParamsValidator
	Handler        Handler
}

type Method struct {
	Method string                    `json:"method"`
	Scope  productcapabilities.Scope `json:"scope"`
}

type CapabilityDocument struct {
	ContractVersion string   `json:"contractVersion"`
	WorkspaceID     string   `json:"workspaceId"`
	SessionEpoch    uint64   `json:"sessionEpoch"`
	FenceEpoch      uint64   `json:"fenceEpoch"`
	ClaimID         string   `json:"claimId"`
	RPCMethods      []string `json:"rpcMethods"`
	Registrations   []Method `json:"registrations"`
}

type ErrorObject struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

type ResponseEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Wire    json.RawMessage `json:"wire"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ErrorObject    `json:"error,omitempty"`
}

type requestEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Wire    json.RawMessage `json:"wire"`
	Params  json.RawMessage `json:"params"`
}

type Dispatcher struct {
	identity Identity
	methods  map[string]Registration
}

func New(identity Identity, registrations ...Registration) (*Dispatcher, error) {
	return newDispatcher(
		identity,
		productcapabilities.CurrentOwnerRPCDescriptors(productcapabilities.GoSidecar),
		registrations,
	)
}

func newDispatcher(
	identity Identity,
	descriptors []productcapabilities.RPCDescriptor,
	registrations []Registration,
) (*Dispatcher, error) {
	if err := validateIdentity(identity); err != nil {
		return nil, err
	}
	expected := make(map[string]productcapabilities.Scope, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor.Method == "" ||
			(descriptor.Scope != productcapabilities.GlobalScope &&
				descriptor.Scope != productcapabilities.WorkspaceScope) {
			return nil, errors.New("product RPC descriptor is invalid")
		}
		if _, duplicate := expected[descriptor.Method]; duplicate {
			return nil, fmt.Errorf("duplicate Product RPC descriptor: %s", descriptor.Method)
		}
		expected[descriptor.Method] = descriptor.Scope
	}
	methods := make(map[string]Registration, len(registrations))
	for _, registration := range registrations {
		if registration.Method == "" || registration.ValidateParams == nil ||
			registration.Handler == nil {
			return nil, errors.New("product RPC registration is invalid")
		}
		if _, duplicate := methods[registration.Method]; duplicate {
			return nil, fmt.Errorf("duplicate Product RPC registration: %s", registration.Method)
		}
		methods[registration.Method] = registration
	}
	if len(methods) != len(expected) {
		return nil, errors.New("product RPC registrations do not match generated goSidecar policy")
	}
	for method, scope := range expected {
		registration, ok := methods[method]
		if !ok || registration.Scope != scope {
			return nil, errors.New("product RPC registrations do not match generated goSidecar policy")
		}
	}
	return &Dispatcher{identity: identity, methods: methods}, nil
}

func (dispatcher *Dispatcher) Methods() []Method {
	methods := make([]Method, 0, len(dispatcher.methods))
	for _, registration := range dispatcher.methods {
		methods = append(methods, Method{Method: registration.Method, Scope: registration.Scope})
	}
	sort.Slice(methods, func(left, right int) bool {
		return methods[left].Method < methods[right].Method
	})
	return methods
}

func (dispatcher *Dispatcher) Capabilities() CapabilityDocument {
	identity := dispatcher.identity
	registrations := dispatcher.Methods()
	methods := make([]string, len(registrations))
	for index, registration := range registrations {
		methods[index] = registration.Method
	}
	return CapabilityDocument{
		ContractVersion: contractsv2.ContractVersion,
		WorkspaceID:     identity.WorkspaceID,
		SessionEpoch:    identity.SessionEpoch,
		FenceEpoch:      identity.FenceEpoch,
		ClaimID:         identity.ClaimID,
		RPCMethods:      methods,
		Registrations:   registrations,
	}
}

func (dispatcher *Dispatcher) Dispatch(ctx context.Context, raw []byte) ResponseEnvelope {
	id, wire := responseIdentity(raw)
	request, err := decodeRequest(raw)
	if err != nil {
		return errorResponse(id, wire, CodeInvalidRequest, "Invalid Request", nil)
	}
	registration, ok := dispatcher.methods[request.Method]
	if !ok {
		return errorResponse(request.ID, request.Wire, CodeMethodNotFound, "Method not found", nil)
	}
	if !dispatcher.scopeIsCurrent(registration.Scope, request.Wire) {
		return errorResponse(request.ID, request.Wire, CodeInvalidRequest, "Invalid Request", nil)
	}
	validationError, panicked := callValidator(registration.ValidateParams, request.Params)
	if panicked {
		return errorResponse(request.ID, request.Wire, CodeInternalError, "Internal error", nil)
	}
	if validationError != nil {
		return errorResponse(request.ID, request.Wire, CodeInvalidParams, "Invalid params", nil)
	}
	result, err, panicked := callHandler(ctx, registration.Handler, request.Params)
	if panicked {
		return errorResponse(request.ID, request.Wire, CodeInternalError, "Internal error", nil)
	}
	if err != nil {
		if data, public := productErrorData(err); public {
			return errorResponse(
				request.ID,
				request.Wire,
				CodeProductData,
				"Product data error",
				data,
			)
		}
		return errorResponse(request.ID, request.Wire, CodeInternalError, "Internal error", nil)
	}
	serialized, err := safeJSONMarshal(result)
	if err != nil {
		return errorResponse(request.ID, request.Wire, CodeInternalError, "Internal error", nil)
	}
	return ResponseEnvelope{
		JSONRPC: "2.0",
		ID:      request.ID,
		Wire:    request.Wire,
		Result:  serialized,
	}
}

func safeJSONMarshal(value any) (raw []byte, err error) {
	defer func() {
		if recover() != nil {
			raw = nil
			err = errors.New("marshal Product RPC JSON")
		}
	}()
	return json.Marshal(value)
}

func callValidator(
	validator ParamsValidator,
	params json.RawMessage,
) (err error, panicked bool) {
	defer func() {
		if recover() != nil {
			err = nil
			panicked = true
		}
	}()
	return validator(params), false
}

func callHandler(
	ctx context.Context,
	handler Handler,
	params json.RawMessage,
) (result any, err error, panicked bool) {
	defer func() {
		if recover() != nil {
			result = nil
			err = nil
			panicked = true
		}
	}()
	result, err = handler(ctx, params)
	return result, err, false
}

func decodeRequest(raw []byte) (requestEnvelope, error) {
	var request requestEnvelope
	if err := rejectDuplicateTopLevelFields(raw); err != nil {
		return request, err
	}
	if err := decodeStrict(raw, &request); err != nil || request.JSONRPC != "2.0" ||
		len(request.ID) == 0 || strings.TrimSpace(request.Method) == "" ||
		len(request.Method) > 128 || len(request.Wire) == 0 || len(request.Params) == 0 {
		return request, errors.New("invalid request")
	}
	var id string
	if json.Unmarshal(request.ID, &id) != nil || id == "" {
		return request, errors.New("invalid request id")
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(request.Params, &params) != nil || params == nil {
		return request, errors.New("invalid request params")
	}
	var wire map[string]json.RawMessage
	if json.Unmarshal(request.Wire, &wire) != nil || wire == nil {
		return request, errors.New("invalid request wire")
	}
	return request, nil
}

func rejectDuplicateTopLevelFields(raw []byte) error {
	_, duplicates, err := decodeTopLevelFields(raw)
	if err != nil {
		return err
	}
	if len(duplicates) != 0 {
		return errors.New("Product RPC request has duplicate fields")
	}
	return nil
}

func decodeTopLevelFields(
	raw []byte,
) (map[string]json.RawMessage, map[string]struct{}, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, nil, errors.New("Product RPC request must be an object")
	}
	fields := make(map[string]json.RawMessage)
	seen := make(map[string]struct{})
	duplicates := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		field, ok := token.(string)
		if !ok {
			return nil, nil, errors.New("Product RPC request field is invalid")
		}
		if _, duplicate := seen[field]; duplicate {
			duplicates[field] = struct{}{}
		}
		seen[field] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, err
		}
		fields[field] = value
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, nil, errors.New("Product RPC request object is incomplete")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("trailing JSON")
	}
	return fields, duplicates, nil
}

func (dispatcher *Dispatcher) scopeIsCurrent(
	scope productcapabilities.Scope,
	wire json.RawMessage,
) bool {
	switch scope {
	case productcapabilities.GlobalScope:
		_, err := contractsv2.DecodeStrict[contractsv2.GlobalWireScope](wire)
		return err == nil
	case productcapabilities.WorkspaceScope:
		workspace, err := contractsv2.DecodeStrict[contractsv2.WorkspaceWireScope](wire)
		if err != nil {
			return false
		}
		current := dispatcher.identity
		return workspace.WorkspaceID == current.WorkspaceID &&
			workspace.SessionEpoch == current.SessionEpoch
	default:
		return false
	}
}

func validateIdentity(identity Identity) error {
	if !isCanonicalUUID(identity.WorkspaceID) || !isCanonicalUUID(identity.ClaimID) ||
		identity.SessionEpoch == 0 || identity.FenceEpoch == 0 {
		return errors.New("product RPC identity is invalid")
	}
	return nil
}

func isCanonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func responseIdentity(raw []byte) (json.RawMessage, json.RawMessage) {
	id := json.RawMessage("null")
	wire := json.RawMessage("null")
	fields, duplicates, err := decodeTopLevelFields(raw)
	if err != nil {
		return id, wire
	}
	if candidate, ok := fields["id"]; ok {
		if _, duplicate := duplicates["id"]; duplicate {
			candidate = nil
		}
		var value string
		if json.Unmarshal(candidate, &value) == nil && value != "" {
			id = append(json.RawMessage(nil), candidate...)
		}
	}
	if candidate, ok := fields["wire"]; ok {
		if _, duplicate := duplicates["wire"]; !duplicate && isJSONObject(candidate) {
			wire = append(json.RawMessage(nil), candidate...)
		}
	}
	return id, wire
}

func isJSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func errorResponse(
	id json.RawMessage,
	wire json.RawMessage,
	code int,
	message string,
	data map[string]any,
) ResponseEnvelope {
	return ResponseEnvelope{
		JSONRPC: "2.0",
		ID:      id,
		Wire:    wire,
		Error:   &ErrorObject{Code: code, Message: message, Data: data},
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
