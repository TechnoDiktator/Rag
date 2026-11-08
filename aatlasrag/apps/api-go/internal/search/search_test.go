package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"atlasrag/api-go/internal/auth"
)

type fakeEmbedder struct {
	vector []float64
	err    error
	calls  int
}

func (e *fakeEmbedder) Embed(_ context.Context, _ string) ([]float64, error) {
	e.calls++
	return e.vector, e.err
}

type fakeReader struct {
	results []Result
	err     error
	calls   int
	limit   int
	id      *uuid.UUID
}

func (r *fakeReader) Search(_ context.Context, _ string, _ []float64, id *uuid.UUID, limit int) ([]Result, error) {
	r.calls++
	r.id = id
	r.limit = limit
	return r.results, r.err
}
func goodVector() []float64 { v := make([]float64, Dimensions); v[0] = 1; return v }

func TestSearchHandlerValidationAndResults(t *testing.T) {
	e := &fakeEmbedder{vector: goodVector()}
	r := &fakeReader{results: []Result{{DocumentID: uuid.NewString(), Pages: []int{2}, Score: 0.1}}}
	h := NewHandler(r, e, nil)
	for _, body := range []string{
		`{}`, `{"query":"  "}`, `{"query":"ok","top_k":0}`, `{"query":"ok","top_k":21}`,
		`{"query":"ok","document_id":"bad"}`, `{"query":"ok","extra":1}`, `{"query":"ok"} {}`,
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		if w.Code != 400 {
			t.Errorf("%s: got %d", body, w.Code)
		}
	}
	if e.calls != 0 {
		t.Fatal("invalid request sent to embedder")
	}
	id := uuid.NewString()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fmt.Sprintf(`{"query":" test ","top_k":5,"document_id":"%s"}`, id))))
	if w.Code != 200 || r.limit != 5 || r.id.String() != id || !strings.Contains(w.Body.String(), `"pages":[2]`) {
		t.Fatalf("unexpected result: %d %s", w.Code, w.Body)
	}
}

func TestSearchHandlerErrors(t *testing.T) {
	tests := []struct {
		embedder Embedder
		reader   *fakeReader
		want     int
	}{
		{nil, &fakeReader{}, 503},
		{&fakeEmbedder{err: errors.New("unavailable")}, &fakeReader{}, 502},
		{&fakeEmbedder{vector: []float64{1}}, &fakeReader{}, 502},
		{&fakeEmbedder{vector: make([]float64, Dimensions)}, &fakeReader{}, 502},
		{&fakeEmbedder{vector: goodVector()}, &fakeReader{err: errors.New("database")}, 500},
	}
	for _, tc := range tests {
		w := httptest.NewRecorder()
		NewHandler(tc.reader, tc.embedder, nil).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"query":"hello"}`)))
		if w.Code != tc.want {
			t.Errorf("got %d, want %d", w.Code, tc.want)
		}
	}
}

func TestOpenAIEmbedderRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing authorization")
		}
		var request struct {
			Model      string `json:"model"`
			Input      string `json:"input"`
			Dimensions int    `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != Model || request.Input != "example" || request.Dimensions != Dimensions {
			t.Errorf("unexpected request: %+v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": goodVector()}}})
	}))
	defer server.Close()
	e := &OpenAIEmbedder{apiKey: "test-key", client: server.Client(), endpoint: server.URL}
	vector, err := e.Embed(context.Background(), "example")
	if err != nil || len(vector) != Dimensions {
		t.Fatalf("unexpected response: %v", err)
	}
}

// Uses only temporary tables on a dedicated connection; it never changes the
// application's persistent rows. Set ATLASRAG_TEST_DATABASE_URL to opt in.
func TestHybridSearchPostgres(t *testing.T) {
	url := os.Getenv("ATLASRAG_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("ATLASRAG_TEST_DATABASE_URL not set")
	}
	ctx := auth.WithProjectID(context.Background(), uuid.New())
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `
CREATE TEMP TABLE documents (id uuid, current_version int, status text, filename text, project_id uuid);
CREATE TEMP TABLE document_chunks (document_id uuid, version int, chunk_index int, content text);
CREATE TEMP TABLE document_chunk_embeddings (document_id uuid, version int, chunk_index int, searchable_text text, description text, embedding vector(1536), embedding_model text);
CREATE TEMP TABLE document_chunk_sources (document_id uuid, version int, chunk_index int, page_number int);
`)
	if err != nil {
		t.Fatal(err)
	}
	id1, id2 := uuid.New(), uuid.New()
	v1, _ := json.Marshal(goodVector())
	v2 := goodVector()
	v2[0] = 0
	v2[1] = 1
	encoded2, _ := json.Marshal(v2)
	projectID, _ := auth.ProjectID(ctx)
	_, err = conn.Exec(ctx, `INSERT INTO documents VALUES ($1,1,'COMPLETED','one.pdf',$3),($2,1,'COMPLETED','two.pdf',$3)`, id1, id2, projectID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `INSERT INTO document_chunks VALUES ($1,1,0,'semantic result'),($2,1,0,'rare keyword')`, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `INSERT INTO document_chunk_embeddings VALUES
  ($1,1,0,'semantic result',NULL,$3::vector,$5),
  ($2,1,0,'diagram shows quasar quasar', 'diagram shows quasar', $4::vector,$5)`,
		id1, id2, string(v1), string(encoded2), Model)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, `INSERT INTO document_chunk_sources VALUES ($1,1,0,1),($2,1,0,3)`, id1, id2)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(conn)
	results, err := repo.Search(ctx, "quasar", goodVector(), nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].DocumentID != id2.String() || results[1].DocumentID != id1.String() ||
		results[0].KeywordRank == nil || *results[0].KeywordRank != 1 || results[1].KeywordRank != nil ||
		results[1].VectorRank == nil || *results[1].VectorRank != 1 ||
		len(results[0].Pages) != 1 || results[0].Pages[0] != 3 || results[0].Description == nil {
		t.Fatalf("unexpected hybrid ranking: %+v", results)
	}
	filtered, err := repo.Search(ctx, "quasar", goodVector(), &id2, 1)
	if err != nil || len(filtered) != 1 || filtered[0].DocumentID != id2.String() {
		t.Fatalf("filter: %+v, %v", filtered, err)
	}
	w := httptest.NewRecorder()
	NewHandler(repo, &fakeEmbedder{vector: goodVector()}, nil).ServeHTTP(
		w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"query":"quasar","top_k":2}`)).WithContext(ctx))
	var response struct {
		Results []Result `json:"results"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil ||
		len(response.Results) != 2 || response.Results[0].DocumentID != id2.String() {
		t.Fatalf("HTTP to PostgreSQL search: %d %s", w.Code, w.Body)
	}
	noResults, err := repo.Search(ctx, "quasar", goodVector(), &uuid.Nil, 3)
	if err != nil || len(noResults) != 0 {
		t.Fatalf("empty corpus: %+v, %v", noResults, err)
	}
	_, err = conn.Exec(ctx, `UPDATE document_chunk_embeddings SET searchable_text = 'quasar other' WHERE document_id = $1`, id1)
	if err != nil {
		t.Fatal(err)
	}
	scored, err := repo.Search(ctx, "quasar quasar", goodVector(), nil, 3)
	if err != nil || len(scored) != 2 || scored[0].DocumentID != id1.String() ||
		scored[0].KeywordRank == nil || *scored[0].KeywordRank != 2 ||
		scored[1].DocumentID != id2.String() || scored[1].KeywordRank == nil || *scored[1].KeywordRank != 1 {
		t.Fatalf("BM25 term frequency ranking: %+v, %v", scored, err)
	}
	_, err = conn.Exec(ctx, `UPDATE document_chunk_embeddings SET embedding_model = 'other-model' WHERE document_id = $1`, id2)
	if err != nil {
		t.Fatal(err)
	}
	modelFiltered, err := repo.Search(ctx, "quasar", goodVector(), nil, 3)
	if err != nil || len(modelFiltered) != 1 || modelFiltered[0].DocumentID != id1.String() {
		t.Fatalf("embedding model filter: %+v, %v", modelFiltered, err)
	}
	otherProject := uuid.New()
	if _, err := conn.Exec(ctx, `UPDATE documents SET project_id=$1 WHERE id=$2`, otherProject, id1); err != nil {
		t.Fatal(err)
	}
	isolated, err := repo.Search(ctx, "quasar", goodVector(), nil, 3)
	if err != nil || len(isolated) != 0 {
		t.Fatalf("foreign project document leaked: %+v, %v", isolated, err)
	}
	otherCtx := auth.WithProjectID(ctx, otherProject)
	own, err := repo.Search(otherCtx, "quasar", goodVector(), nil, 3)
	if err != nil || len(own) != 1 || own[0].DocumentID != id1.String() {
		t.Fatalf("project scoped retrieval failed: %+v, %v", own, err)
	}
	foreignFilter, err := repo.Search(ctx, "quasar", goodVector(), &id1, 3)
	if err != nil || len(foreignFilter) != 0 {
		t.Fatalf("foreign document filter leaked: %+v, %v", foreignFilter, err)
	}
	if _, err := repo.Search(context.Background(), "quasar", goodVector(), nil, 3); err == nil {
		t.Fatal("search without project scope succeeded")
	}
}

func TestOpenAIProviderErrorDoesNotExposeBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte("sensitive provider response"))
	}))
	defer server.Close()
	e := &OpenAIEmbedder{apiKey: "test-key", client: server.Client(), endpoint: server.URL}
	_, err := e.Embed(context.Background(), "example")
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("429")) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unexpected provider error: %v", err)
	}
}
