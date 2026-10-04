package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/sourceimport"
)

type sourceStoredPlan struct {
	plan    sourceimport.Plan
	expires time.Time
	epoch   uint64
	claimed bool
	handles map[sourceimport.Key]string
	manager *attachments.Manager
	jobID   string
	closed  bool
}

type sourcePreviewReply struct {
	Contract  string            `json:"contract"`
	Token     string            `json:"token"`
	ExpiresAt float64           `json:"expiresAt"`
	Plan      sourceimport.Plan `json:"plan"`
}

func (owner *importPlanOwner) mintSource(plan sourceimport.Plan, epoch uint64, manager *attachments.Manager) (sourcePreviewReply, error) {
	if owner.workspaceID == "" || plan.Contract != sourceimport.Contract {
		return sourcePreviewReply{}, fmt.Errorf("source_import.plan.invalid")
	}
	// Detached before storage: neither provider buffers nor a preview response
	// may mutate the authoritative frozen plan after the user reviews it.
	raw, err := json.Marshal(plan)
	if err != nil {
		return sourcePreviewReply{}, err
	}
	var storedPlan sourceimport.Plan
	if err := v2.StrictDecode(raw, &storedPlan); err != nil {
		return sourcePreviewReply{}, err
	}
	token, err := importPlanToken()
	if err != nil {
		return sourcePreviewReply{}, err
	}
	token = "mip1." + strings.TrimPrefix(token, "imp1.")
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.sourcePlans == nil {
		owner.sourcePlans = map[string]*sourceStoredPlan{}
	}
	for id, stored := range owner.sourcePlans {
		if !stored.claimed && stored.jobID == "" && !owner.now().Before(stored.expires) {
			stored.cleanup()
			delete(owner.sourcePlans, id)
		}
	}
	if len(owner.sourcePlans) >= 8 {
		return sourcePreviewReply{}, fmt.Errorf("source_import.plan.capacity")
	}
	expires := owner.now().Add(importPlanTTL)
	owner.sourcePlans[token] = &sourceStoredPlan{plan: storedPlan, expires: expires, epoch: epoch, handles: map[sourceimport.Key]string{}, manager: manager}
	publicPlan := plan
	publicPlan.Tables = append([]sourceimport.TablePlan{}, plan.Tables...)
	for index := range publicPlan.Tables {
		publicPlan.Tables[index].RecordCount = len(publicPlan.Tables[index].Records)
		publicPlan.Tables[index].Records = []sourceimport.Record{}
	}
	return sourcePreviewReply{Contract: sourceimport.Contract, Token: token, ExpiresAt: float64(expires.UnixNano()) / 1e9, Plan: publicPlan}, nil
}

type sourceClaimRequest struct {
	Contract         string                   `json:"contract"`
	Token            string                   `json:"token"`
	JobID            string                   `json:"jobId"`
	SessionEpoch     uint64                   `json:"sessionEpoch"`
	Confirmed        bool                     `json:"confirmed"`
	Observation      sourceimport.Observation `json:"observation"`
	requireAdmission bool
}

func (owner *importPlanOwner) claimSource(input sourceClaimRequest, epoch uint64) (*sourceStoredPlan, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	stored := owner.sourcePlans[input.Token]
	if stored == nil || owner.workspaceID == "" || input.Contract != sourceimport.Contract {
		return nil, fmt.Errorf("source_import.plan.unknown")
	}
	if stored.epoch != epoch || input.SessionEpoch != epoch {
		return nil, fmt.Errorf("source_import.session_changed")
	}
	if !owner.now().Before(stored.expires) {
		stored.cleanup()
		if stored.jobID == "" {
			delete(owner.sourcePlans, input.Token)
		}
		return nil, fmt.Errorf("source_import.plan.expired")
	}
	if stored.claimed || stored.closed {
		return nil, fmt.Errorf("source_import.plan.consumed")
	}
	if !input.Confirmed || !stored.plan.CanApply {
		return nil, fmt.Errorf("source_import.confirmation_required")
	}
	if err := stored.plan.CheckObservation(input.Observation); err != nil {
		return nil, err
	}
	if input.JobID == "" || len(input.JobID) > 80 || strings.ContainsAny(input.JobID, "\x00\r\n/\\") {
		return nil, fmt.Errorf("source_import.job.invalid")
	}
	if (stored.jobID != "" || input.requireAdmission) && stored.jobID != input.JobID {
		return nil, fmt.Errorf("source_import.job.binding_conflict")
	}
	for _, attachment := range stored.plan.Attachments {
		key := sourceimport.Key{Provider: stored.plan.Provider, ContainerID: stored.plan.ContainerID, TableID: attachment.TableID, FieldID: attachment.FieldID, RecordID: attachment.RecordID, ObjectID: attachment.ID}
		if stored.handles[key] == "" {
			return nil, fmt.Errorf("source_import.attachment.bytes_missing")
		}
	}
	// A failed/unknown/cancelled execution remains consumed. Re-preview creates
	// a new group and shows durable targets; it cannot append to the old group.
	stored.claimed = true
	return stored, nil
}

func (owner *importPlanOwner) uploadSource(token string, epoch uint64, key sourceimport.Key, content []byte) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	stored := owner.sourcePlans[token]
	if stored == nil || stored.claimed || stored.closed || stored.epoch != epoch || !owner.now().Before(stored.expires) || !stored.plan.CanApply {
		return fmt.Errorf("source_import.attachment.plan_invalid")
	}
	if key.Provider != stored.plan.Provider || key.ContainerID != stored.plan.ContainerID {
		return fmt.Errorf("source_import.attachment.identity_invalid")
	}
	var metadata *sourceimport.Attachment
	for i := range stored.plan.Attachments {
		candidate := &stored.plan.Attachments[i]
		if candidate.TableID == key.TableID && candidate.FieldID == key.FieldID && candidate.RecordID == key.RecordID && candidate.ID == key.ObjectID {
			metadata = candidate
			break
		}
	}
	if metadata == nil || int64(len(content)) != metadata.Size {
		return fmt.Errorf("source_import.attachment.content_invalid")
	}
	if stored.handles[key] != "" {
		return fmt.Errorf("source_import.attachment.already_uploaded")
	}
	handle := fmt.Sprintf("mip_%s_%d", strings.TrimPrefix(token, "mip1."), len(stored.handles))
	if err := stored.manager.StageOwned(handle, metadata.Name, content); err != nil {
		return err
	}
	stored.handles[key] = handle
	return nil
}

type sourceLifecycleRequest struct {
	Token        string `json:"token"`
	JobID        string `json:"jobId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
	State        string `json:"state,omitempty"`
}

func (owner *importPlanOwner) startSource(ctx context.Context, input sourceLifecycleRequest, epoch uint64, journal sourceimport.Journal) (sourceimport.Result, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	stored := owner.sourcePlans[input.Token]
	if stored == nil || stored.claimed || stored.closed || !stored.plan.CanApply || !owner.now().Before(stored.expires) {
		return sourceimport.Result{}, fmt.Errorf("source_import.plan.unknown")
	}
	if input.SessionEpoch != epoch || stored.epoch != epoch {
		return sourceimport.Result{}, fmt.Errorf("source_import.session_changed")
	}
	if input.State != "" || input.JobID == "" || len(input.JobID) > 80 || strings.ContainsAny(input.JobID, "\x00\r\n/\\") || (stored.jobID != "" && stored.jobID != input.JobID) {
		return sourceimport.Result{}, fmt.Errorf("source_import.job.binding_conflict")
	}
	for _, other := range owner.sourcePlans {
		if other != stored && other.jobID == input.JobID {
			return sourceimport.Result{}, fmt.Errorf("source_import.job.binding_conflict")
		}
	}
	stored.jobID = input.JobID
	result := sourceimport.InitialResult(stored.plan, input.JobID, epoch, "preparing")
	if err := journal.Start(ctx, result); err != nil {
		return sourceimport.Result{}, err
	}
	return journal.Read(ctx, input.JobID)
}

func (owner *importPlanOwner) finishSourcePreparation(ctx context.Context, input sourceLifecycleRequest, epoch uint64, journal sourceimport.Journal) (sourceimport.Result, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	stored := owner.sourcePlans[input.Token]
	if stored == nil || stored.claimed || stored.jobID != input.JobID {
		return sourceimport.Result{}, fmt.Errorf("source_import.plan.consumed")
	}
	if input.SessionEpoch != epoch || stored.epoch != epoch {
		return sourceimport.Result{}, fmt.Errorf("source_import.session_changed")
	}
	if input.State != "failed" && input.State != "cancelled" {
		return sourceimport.Result{}, fmt.Errorf("source_import.job.state_invalid")
	}
	result, err := journal.Read(ctx, input.JobID)
	if err != nil {
		return sourceimport.Result{}, err
	}
	expected := sourceimport.InitialResult(stored.plan, input.JobID, epoch, "preparing")
	if result.Provider != expected.Provider || result.ContainerID != expected.ContainerID || result.SourceName != expected.SourceName || result.Total != expected.Total || result.SessionEpoch != epoch || result.ReadWindow != expected.ReadWindow || !reflect.DeepEqual(result.Fields, expected.Fields) {
		return sourceimport.Result{}, fmt.Errorf("source_import.job.binding_conflict")
	}
	if stored.closed {
		return result, nil
	}
	if result.Stage == "preparing" && (result.State == "failed" || result.State == "cancelled") && result.FinishedAt != "" && result.Created == 0 && len(result.Batches) == 0 && len(result.Targets) == 0 && result.SessionEpoch == epoch {
		// Reconcile a lost preparation-settlement acknowledgement without
		// rewriting its terminal facts or issuing a replacement job.
		stored.closed = true
		stored.cleanup()
		return result, nil
	}
	if result.State != "interrupted" || result.Stage != "preparing" || result.Created != 0 || len(result.Batches) != 0 || len(result.Targets) != 0 || result.SessionEpoch != epoch {
		return sourceimport.Result{}, fmt.Errorf("source_import.job.already_started")
	}
	result.State = input.State
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	result.Diagnostics = append(result.Diagnostics, sourceimport.Diagnostic{Code: "source_import.preparation_stopped", Message: "来源核对或附件读取阶段停止；尚未创建业务目标，请重新预检", Blocking: true})
	if err := journal.Finish(ctx, result); err != nil {
		return sourceimport.Result{}, err
	}
	stored.closed = true
	stored.cleanup()
	return journal.Read(ctx, input.JobID)
}

func (stored *sourceStoredPlan) Stage(ctx context.Context, job, table, field string, attachment sourceimport.Attachment) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key := sourceimport.Key{Provider: stored.plan.Provider, ContainerID: stored.plan.ContainerID, TableID: attachment.TableID, FieldID: attachment.FieldID, RecordID: attachment.RecordID, ObjectID: attachment.ID}
	handle := stored.handles[key]
	if !stored.claimed || handle == "" {
		return "", fmt.Errorf("source_import.attachment.bytes_missing")
	}
	return handle, nil
}

func (stored *sourceStoredPlan) cleanup() {
	if stored.manager != nil {
		for _, handle := range stored.handles {
			stored.manager.Drop(handle)
		}
	}
}

func (stored *sourceStoredPlan) Cleanup(ctx context.Context, job string) error {
	stored.cleanup()
	return nil
}

func (owner *importPlanOwner) retireSource(token string) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if stored := owner.sourcePlans[token]; stored != nil {
		stored.cleanup()
		delete(owner.sourcePlans, token)
	}
}

// Discard is a Host lifecycle operation, never an execution cancellation. The
// same lock as upload/claim makes cleanup unable to race a claimed executor.
func (owner *importPlanOwner) discardSource(token string, epoch uint64) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	stored := owner.sourcePlans[token]
	if stored == nil {
		return nil
	}
	if stored.epoch != epoch {
		return fmt.Errorf("source_import.session_changed")
	}
	if stored.claimed {
		return fmt.Errorf("source_import.plan.consumed")
	}
	stored.cleanup()
	delete(owner.sourcePlans, token)
	return nil
}
