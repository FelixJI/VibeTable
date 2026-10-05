package app

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"github.com/vibetable/vibetable/sidecar/internal/attachments"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/sourceimport"
)

const sourceImportPath = "/api/vibetable/v2/source-import"

type sourceImportJournal struct {
	*sourceimport.PocketBaseJournal
	gates []businessWriteGate
}

func (journal *sourceImportJournal) Start(ctx context.Context, result sourceimport.Result) error {
	kind := "source.import.start"
	if result.Stage == "preparing" {
		// Admission and execution each commit once for this job. Sharing a
		// receipt identity would collide when preparing advances to schema.
		kind = "source.import.admit"
	}
	return runBusinessWrite(ctx, journal.gates, kind, result.JobID, func(ctx context.Context) error { return journal.PocketBaseJournal.Start(ctx, result) })
}

func (journal *sourceImportJournal) Prepare(ctx context.Context, job, batch, stage string, count int) error {
	return runBusinessWrite(ctx, journal.gates, "source.import.prepare", batch, func(ctx context.Context) error {
		return journal.PocketBaseJournal.Prepare(ctx, job, batch, stage, count)
	})
}

func (journal *sourceImportJournal) Finish(ctx context.Context, result sourceimport.Result) error {
	return runBusinessWrite(ctx, journal.gates, "source.import.finish", result.JobID, func(ctx context.Context) error { return journal.PocketBaseJournal.Finish(ctx, result) })
}

func registerSourceImportRoutes(r *router.Router[*core.RequestEvent], pb core.App, owner *importPlanOwner, authority sourceimport.Authority, manager *attachments.Manager, epoch uint64, gates ...businessWriteGate) {
	journal := &sourceImportJournal{PocketBaseJournal: sourceimport.NewJournal(pb), gates: gates}
	// Only authenticated loopback Host provider traffic uses these ports.
	// There is no Product RPC arbitrary source/JSON import method. Future cloud
	// providers own download/credential lifetime in Host, not Python or Go.
	r.POST(sourceImportPath+"/preview", func(request *core.RequestEvent) error {
		var input struct {
			Contract     string                `json:"contract"`
			SessionEpoch uint64                `json:"sessionEpoch"`
			Snapshot     sourceimport.Snapshot `json:"snapshot"`
			Options      sourceimport.Options  `json:"options"`
		}
		if err := decodeImportPlanBody(request, &input); err != nil {
			return writeFieldError(request, err)
		}
		if input.Contract != sourceimport.Contract || input.SessionEpoch != epoch {
			return writeFieldError(request, fmt.Errorf("source_import.session_changed"))
		}
		rows, err := pb.FindRecordsByFilter("vibetable_tables", "", "", 0, 0)
		if err != nil {
			return writeFieldError(request, err)
		}
		names := make([]string, 0, len(rows))
		for _, row := range rows {
			names = append(names, row.GetString("display_name"))
		}
		plan, err := sourceimport.Preview(request.Request.Context(), input.Snapshot, input.Options, names)
		if err != nil {
			return writeFieldError(request, err)
		}
		reply, err := owner.mintSource(plan, epoch, manager)
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, reply)
	})
	r.POST(sourceImportPath+"/start", func(request *core.RequestEvent) error {
		var input sourceLifecycleRequest
		if err := decodeImportPlanBody(request, &input); err != nil {
			return writeFieldError(request, err)
		}
		result, err := owner.startSource(request.Request.Context(), input, epoch, journal)
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, sourceimport.Project(result))
	})
	r.POST(sourceImportPath+"/finish", func(request *core.RequestEvent) error {
		var input sourceLifecycleRequest
		if err := decodeImportPlanBody(request, &input); err != nil {
			return writeFieldError(request, err)
		}
		result, err := owner.finishSourcePreparation(request.Request.Context(), input, epoch, journal)
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, sourceimport.Project(result))
	})
	r.POST(sourceImportPath+"/upload", func(request *core.RequestEvent) error {
		// Metadata identities live in the frozen plan. The Host transports only
		// their key and real bytes; URLs and arbitrary file paths are rejected.
		request.Request.Body = http.MaxBytesReader(request.Response, request.Request.Body, 34<<20)
		if err := request.Request.ParseMultipartForm(1 << 20); err != nil {
			return writeFieldError(request, err)
		}
		if request.Request.MultipartForm != nil {
			defer request.Request.MultipartForm.RemoveAll()
		}
		if len(request.Request.MultipartForm.Value) != 1 || len(request.Request.MultipartForm.Value["metadata"]) != 1 || len(request.Request.MultipartForm.File) != 1 || len(request.Request.MultipartForm.File["file"]) != 1 {
			return writeFieldError(request, fmt.Errorf("source_import.attachment.request_invalid"))
		}
		var input struct {
			Token        string           `json:"token"`
			SessionEpoch uint64           `json:"sessionEpoch"`
			Key          sourceimport.Key `json:"key"`
		}
		metadata := request.Request.FormValue("metadata")
		if len(metadata) > 4096 {
			return writeFieldError(request, fmt.Errorf("source_import.attachment.metadata_invalid"))
		}
		if err := v2.StrictDecode([]byte(metadata), &input); err != nil {
			return writeFieldError(request, err)
		}
		if input.SessionEpoch != epoch {
			return writeFieldError(request, fmt.Errorf("source_import.session_changed"))
		}
		file, _, err := request.Request.FormFile("file")
		if err != nil {
			return writeFieldError(request, err)
		}
		defer file.Close()
		content, err := io.ReadAll(io.LimitReader(file, (32<<20)+1))
		if err != nil || len(content) > 32<<20 {
			return writeFieldError(request, fmt.Errorf("source_import.attachment.capacity"))
		}
		if err := owner.uploadSource(input.Token, epoch, input.Key, content); err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, map[string]any{"uploaded": true})
	})
	r.POST(sourceImportPath+"/discard", func(request *core.RequestEvent) error {
		var input struct {
			Token        string `json:"token"`
			SessionEpoch uint64 `json:"sessionEpoch"`
		}
		if err := decodeImportPlanBody(request, &input); err != nil {
			return writeFieldError(request, err)
		}
		if input.SessionEpoch != epoch {
			return writeFieldError(request, fmt.Errorf("source_import.session_changed"))
		}
		if err := owner.discardSource(input.Token, epoch); err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, map[string]any{"discarded": true})
	})
	r.POST(sourceImportPath+"/execute", func(request *core.RequestEvent) error {
		var input sourceClaimRequest
		if err := decodeImportPlanBody(request, &input); err != nil {
			return writeFieldError(request, err)
		}
		input.requireAdmission = true
		stored, err := owner.claimSource(input, epoch)
		if err != nil {
			return writeFieldError(request, err)
		}
		defer owner.retireSource(input.Token)
		executor := sourceimport.NewExecutor(authority, journal, stored)
		result, err := executor.Execute(request.Request.Context(), stored.plan, input.JobID, epoch)
		if result.JobID != "" {
			// Partial and unknown results are formal task outcomes. Transport
			// success alone is never interpreted as overall migration success.
			return request.JSON(http.StatusOK, sourceimport.Project(result))
		}
		if err != nil {
			return writeFieldError(request, err)
		}
		return writeFieldError(request, fmt.Errorf("source_import.result_unavailable"))
	})
	r.GET(sourceImportPath+"/history", func(request *core.RequestEvent) error {
		if request.Request.URL.RawQuery != "" {
			return writeFieldError(request, fmt.Errorf("source_import.history.query_invalid"))
		}
		results, err := journal.List(request.Request.Context())
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, map[string]any{"contract": sourceimport.Contract, "entries": results})
	})
	r.GET(sourceImportPath+"/result/{job}", func(request *core.RequestEvent) error {
		result, err := journal.Read(request.Request.Context(), request.Request.PathValue("job"))
		if err != nil {
			return writeFieldError(request, err)
		}
		return request.JSON(http.StatusOK, sourceimport.Project(result))
	})
}
