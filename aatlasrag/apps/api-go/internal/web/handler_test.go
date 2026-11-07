package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedAssets(t *testing.T) {
	handler := Handler()
	for _, tc := range []struct {
		path, contentType string
		status            int
	}{
		{"/", "text/html", http.StatusOK},
		{"/app.css", "text/css", http.StatusOK},
		{"/app.js", "javascript", http.StatusOK},
		{"/missing", "text/plain", http.StatusNotFound},
	} {
		t.Run(tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d", response.Code, tc.status)
			}
			if !strings.Contains(response.Header().Get("Content-Type"), tc.contentType) {
				t.Fatalf("unexpected content type: %s", response.Header().Get("Content-Type"))
			}
		})
	}
}
