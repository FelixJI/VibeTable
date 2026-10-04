package app

import (
	"context"
	"encoding/json"
	"fmt"
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
		if !stored.claimed && !owner.now().Before(stored.expires) {
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
		publicPlan.Tables[index].Records = []sourceimport.Record{}
	}
	return sourcePreviewReply{Contract: sourceimport.Contract, Token: token, ExpiresAt: float64(expires.UnixNano()) / 1e9, Plan: publicPlan}, nil
}

type sourceClaimRequest struct {
	Contract     string                   `json:"contract"`
	Token        string                   `json:"token"`
	JobID        string                   `json:"jobId"`
	SessionEpoch uint64                   `json:"sessionEpoch"`
	Confirmed    bool                     `json:"confirmed"`
	Observation  sourceimport.Observation `json:"observation"`
}

func (owner *importPlanOwner) claimSource(input sourceClaimRequest, epoch uint64) (*sourceStoredPlan, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	stored := owner.sourcePlans[input.Token]
	if stored == nil || owner.workspaceID == "" || input.Contract != sourceimport.Contract {
		return nil, fmt.Errorf("source_import.plan.unknown")
	}
	if !owner.now().Before(stored.expires) {
		stored.cleanup()
		delete(owner.sourcePlans, input.Token)
		return nil, fmt.Errorf("source_import.plan.expired")
	}
	if stored.claimed {
		return nil, fmt.Errorf("source_import.plan.consumed")
	}
	if stored.epoch != epoch || input.SessionEpoch != epoch {
		return nil, fmt.Errorf("source_import.session_changed")
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
	if stored == nil || stored.claimed || stored.epoch != epoch || !owner.now().Before(stored.expires) || !stored.plan.CanApply {
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
