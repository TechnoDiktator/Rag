CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE documents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID,
    filename TEXT NOT NULL CHECK (length(trim(filename)) > 0),
    object_key TEXT NOT NULL CHECK (length(trim(object_key)) > 0),
    content_hash TEXT NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    mime_type TEXT NOT NULL CHECK (mime_type = 'application/pdf'),
    file_size_bytes BIGINT NOT NULL CHECK (file_size_bytes >= 0),
    page_count INTEGER CHECK (page_count IS NULL OR page_count >= 0),
    current_version INTEGER NOT NULL DEFAULT 1 CHECK (current_version >= 1),
    status TEXT NOT NULL DEFAULT 'UPLOADED'
        CHECK (status IN (
            'UPLOADED',
            'QUEUED',
            'PARSING',
            'ENRICHING',
            'CHUNKING',
            'EMBEDDING',
            'INDEXING',
            'COMPLETED',
            'FAILED'
        )),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_documents_tenant_content_hash
    ON documents (tenant_id, content_hash)
    WHERE tenant_id IS NOT NULL;

CREATE UNIQUE INDEX uq_documents_shared_content_hash
    ON documents (content_hash)
    WHERE tenant_id IS NULL;

CREATE INDEX idx_documents_status ON documents (status);
CREATE INDEX idx_documents_tenant ON documents (tenant_id);

CREATE TABLE document_versions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK (version >= 1),
    object_key TEXT NOT NULL CHECK (length(trim(object_key)) > 0),
    content_hash TEXT NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    parser_version TEXT,
    embedding_model TEXT,
    status TEXT NOT NULL DEFAULT 'UPLOADED'
        CHECK (status IN (
            'UPLOADED',
            'QUEUED',
            'PARSING',
            'ENRICHING',
            'CHUNKING',
            'EMBEDDING',
            'INDEXING',
            'COMPLETED',
            'FAILED'
        )),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (document_id, version)
);

CREATE TABLE ingestion_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id UUID NOT NULL,
    version INTEGER NOT NULL,
    stage TEXT NOT NULL
        CHECK (stage IN ('PARSE', 'ENRICH', 'CHUNK', 'EMBED', 'INDEX')),
    status TEXT NOT NULL DEFAULT 'QUEUED'
        CHECK (status IN ('QUEUED', 'RUNNING', 'COMPLETED', 'FAILED')),
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
    error_message TEXT,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (document_id, version)
        REFERENCES document_versions(document_id, version)
        ON DELETE CASCADE,
    UNIQUE (document_id, version, stage)
);

CREATE INDEX idx_ingestion_jobs_status_stage
    ON ingestion_jobs (status, stage);

CREATE TABLE outbox_events (
    id UUID PRIMARY KEY,
    aggregate_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    event_version SMALLINT NOT NULL CHECK (event_version >= 1),
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    CHECK (payload->>'event_id' = id::TEXT),
    CHECK (payload->>'event_type' = event_type),
    CHECK (payload->>'event_version' = event_version::TEXT),
    CHECK (payload->>'document_id' = aggregate_id::TEXT)
);

CREATE INDEX idx_outbox_events_unpublished
    ON outbox_events (created_at, id)
    WHERE published_at IS NULL;
