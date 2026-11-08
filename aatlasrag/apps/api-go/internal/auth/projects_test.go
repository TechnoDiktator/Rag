package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRegistrationAndSharedProjectPostgres(t *testing.T) {
	url := os.Getenv("ATLASRAG_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("ATLASRAG_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `
	CREATE TEMP TABLE users (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), email text UNIQUE NOT NULL, password_hash text NOT NULL);
	CREATE TEMP TABLE projects (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL, created_by uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
	CREATE TEMP TABLE project_members (project_id uuid NOT NULL, user_id uuid NOT NULL, UNIQUE (project_id, user_id));
	`)
	if err != nil {
		t.Fatal(err)
	}
	s := testService(t)
	s.db = pool
	register := func(email string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"email":"`+email+`","password":"long enough password"}`))
		w := httptest.NewRecorder()
		s.Register(w, r)
		if w.Code != 201 {
			t.Fatalf("registration: %d %s", w.Code, w.Body)
		}
	}
	register("a@example.com")
	register("b@example.com")
	login := func(email string) string {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"email":"`+email+`","password":"long enough password"}`))
		w := httptest.NewRecorder()
		s.Login(w, r)
		var data struct {
			Token string `json:"access_token"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &data) != nil || data.Token == "" {
			t.Fatalf("login: %d %s", w.Code, w.Body)
		}
		return data.Token
	}
	tokenA, tokenB := login("a@example.com"), login("b@example.com")
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"shared"}`))
	r.Header.Set("Authorization", "Bearer "+tokenA)
	w := httptest.NewRecorder()
	s.Authenticate(http.HandlerFunc(s.CreateProject)).ServeHTTP(w, r)
	var project struct {
		ID uuid.UUID `json:"id"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &project) != nil || project.ID == uuid.Nil {
		t.Fatalf("create project: %d %s", w.Code, w.Body)
	}
	callMember := func(token, email string) int {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"email":"`+email+`"}`))
		r.SetPathValue("projectID", project.ID.String())
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ProjectAccess(http.HandlerFunc(s.AddMember), false).ServeHTTP(w, r)
		return w.Code
	}
	if code := callMember(tokenB, "b@example.com"); code != 404 {
		t.Fatalf("nonmember added self: %d", code)
	}
	if code := callMember(tokenA, "b@example.com"); code != 204 {
		t.Fatalf("member addition failed: %d", code)
	}
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+tokenB)
	w = httptest.NewRecorder()
	s.Authenticate(http.HandlerFunc(s.ListProjects)).ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), project.ID.String()) {
		t.Fatalf("shared project not visible: %d %s", w.Code, w.Body)
	}
}

func TestProjectAndDocumentAuthorizationPostgres(t *testing.T) {
	url := os.Getenv("ATLASRAG_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("ATLASRAG_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `
CREATE TEMP TABLE project_members (project_id uuid, user_id uuid);
CREATE TEMP TABLE documents (id uuid, project_id uuid);
`)
	if err != nil {
		t.Fatal(err)
	}
	userA, userB := uuid.New(), uuid.New()
	projectA, projectB := uuid.New(), uuid.New()
	docA, docB := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO project_members VALUES ($1,$2),($3,$4)`, projectA, userA, projectB, userB)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO documents VALUES ($1,$2),($3,$4)`, docA, projectA, docB, projectB)
	if err != nil {
		t.Fatal(err)
	}
	s := testService(t)
	s.db = pool
	tokenA, err := s.token(userA, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	handler := s.ProjectAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := ProjectID(r.Context())
		if !ok || id != projectA {
			t.Errorf("wrong project context: %v", id)
		}
		w.WriteHeader(204)
	}), true)
	for _, tc := range []struct {
		name              string
		project, document uuid.UUID
		want              int
	}{
		{"own document", projectA, docA, 204},
		{"foreign project", projectB, docB, 404},
		{"foreign document in own route", projectA, docB, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.SetPathValue("projectID", tc.project.String())
			r.SetPathValue("documentID", tc.document.String())
			r.Header.Set("Authorization", "Bearer "+tokenA)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d, want %d", w.Code, tc.want)
			}
		})
	}
	if _, err := pool.Exec(ctx, "DELETE FROM project_members WHERE project_id=$1 AND user_id=$2", projectA, userA); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.SetPathValue("projectID", projectA.String())
	r.SetPathValue("documentID", docA.String())
	r.Header.Set("Authorization", "Bearer "+tokenA)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("removed member retained access: %d", w.Code)
	}
}
