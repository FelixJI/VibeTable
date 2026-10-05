package app

import (
	"net/http"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	"github.com/vibetable/vibetable/sidecar/internal/auth"
	"github.com/vibetable/vibetable/sidecar/internal/sourceimport"
)

func TestSourceImportDiscardHTTPRequiresSessionAndCurrentEpoch(t *testing.T) {
	r, encoded := importPlanHTTPRouter(t)
	owner := newImportPlanOwner("workspace-one")
	plan := sourceExecutionPreview(t, sourceExecutionOrdinarySnapshot(3), nil)
	reply, err := owner.mintSource(plan, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	registerSourceImportRoutes(r, nil, owner, nil, nil, 7)
	body := map[string]any{"token": reply.Token, "sessionEpoch": 7}
	for _, secret := range []string{"", encoded + "x"} {
		response, _ := importPlanHTTPCall(t, r, sourceImportPath+"/discard", body, secret)
		if response.Code != http.StatusUnauthorized || owner.sourcePlans[reply.Token] == nil {
			t.Fatalf("missing/wrong %s allowed discard: %d", auth.HeaderName, response.Code)
		}
	}
	body["sessionEpoch"] = 8
	response, _ := importPlanHTTPCall(t, r, sourceImportPath+"/discard", body, encoded)
	if response.Code == http.StatusOK || owner.sourcePlans[reply.Token] == nil {
		t.Fatal("old session epoch discarded a current plan")
	}
	body["sessionEpoch"] = 7
	body["arbitraryPath"] = "other-upload"
	response, _ = importPlanHTTPCall(t, r, sourceImportPath+"/discard", body, encoded)
	if response.Code == http.StatusOK || owner.sourcePlans[reply.Token] == nil {
		t.Fatal("discard accepted arbitrary cleanup input")
	}
	delete(body, "arbitraryPath")
	response, payload := importPlanHTTPCall(t, r, sourceImportPath+"/discard", body, encoded)
	if response.Code != http.StatusOK || payload["discarded"] != true || owner.sourcePlans[reply.Token] != nil {
		t.Fatalf("authorized unclaimed discard failed: %d %v", response.Code, payload)
	}
}

func TestSourceImportDiscardReleasesOnlyUnclaimedPlanAndPreservesOtherUploads(t *testing.T) {
	manager, err := attachments.New()
	if err != nil {
		t.Fatal(err)
	}
	plan := sourceExecutionPreview(t, sourceExecutionOrdinarySnapshot(3), nil)
	plan.Attachments = []sourceimport.Attachment{{ID: "file", TableID: "a", FieldID: "file", RecordID: "r0000", Name: "source.txt", Size: 3}}
	owner := newImportPlanOwner("workspace-one")
	first, err := owner.mintSource(plan, 7, manager)
	if err != nil {
		t.Fatal(err)
	}
	second, err := owner.mintSource(plan, 7, manager)
	if err != nil {
		t.Fatal(err)
	}
	key := sourceimport.Key{Provider: plan.Provider, ContainerID: plan.ContainerID, TableID: "a", FieldID: "file", RecordID: "r0000", ObjectID: "file"}
	for _, token := range []string{first.Token, second.Token} {
		if err := owner.uploadSource(token, 7, key, []byte("abc")); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Stage("ordinary-upload", "ordinary.txt", []byte("other")); err != nil {
		t.Fatal(err)
	}
	firstHandle := owner.sourcePlans[first.Token].handles[key]
	secondHandle := owner.sourcePlans[second.Token].handles[key]
	if err := owner.discardSource(first.Token, 8); err == nil {
		t.Fatal("old epoch could discard a current plan")
	}
	if err := owner.discardSource(first.Token, 7); err != nil {
		t.Fatal(err)
	}
	if err := owner.discardSource(first.Token, 7); err != nil {
		t.Fatal("discard was not idempotent")
	}
	if owner.sourcePlans[first.Token] != nil {
		t.Fatal("discard retained token authority")
	}
	if err := manager.StageOwned(firstHandle, "new.txt", []byte("new")); err != nil {
		t.Fatal("discard did not release its staged bytes")
	}
	if err := manager.StageOwned(secondHandle, "new.txt", []byte("new")); err == nil {
		t.Fatal("discard cleared another plan's staged bytes")
	}
	if err := manager.Stage("ordinary-upload", "new.txt", []byte("new")); err == nil {
		t.Fatal("discard cleared an ordinary attachment upload")
	}
	claim := sourceClaimRequest{Contract: sourceimport.Contract, Token: second.Token, JobID: "claimed-job", SessionEpoch: 7, Confirmed: true,
		Observation: sourceimport.Observation{Version: "v1", TableVersions: map[string]string{"a": "a1", "b": "b1", "c": "c1"}}}
	if _, err := owner.claimSource(claim, 7); err != nil {
		t.Fatal(err)
	}
	if err := owner.discardSource(second.Token, 7); err == nil {
		t.Fatal("discard stole bytes from a claimed executor")
	}
	if err := manager.StageOwned(secondHandle, "new.txt", []byte("new")); err == nil {
		t.Fatal("rejected discard still cleared the active upload")
	}
}

func TestSourceImportPublicPreviewCarriesCountsWithoutSourceRows(t *testing.T) {
	plan := sourceExecutionPreview(t, sourceExecutionOrdinarySnapshot(3), nil)
	owner := newImportPlanOwner("workspace-one")
	reply, err := owner.mintSource(plan, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, table := range reply.Plan.Tables {
		if len(table.Records) != 0 {
			t.Fatal("public plan exposed source row contents")
		}
		total += table.RecordCount
	}
	if total != 5 || len(owner.sourcePlans[reply.Token].plan.Tables[0].Records) == 0 {
		t.Fatal("public progress counts changed frozen execution rows")
	}
}
