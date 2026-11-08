package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// All DDL and data are rolled back; the application schema is not modified.
func TestProjectMigrationPostgres(t *testing.T) {
	url := os.Getenv("ATLASRAG_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("ATLASRAG_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	schema := "atlasrag_test_" + uuid.New().String()[:8]
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{schema}.Sanitize()+", public"); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{
		"000001_create_document_lifecycle.up.sql",
		"000002_create_document_pages.up.sql",
		"000003_create_document_elements.up.sql",
		"000004_create_document_chunks.up.sql",
		"000005_create_chunk_embeddings.up.sql",
		"000006_project_isolation.up.sql",
	} {
		sql, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	_, err = tx.Exec(ctx, `
INSERT INTO users (id,email,password_hash) VALUES
('00000000-0000-0000-0000-000000000001','a@example.com','hash'),
('00000000-0000-0000-0000-000000000002','b@example.com','hash');
INSERT INTO projects (id,name,created_by) VALUES
('10000000-0000-0000-0000-000000000001','A','00000000-0000-0000-0000-000000000001'),
('10000000-0000-0000-0000-000000000002','B','00000000-0000-0000-0000-000000000002');
INSERT INTO project_members (project_id,user_id) VALUES
('10000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000001'),
('10000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000002');
INSERT INTO documents (project_id,uploaded_by,filename,object_key,content_hash,mime_type,file_size_bytes) VALUES
('10000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000001','a.pdf','a',repeat('a',64),'application/pdf',1),
('10000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000002','b.pdf','b',repeat('a',64),'application/pdf',1);
`)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM documents d JOIN project_members m ON m.project_id=d.project_id WHERE m.user_id=$1`,
		"00000000-0000-0000-0000-000000000001").Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("membership scope count = %d, error = %v", count, err)
	}
}
