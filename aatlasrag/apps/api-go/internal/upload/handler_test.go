package upload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"atlasrag/api-go/internal/documents"
)

type fakeRegistrar struct {
	registration documents.Registration
	err          error
	calls        int
}

func (registrar *fakeRegistrar) RegisterUploaded(_ context.Context, registration documents.Registration) error {
	registrar.calls++
	registrar.registration = registration
	return registrar.err
}

type fakeObjectStore struct {
	key       string
	body      []byte
	putCalls  int
	putErr    error
	deleteKey string
	deleteErr error
}

func (store *fakeObjectStore) PutPDF(_ context.Context, key string, body io.Reader, _ int64) error {
	store.putCalls++
	store.key = key
	if store.putErr != nil {
		return store.putErr
	}
	var err error
	store.body, err = io.ReadAll(body)
	return err
}

func (store *fakeObjectStore) Delete(_ context.Context, key string) error {
	store.deleteKey = key
	return store.deleteErr
}

func TestUploadRegistersDocumentAndStoresPDF(t *testing.T) {
	pdf := []byte("%PDF-1.7\nexample")
	registrar := &fakeRegistrar{}
	store := &fakeObjectStore{}
	handler := NewHandler(registrar, store, 1024, log.New(io.Discard, "", 0))

	response := performUpload(t, handler, `C:\private\report.pdf`, pdf, "")

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, http.StatusAccepted, response.Body)
	}
	if registrar.calls != 1 {
		t.Fatalf("registration calls = %d, want 1", registrar.calls)
	}
	if store.putCalls != 1 || !bytes.Equal(store.body, pdf) {
		t.Fatalf("stored PDF = %q, want %q", store.body, pdf)
	}

	registration := registrar.registration
	if registration.Filename != "report.pdf" {
		t.Errorf("filename = %q, want report.pdf", registration.Filename)
	}
	expectedHash := sha256.Sum256(pdf)
	if registration.ContentHash != hex.EncodeToString(expectedHash[:]) {
		t.Errorf("content hash = %s, want SHA-256 %s", registration.ContentHash, hex.EncodeToString(expectedHash[:]))
	}
	if registration.DocumentID == uuid.Nil || registration.EventID == uuid.Nil || registration.CorrelationID == uuid.Nil {
		t.Error("registration UUIDs must be populated")
	}
	if registration.ObjectKey != store.key || !strings.HasPrefix(store.key, "documents/"+registration.DocumentID.String()+"/v1/") {
		t.Errorf("object key %q does not match document ID", store.key)
	}
	if registration.FileSizeBytes != int64(len(pdf)) {
		t.Errorf("file size = %d, want %d", registration.FileSizeBytes, len(pdf))
	}

	var result uploadResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.DocumentID != registration.DocumentID.String() || result.Status != "QUEUED" {
		t.Errorf("response = %+v, want document ID and QUEUED status", result)
	}
}

func TestUploadRejectsNonPDFWithoutStoring(t *testing.T) {
	registrar := &fakeRegistrar{}
	store := &fakeObjectStore{}
	handler := NewHandler(registrar, store, 1024, log.New(io.Discard, "", 0))

	response := performUpload(t, handler, "notes.txt", []byte("not a PDF"), "")

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if registrar.calls != 0 || store.putCalls != 0 {
		t.Fatal("invalid PDF must not be stored or registered")
	}
}

func TestUploadRejectsOversizedFile(t *testing.T) {
	registrar := &fakeRegistrar{}
	store := &fakeObjectStore{}
	handler := NewHandler(registrar, store, 8, log.New(io.Discard, "", 0))

	response := performUpload(t, handler, "large.pdf", []byte("%PDF-123456"), "")

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
	if registrar.calls != 0 || store.putCalls != 0 {
		t.Fatal("oversized PDF must not be stored or registered")
	}
}

func TestUploadRejectsInvalidCorrelationID(t *testing.T) {
	registrar := &fakeRegistrar{}
	store := &fakeObjectStore{}
	handler := NewHandler(registrar, store, 1024, log.New(io.Discard, "", 0))

	response := performUpload(t, handler, "report.pdf", []byte("%PDF-1.7"), "not-a-uuid")

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if registrar.calls != 0 || store.putCalls != 0 {
		t.Fatal("invalid correlation ID must stop upload processing")
	}
}

func TestUploadRemovesObjectWhenRegistrationFails(t *testing.T) {
	registrar := &fakeRegistrar{err: documents.ErrDuplicateContent}
	store := &fakeObjectStore{}
	handler := NewHandler(registrar, store, 1024, log.New(io.Discard, "", 0))

	response := performUpload(t, handler, "report.pdf", []byte("%PDF-1.7"), "")

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusConflict)
	}
	if store.deleteKey != store.key {
		t.Errorf("deleted key = %q, uploaded key = %q", store.deleteKey, store.key)
	}
}

func TestUploadRetainsObjectWhenCommitOutcomeIsUnknown(t *testing.T) {
	registrar := &fakeRegistrar{err: documents.ErrCommitOutcomeUnknown}
	store := &fakeObjectStore{}
	handler := NewHandler(registrar, store, 1024, log.New(io.Discard, "", 0))

	response := performUpload(t, handler, "report.pdf", []byte("%PDF-1.7"), "")

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if store.deleteKey != "" {
		t.Fatal("object must be retained because the database transaction may have committed")
	}
}

func TestUploadCleansUpAfterObjectStoreFailure(t *testing.T) {
	registrar := &fakeRegistrar{}
	store := &fakeObjectStore{putErr: errors.New("storage failed")}
	handler := NewHandler(registrar, store, 1024, log.New(io.Discard, "", 0))

	response := performUpload(t, handler, "report.pdf", []byte("%PDF-1.7"), "")

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if store.deleteKey != store.key {
		t.Errorf("deleted key = %q, attempted upload key = %q", store.deleteKey, store.key)
	}
	if registrar.calls != 0 {
		t.Fatal("failed object upload must not register the document")
	}
}

func TestUploadReturnsServerErrorWhenObjectCleanupFails(t *testing.T) {
	registrar := &fakeRegistrar{err: errors.New("database unavailable")}
	store := &fakeObjectStore{deleteErr: errors.New("storage unavailable")}
	handler := NewHandler(registrar, store, 1024, log.New(io.Discard, "", 0))

	response := performUpload(t, handler, "report.pdf", []byte("%PDF-1.7"), "")

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}

func performUpload(t *testing.T, handler http.Handler, filename string, content []byte, correlationID string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/documents", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if correlationID != "" {
		request.Header.Set("X-Correlation-ID", correlationID)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
