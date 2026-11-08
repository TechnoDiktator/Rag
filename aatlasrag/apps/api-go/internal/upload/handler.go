package upload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"atlasrag/api-go/internal/auth"
	"atlasrag/api-go/internal/documents"
)

const multipartMemoryBytes = 1 << 20
const multipartOverheadBytes = 1 << 20

var errInvalidPDF = errors.New("uploaded file is not a PDF")

type Registrar interface {
	RegisterUploaded(context.Context, documents.Registration) error
}

type ObjectStore interface {
	PutPDF(context.Context, string, io.Reader, int64) error
	Delete(context.Context, string) error
}

type Handler struct {
	registrar     Registrar
	objectStore   ObjectStore
	maxUploadSize int64
	logger        *log.Logger
}

type uploadResponse struct {
	DocumentID string `json:"document_id"`
	Status     string `json:"status"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func NewHandler(registrar Registrar, objectStore ObjectStore, maxUploadSize int64, logger *log.Logger) *Handler {
	return &Handler{
		registrar:     registrar,
		objectStore:   objectStore,
		maxUploadSize: maxUploadSize,
		logger:        logger,
	}
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	projectID, hasProject := auth.ProjectID(r.Context())
	userID, hasUser := auth.UserID(r.Context())
	if !hasProject || !hasUser {
		writeError(w, http.StatusUnauthorized, "project authentication required")
		return
	}

	correlationID, err := correlationIDFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "X-Correlation-ID must be a UUID")
		return
	}

	maxRequestBytes := handler.maxUploadSize + multipartOverheadBytes
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if err := r.ParseMultipartForm(multipartMemoryBytes); err != nil {
		if isRequestTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request exceeds the upload size limit")
		} else {
			writeError(w, http.StatusBadRequest, "expected multipart form with one PDF file in field 'file'")
		}
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if len(r.MultipartForm.Value) != 0 || len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["file"]) != 1 {
		writeError(w, http.StatusBadRequest, "upload exactly one PDF file in field 'file'")
		return
	}

	fileHeader := r.MultipartForm.File["file"][0]
	filename := safeFilename(fileHeader.Filename)
	if filename == "" || strings.ContainsRune(filename, '\x00') {
		writeError(w, http.StatusBadRequest, "file must have a valid filename")
		return
	}

	tempFile, contentHash, fileSize, err := stagePDF(fileHeader, handler.maxUploadSize)
	if err != nil {
		var tooLarge *uploadTooLargeError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "file exceeds the upload size limit")
		} else if errors.Is(err, errInvalidPDF) {
			writeError(w, http.StatusBadRequest, errInvalidPDF.Error())
		} else {
			handler.internalError(w, "stage uploaded PDF", err)
		}
		return
	}
	defer func() {
		_ = tempFile.Close()
		_ = os.Remove(tempFile.Name())
	}()

	documentID := uuid.New()
	eventID := uuid.New()
	objectKey := fmt.Sprintf("documents/%s/v1/source.pdf", documentID)
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		handler.internalError(w, "rewind uploaded PDF", err)
		return
	}
	if err := handler.objectStore.PutPDF(r.Context(), objectKey, tempFile, fileSize); err != nil {
		if cleanupErr := handler.deleteObjectAfterFailure(objectKey); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
		handler.internalError(w, "store uploaded PDF", err)
		return
	}

	registration := documents.Registration{
		DocumentID:    documentID,
		ProjectID:     projectID,
		UploadedBy:    userID,
		EventID:       eventID,
		CorrelationID: correlationID,
		Filename:      filename,
		ObjectKey:     objectKey,
		ContentHash:   contentHash,
		FileSizeBytes: fileSize,
		OccurredAt:    time.Now().UTC(),
	}
	if err := handler.registrar.RegisterUploaded(r.Context(), registration); err != nil {
		if errors.Is(err, documents.ErrCommitOutcomeUnknown) {
			handler.internalError(w, "document registration commit outcome is unknown; retaining uploaded object", err)
			return
		}
		cleanupErr := handler.deleteObjectAfterFailure(objectKey)
		if errors.Is(err, documents.ErrDuplicateContent) {
			if cleanupErr != nil {
				handler.internalError(w, "clean up duplicate upload", cleanupErr)
				return
			}
			writeError(w, http.StatusConflict, "a document with the same content already exists")
			return
		}
		if cleanupErr != nil {
			handler.internalError(w, "register document and clean up stored object", errors.Join(err, cleanupErr))
			return
		}
		handler.internalError(w, "register document", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(uploadResponse{
		DocumentID: documentID.String(),
		Status:     "QUEUED",
	}); err != nil && handler.logger != nil {
		handler.logger.Printf("write upload response: %v", err)
	}
}

func correlationIDFromRequest(r *http.Request) (uuid.UUID, error) {
	header := r.Header.Get("X-Correlation-ID")
	if header == "" {
		return uuid.New(), nil
	}
	return uuid.Parse(header)
}

func safeFilename(filename string) string {
	filename = strings.ReplaceAll(filename, "\\", "/")
	return path.Base(strings.TrimSpace(filename))
}

func stagePDF(fileHeader *multipart.FileHeader, maxSize int64) (*os.File, string, int64, error) {
	source, err := fileHeader.Open()
	if err != nil {
		return nil, "", 0, fmt.Errorf("open multipart file: %w", err)
	}
	defer source.Close()

	tempFile, err := os.CreateTemp("", "atlasrag-upload-*.pdf")
	if err != nil {
		return nil, "", 0, fmt.Errorf("create temporary upload file: %w", err)
	}
	keepTempFile := false
	defer func() {
		if !keepTempFile {
			_ = tempFile.Close()
			_ = os.Remove(tempFile.Name())
		}
	}()

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tempFile, hasher), io.LimitReader(source, maxSize+1))
	if err != nil {
		return nil, "", 0, fmt.Errorf("copy uploaded file to temporary storage: %w", err)
	}
	if size > maxSize {
		return nil, "", 0, &uploadTooLargeError{}
	}
	var signature [5]byte
	if _, err := tempFile.ReadAt(signature[:], 0); err != nil {
		return nil, "", 0, fmt.Errorf("verify staged PDF signature: %w", err)
	}
	if string(signature[:]) != "%PDF-" {
		return nil, "", 0, fmt.Errorf("%w: missing PDF signature", errInvalidPDF)
	}
	keepTempFile = true
	return tempFile, hex.EncodeToString(hasher.Sum(nil)), size, nil
}

type uploadTooLargeError struct{}

func (*uploadTooLargeError) Error() string {
	return "file exceeds configured upload size limit"
}

func isRequestTooLarge(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}

func (handler *Handler) deleteObjectAfterFailure(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := handler.objectStore.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete unregistered object %q: %w", key, err)
	}
	return nil
}

func (handler *Handler) internalError(w http.ResponseWriter, operation string, err error) {
	if handler.logger != nil {
		handler.logger.Printf("%s: %v", operation, err)
	}
	writeError(w, http.StatusInternalServerError, "could not complete document upload")
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: message})
}
