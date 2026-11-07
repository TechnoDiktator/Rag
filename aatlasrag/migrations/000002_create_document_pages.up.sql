CREATE TABLE document_pages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id UUID NOT NULL,
    version INTEGER NOT NULL,
    page_number INTEGER NOT NULL CHECK (page_number >= 1),
    extracted_text TEXT NOT NULL DEFAULT '',
    extraction_method TEXT NOT NULL DEFAULT 'pypdf',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (document_id, version)
        REFERENCES document_versions(document_id, version)
        ON DELETE CASCADE,
    UNIQUE (document_id, version, page_number)
);

CREATE INDEX idx_document_pages_document_version
    ON document_pages (document_id, version);
