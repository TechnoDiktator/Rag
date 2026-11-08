package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type projectKey struct{}

func ProjectID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(projectKey{}).(uuid.UUID)
	return id, ok
}

func WithProjectID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, projectKey{}, id)
}

func (s *Service) ProjectAccess(next http.Handler, documentScoped bool) http.Handler {
	return s.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, _ := UserID(r.Context())
		projectID, err := uuid.Parse(r.PathValue("projectID"))
		if err != nil {
			writeError(w, 400, "invalid project ID")
			return
		}
		var allowed bool
		err = s.db.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM project_members WHERE project_id=$1 AND user_id=$2)", projectID, userID).Scan(&allowed)
		if err != nil {
			s.logger.Printf("project authorization: %v", err)
			writeError(w, 500, "authorization failed")
			return
		}
		if !allowed {
			writeError(w, 404, "project not found")
			return
		}
		if documentScoped {
			documentID, err := uuid.Parse(r.PathValue("documentID"))
			if err != nil {
				writeError(w, 400, "invalid document ID")
				return
			}
			err = s.db.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM documents WHERE id=$1 AND project_id=$2)", documentID, projectID).Scan(&allowed)
			if err != nil {
				s.logger.Printf("document authorization: %v", err)
				writeError(w, 500, "authorization failed")
				return
			}
			if !allowed {
				writeError(w, 404, "document not found")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(WithProjectID(r.Context(), projectID)))
	}))
}

func readJSON(w http.ResponseWriter, r *http.Request, dest any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(dest); err != nil {
		writeError(w, 400, "invalid request")
		return false
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, 400, "invalid request")
		return false
	}
	return true
}

func (s *Service) CreateProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if len(input.Name) == 0 || len(input.Name) > 200 {
		writeError(w, 400, "project name must contain 1 to 200 bytes")
		return
	}
	userID, _ := UserID(r.Context())
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		s.logger.Printf("create project: %v", err)
		writeError(w, 500, "could not create project")
		return
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			s.logger.Printf("roll back project creation: %v", err)
		}
	}()
	var id uuid.UUID
	err = tx.QueryRow(r.Context(), "INSERT INTO projects (name, created_by) VALUES ($1,$2) RETURNING id", input.Name, userID).Scan(&id)
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO project_members (project_id,user_id) VALUES ($1,$2)", id, userID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.logger.Printf("create project: %v", err)
		writeError(w, 500, "could not create project")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	if err := json.NewEncoder(w).Encode(map[string]any{"id": id, "name": input.Name}); err != nil {
		s.logger.Printf("write project response: %v", err)
	}
}

func (s *Service) ListProjects(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	rows, err := s.db.Query(r.Context(), "SELECT p.id,p.name FROM projects p JOIN project_members m ON m.project_id=p.id WHERE m.user_id=$1 ORDER BY p.created_at,p.id", userID)
	if err != nil {
		s.logger.Printf("list projects: %v", err)
		writeError(w, 500, "could not list projects")
		return
	}
	defer rows.Close()
	type project struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}
	items := []project{}
	for rows.Next() {
		var item project
		if err = rows.Scan(&item.ID, &item.Name); err != nil {
			break
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		s.logger.Printf("read projects: %v", err)
		writeError(w, 500, "could not list projects")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"projects": items}); err != nil {
		s.logger.Printf("write projects response: %v", err)
	}
}

func (s *Service) AddMember(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email string `json:"email"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	email, ok := normalizeEmail(input.Email)
	if !ok {
		writeError(w, 400, "invalid email")
		return
	}
	projectID, _ := ProjectID(r.Context())
	var userID uuid.UUID
	err := s.db.QueryRow(r.Context(), "SELECT id FROM users WHERE email=$1", email).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "user not found")
		return
	}
	if err != nil {
		s.logger.Printf("find member: %v", err)
		writeError(w, 500, "could not add member")
		return
	}
	_, err = s.db.Exec(r.Context(), "INSERT INTO project_members (project_id,user_id) VALUES ($1,$2) ON CONFLICT DO NOTHING", projectID, userID)
	if err != nil {
		s.logger.Printf("add member: %v", err)
		writeError(w, 500, "could not add member")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) ListDocuments(w http.ResponseWriter, r *http.Request) {
	projectID, _ := ProjectID(r.Context())
	rows, err := s.db.Query(r.Context(), "SELECT id, filename, status FROM documents WHERE project_id=$1 ORDER BY created_at DESC, id", projectID)
	if err != nil {
		s.logger.Printf("list documents: %v", err)
		writeError(w, 500, "could not list documents")
		return
	}
	defer rows.Close()
	type document struct {
		ID       uuid.UUID `json:"id"`
		Filename string    `json:"filename"`
		Status   string    `json:"status"`
	}
	items := []document{}
	for rows.Next() {
		var item document
		if err = rows.Scan(&item.ID, &item.Filename, &item.Status); err != nil {
			break
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		s.logger.Printf("read documents: %v", err)
		writeError(w, 500, "could not list documents")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"documents": items}); err != nil {
		s.logger.Printf("write documents response: %v", err)
	}
}
