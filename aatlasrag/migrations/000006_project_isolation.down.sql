DO $$
BEGIN
    IF EXISTS (
        SELECT content_hash FROM documents
        WHERE tenant_id IS NULL
        GROUP BY content_hash HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'Cannot remove project isolation: document hashes overlap across projects';
    END IF;
END $$;

DROP INDEX uq_documents_project_content_hash;
DROP INDEX uq_documents_legacy_content_hash;
DROP INDEX idx_documents_project;
ALTER TABLE documents DROP COLUMN uploaded_by;
ALTER TABLE documents DROP COLUMN project_id;
CREATE UNIQUE INDEX uq_documents_shared_content_hash
    ON documents (content_hash)
    WHERE tenant_id IS NULL;
DROP TABLE project_members;
DROP TABLE projects;
DROP TABLE users;
