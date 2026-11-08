CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE CHECK (email = lower(trim(email)) AND length(email) > 0),
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    created_by UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE project_members (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);

CREATE INDEX idx_project_members_user ON project_members (user_id, project_id);
ALTER TABLE documents ADD COLUMN project_id UUID REFERENCES projects(id);
ALTER TABLE documents ADD COLUMN uploaded_by UUID REFERENCES users(id);
CREATE INDEX idx_documents_project ON documents (project_id);

DROP INDEX uq_documents_shared_content_hash;
CREATE UNIQUE INDEX uq_documents_legacy_content_hash
    ON documents (content_hash)
    WHERE tenant_id IS NULL AND project_id IS NULL;
CREATE UNIQUE INDEX uq_documents_project_content_hash
    ON documents (project_id, content_hash)
    WHERE project_id IS NOT NULL;
