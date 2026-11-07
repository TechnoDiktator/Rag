package documents

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
)

type pdfKeyReader interface {
	GetPDFKey(context.Context, uuid.UUID, int) (string, error)
}

type pdfStore interface {
	OpenPDF(context.Context, string) (io.ReadSeekCloser, time.Time, error)
}

type PDFHandler struct {
	reader pdfKeyReader
	store  pdfStore
	logger *log.Logger
}

func NewPDFHandler(reader pdfKeyReader, store pdfStore, logger *log.Logger) *PDFHandler {
	if logger == nil {
		logger = log.Default()
	}
	return &PDFHandler{reader: reader, store: store, logger: logger}
}

func (handler *PDFHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("documentID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "document ID must be a UUID")
		return
	}
	version, err := strconv.Atoi(r.URL.Query().Get("version"))
	if err != nil || version < 1 {
		writeJSONError(w, http.StatusBadRequest, "version must be a positive integer")
		return
	}
	key, err := handler.reader.GetPDFKey(r.Context(), id, version)
	if errors.Is(err, ErrDocumentNotFound) {
		writeJSONError(w, http.StatusNotFound, "document version not found")
		return
	}
	if err != nil {
		handler.logger.Printf("get PDF key for %s: %v", id, err)
		writeJSONError(w, http.StatusInternalServerError, "could not retrieve PDF")
		return
	}
	file, modified, err := handler.store.OpenPDF(r.Context(), key)
	if err != nil {
		handler.logger.Printf("open PDF for %s: %v", id, err)
		writeJSONError(w, http.StatusInternalServerError, "could not retrieve PDF")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="document.pdf"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "document.pdf", modified, file)
}
