CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE document_chunk_embeddings (
    document_id UUID NOT NULL,
    version INTEGER NOT NULL,
    chunk_index INTEGER NOT NULL,
    embedding_model TEXT NOT NULL,
    vision_model TEXT,
    description TEXT,
    searchable_text TEXT NOT NULL CHECK (length(trim(searchable_text)) > 0),
    embedding vector(1536) NOT NULL,
    PRIMARY KEY (document_id, version, chunk_index),
    FOREIGN KEY (document_id, version, chunk_index)
        REFERENCES document_chunks(document_id, version, chunk_index) ON DELETE CASCADE
);
