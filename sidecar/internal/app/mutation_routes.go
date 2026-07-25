package app

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/vibetable/vibetable/sidecar/internal/formula"
	"github.com/vibetable/vibetable/sidecar/internal/mutation"
)

const (
	maxMutationRequestBytes   = 1 << 20
	maxMultipartMutationBytes = 101 << 20
)

type uploadStager interface {
	Stage(handle, originalName string, content []byte) error
	Drop(handles ...string)
}

type ownedUploadStager interface {
	StageOwned(handle, originalName string, content []byte) error
}

type multipartUpload struct {
	handle       string
	originalName string
	content      []byte
}

type mutationKernel interface {
	Preview(context.Context, mutation.Request) (mutation.PreviewResult, error)
	Apply(context.Context, mutation.Request) (mutation.Receipt, error)
}

func registerMutationRoutes(
	r *router.Router[*core.RequestEvent],
	kernel mutationKernel,
	stager uploadStager,
) {
	r.POST("/api/vibetable/v1/mutations/preview", func(request *core.RequestEvent) error {
		input, err := decodeMutationRequest(request.Request.Body)
		if err != nil {
			return writeMutationError(request, err)
		}
		result, err := kernel.Preview(request.Request.Context(), input)
		if err != nil {
			return writeMutationError(request, err)
		}
		return request.JSON(http.StatusOK, result)
	})
	r.POST("/api/vibetable/v1/mutations/apply", func(request *core.RequestEvent) error {
		input, stagedHandles, err := decodeMutationApplyRequest(request.Request, stager)
		if stager != nil {
			defer stager.Drop(stagedHandles...)
		}
		if err != nil {
			return writeMutationError(request, err)
		}
		receipt, err := kernel.Apply(request.Request.Context(), input)
		if err != nil {
			return writeMutationError(request, err)
		}
		return request.JSON(http.StatusOK, receipt)
	})
}

func decodeMutationApplyRequest(
	request *http.Request,
	stager uploadStager,
) (mutation.Request, []string, error) {
	contentType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err == nil && contentType == "multipart/form-data" {
		if stager == nil {
			return mutation.Request{}, nil, mutationRequestError(
				"managed attachment service is unavailable",
			)
		}
		return decodeMultipartMutation(request.Body, parameters["boundary"], stager)
	}
	decoded, err := decodeMutationRequest(request.Body)
	return decoded, nil, err
}

func decodeMultipartMutation(
	body io.Reader,
	boundary string,
	stager uploadStager,
) (mutation.Request, []string, error) {
	if boundary == "" {
		return mutation.Request{}, nil, mutationRequestError(
			"multipart mutation boundary is missing",
		)
	}
	reader := multipart.NewReader(
		io.LimitReader(body, maxMultipartMutationBytes+1),
		boundary,
	)
	var requestRaw []byte
	uploads := []multipartUpload{}
	clientHandles := map[string]struct{}{}
	totalBytes := int64(0)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return mutation.Request{}, nil, mutationRequestError(
				"multipart mutation body is invalid",
			)
		}
		raw, err := io.ReadAll(io.LimitReader(part, maxMultipartMutationBytes-totalBytes+1))
		_ = part.Close()
		if err != nil {
			return mutation.Request{}, nil, mutationRequestError(
				"multipart mutation part could not be read",
			)
		}
		totalBytes += int64(len(raw))
		if totalBytes > maxMultipartMutationBytes {
			return mutation.Request{}, nil, mutationRequestError(
				"multipart mutation exceeds the supported size",
			)
		}
		switch {
		case part.FormName() == "request":
			if requestRaw != nil || part.FileName() != "" ||
				len(raw) > maxMutationRequestBytes {
				return mutation.Request{}, nil, mutationRequestError(
					"multipart mutation request part is invalid",
				)
			}
			requestRaw = raw
		case strings.HasPrefix(part.FormName(), "upload:"):
			handle := strings.TrimPrefix(part.FormName(), "upload:")
			if handle == "" || part.FileName() == "" {
				return mutation.Request{}, nil, mutationRequestError(
					"multipart upload handle and filename are required",
				)
			}
			if _, duplicate := clientHandles[handle]; duplicate {
				return mutation.Request{}, nil, mutationRequestError(
					"multipart upload handle is duplicated",
				)
			}
			clientHandles[handle] = struct{}{}
			uploads = append(uploads, multipartUpload{
				handle: handle, originalName: part.FileName(), content: raw,
			})
		default:
			return mutation.Request{}, nil, mutationRequestError(
				"unknown multipart mutation part",
			)
		}
	}
	if requestRaw == nil {
		return mutation.Request{}, nil, mutationRequestError(
			"multipart mutation request part is required",
		)
	}
	var decoded mutation.Request
	if err := mutation.DecodeStrict(requestRaw, &decoded); err != nil {
		return mutation.Request{}, nil, mutationRequestError(
			"mutation request body is invalid",
		)
	}
	referenced := map[string]struct{}{}
	for _, operation := range decoded.Operations {
		for _, handle := range operation.UploadHandles {
			referenced[handle] = struct{}{}
		}
	}
	if len(referenced) != len(clientHandles) {
		return mutation.Request{}, nil, mutationRequestError(
			"multipart uploads must exactly match referenced upload handles",
		)
	}
	for handle := range referenced {
		if _, exists := clientHandles[handle]; !exists {
			return mutation.Request{}, nil, mutationRequestError(
				"multipart uploads must exactly match referenced upload handles",
			)
		}
	}

	internalByClient := make(map[string]string, len(uploads))
	stagedHandles := make([]string, 0, len(uploads))
	for index := range uploads {
		internalHandle := internalUploadHandle(
			decoded.RequestID, uploads[index],
		)
		stage := stager.Stage
		if owned, ok := stager.(ownedUploadStager); ok {
			stage = owned.StageOwned
		}
		if err := stage(internalHandle, uploads[index].originalName, uploads[index].content); err != nil {
			stager.Drop(stagedHandles...)
			return mutation.Request{}, nil, err
		}
		internalByClient[uploads[index].handle] = internalHandle
		stagedHandles = append(stagedHandles, internalHandle)
		uploads[index].content = nil
	}
	for operationIndex := range decoded.Operations {
		for handleIndex, handle := range decoded.Operations[operationIndex].UploadHandles {
			decoded.Operations[operationIndex].UploadHandles[handleIndex] =
				internalByClient[handle]
		}
	}
	return decoded, stagedHandles, nil
}

func internalUploadHandle(requestID string, upload multipartUpload) string {
	hash := sha256.New()
	writeFramed := func(value []byte) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(value)
	}
	writeFramed([]byte(requestID))
	writeFramed([]byte(upload.handle))
	writeFramed([]byte(upload.originalName))
	writeFramed(upload.content)
	return "request_" + hex.EncodeToString(hash.Sum(nil))
}

func decodeMutationRequest(reader io.Reader) (mutation.Request, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, maxMutationRequestBytes+1))
	if err != nil || len(raw) > maxMutationRequestBytes {
		return mutation.Request{}, mutationRequestError("mutation request body is invalid")
	}
	var request mutation.Request
	if err := mutation.DecodeStrict(raw, &request); err != nil {
		return mutation.Request{}, mutationRequestError("mutation request body is invalid")
	}
	return request, nil
}

func mutationRequestError(message string) *mutation.ProductError {
	return &mutation.ProductError{
		ContractVersion: mutation.ContractVersion,
		Code:            "mutation.request.invalid",
		Message:         message,
		Details:         map[string]any{},
	}
}

func writeMutationError(request *core.RequestEvent, err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	var formulaErr *formula.Error
	if errors.As(err, &formulaErr) {
		return writeFormulaError(request, formulaErr)
	}
	var productErr *mutation.ProductError
	if !errors.As(err, &productErr) {
		productErr = mutationRequestError("mutation operation failed")
		productErr.Code = "mutation.internal.failed"
		productErr.Retryable = true
	}
	status := mutationErrorStatus(productErr)
	if writeErr := request.JSON(status, productErr); writeErr != nil {
		return fmt.Errorf("write mutation error response: %w", writeErr)
	}
	return nil
}

func mutationErrorStatus(productErr *mutation.ProductError) int {
	status := http.StatusUnprocessableEntity
	switch {
	case productErr.Code == "mutation.request.invalid" ||
		productErr.Code == "attachment.request.invalid" ||
		productErr.Code == "relation.request.invalid" ||
		strings.HasPrefix(productErr.Code, "mutation.contract."):
		status = http.StatusBadRequest
	case strings.HasSuffix(productErr.Code, ".not_found") ||
		strings.HasSuffix(productErr.Code, "_not_found") ||
		productErr.Code == "attachment.file_missing" ||
		productErr.Code == "attachment.metadata_missing":
		status = http.StatusNotFound
	case strings.HasSuffix(productErr.Code, "_conflict") ||
		strings.HasSuffix(productErr.Code, ".conflict"):
		status = http.StatusConflict
	case productErr.Code == "mutation.storage.failed" ||
		productErr.Code == "mutation.internal.failed" ||
		productErr.Code == "attachment.storage_failed" ||
		productErr.Code == "attachment.integrity_failed" ||
		productErr.Code == "attachment.thumbnail_failed" ||
		productErr.Code == "attachment.capability_failed":
		status = http.StatusInternalServerError
	}
	return status
}
