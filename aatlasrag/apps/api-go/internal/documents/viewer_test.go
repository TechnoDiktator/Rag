package documents

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeChunkReader struct {
	result ChunkPage
	err    error
	offset int
}

func (reader *fakeChunkReader) GetChunks(_ context.Context, _ uuid.UUID, offset int) (ChunkPage, error) {
	reader.offset = offset
	return reader.result, reader.err
}

type fakePDFReader struct{ err error }

func (reader fakePDFReader) GetPDFKey(_ context.Context, _ uuid.UUID, _ int) (string, error) {
	return "known-key", reader.err
}

type seekCloser struct{ *bytes.Reader }

func (seekCloser) Close() error { return nil }

type fakePDFStore struct{ err error }

func (store fakePDFStore) OpenPDF(_ context.Context, key string) (io.ReadSeekCloser, time.Time, error) {
	if store.err != nil {
		return nil, time.Time{}, store.err
	}
	if key != "known-key" {
		return nil, time.Time{}, errors.New("unexpected object key")
	}
	return seekCloser{bytes.NewReader([]byte("%PDF-test"))}, time.Time{}, nil
}

func viewerRequest(path, id string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.SetPathValue("documentID", id)
	return request
}

func TestChunkHandler(t *testing.T) {
	id := uuid.NewString()
	description := "Diagram description"
	reader := &fakeChunkReader{result: ChunkPage{Version: 1, Chunks: []Chunk{
		{Index: 2, Text: "Example", Pages: []int{1, 2}, Description: &description},
	}, HasMore: true}}
	handler := NewChunkHandler(reader, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, viewerRequest("/?offset=20", id))
	if response.Code != http.StatusOK || reader.offset != 20 ||
		!containsAll(response.Body.String(), `"version":1`, `"pages":[1,2]`, `"description":"Diagram description"`, `"has_more":true`) {
		t.Fatalf("unexpected response: %d %s, offset %d", response.Code, response.Body, reader.offset)
	}
	for _, path := range []string{"/?offset=-1", "/?offset=abc", "/?offset=1000001"} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, viewerRequest(path, id))
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s: got status %d", path, response.Code)
		}
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, viewerRequest("/", "invalid"))
	if response.Code != http.StatusBadRequest {
		t.Errorf("invalid ID: got status %d", response.Code)
	}
	for _, tc := range []struct {
		err  error
		code int
	}{{ErrDocumentNotFound, 404}, {errors.New("database failure"), 500}} {
		reader.err = tc.err
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, viewerRequest("/", id))
		if response.Code != tc.code {
			t.Errorf("error %v: got status %d", tc.err, response.Code)
		}
	}
}

func TestPDFHandler(t *testing.T) {
	id := uuid.NewString()
	handler := NewPDFHandler(fakePDFReader{}, fakePDFStore{}, nil)
	response := httptest.NewRecorder()
	request := viewerRequest("/?version=1", id)
	request.Header.Set("Range", "bytes=0-3")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "%PDF" ||
		response.Header().Get("Content-Type") != "application/pdf" ||
		!strings.Contains(response.Header().Get("Content-Range"), "bytes 0-3/") {
		t.Fatalf("unexpected range response: %d %s %v", response.Code, response.Body, response.Header())
	}
	for _, tc := range []struct {
		id, path string
		code     int
	}{
		{"bad", "/?version=1", 400}, {id, "/", 400}, {id, "/?version=0", 400},
	} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, viewerRequest(tc.path, tc.id))
		if response.Code != tc.code {
			t.Errorf("%s %s: got %d", tc.id, tc.path, response.Code)
		}
	}
	for _, tc := range []struct {
		reader fakePDFReader
		store  fakePDFStore
		code   int
	}{
		{fakePDFReader{ErrDocumentNotFound}, fakePDFStore{}, 404},
		{fakePDFReader{errors.New("database failure")}, fakePDFStore{}, 500},
		{fakePDFReader{}, fakePDFStore{errors.New("storage failure")}, 500},
	} {
		response = httptest.NewRecorder()
		NewPDFHandler(tc.reader, tc.store, nil).ServeHTTP(response, viewerRequest("/?version=1", id))
		if response.Code != tc.code {
			t.Errorf("got %d, want %d", response.Code, tc.code)
		}
	}
}
