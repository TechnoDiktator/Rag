package documents

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStatusReader struct {
	status Status
	err    error
}

func (reader fakeStatusReader) GetStatus(_ context.Context, _ uuid.UUID) (Status, error) {
	return reader.status, reader.err
}

func TestStatusHandlerReturnsDocumentStatus(t *testing.T) {
	pageCount := 3
	reader := fakeStatusReader{status: Status{
		DocumentID: uuid.NewString(),
		Version:    1,
		Status:     "COMPLETED",
		PageCount:  &pageCount,
		JobStage:   "PARSE",
		JobStatus:  "COMPLETED",
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}}
	handler := NewStatusHandler(reader, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/documents/"+reader.status.DocumentID+"/status", nil)
	request.SetPathValue("documentID", reader.status.DocumentID)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Body.String(); !containsAll(got, `"status":"COMPLETED"`, `"page_count":3`, `"job_status":"COMPLETED"`) {
		t.Errorf("response missing expected status fields: %s", got)
	}
}

func TestStatusHandlerRejectsInvalidDocumentID(t *testing.T) {
	handler := NewStatusHandler(fakeStatusReader{}, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/documents/not-a-uuid/status", nil)
	request.SetPathValue("documentID", "not-a-uuid")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestStatusHandlerReturnsNotFound(t *testing.T) {
	handler := NewStatusHandler(fakeStatusReader{err: ErrDocumentNotFound}, nil)
	id := uuid.NewString()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/documents/"+id+"/status", nil)
	request.SetPathValue("documentID", id)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestStatusHandlerReturnsInternalError(t *testing.T) {
	handler := NewStatusHandler(fakeStatusReader{err: errors.New("database unavailable")}, nil)
	id := uuid.NewString()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/documents/"+id+"/status", nil)
	request.SetPathValue("documentID", id)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}

func containsAll(value string, expected ...string) bool {
	for _, item := range expected {
		if !strings.Contains(value, item) {
			return false
		}
	}
	return true
}
