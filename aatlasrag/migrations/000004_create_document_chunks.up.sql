CREATE TABLE document_chunks (
    document_id UUID NOT NULL,
    version INTEGER NOT NULL,
    chunk_index INTEGER NOT NULL CHECK (chunk_index >= 0),
    content TEXT NOT NULL,
    PRIMARY KEY (document_id, version, chunk_index),
    FOREIGN KEY (document_id, version)
        REFERENCES document_versions(document_id, version)
        ON DELETE CASCADE
);

CREATE TABLE document_chunk_sources (
    document_id UUID NOT NULL,
    version INTEGER NOT NULL,
    chunk_index INTEGER NOT NULL,
    source_index INTEGER NOT NULL CHECK (source_index >= 0),
    page_number INTEGER NOT NULL CHECK (page_number >= 1),
    element_index INTEGER,
    PRIMARY KEY (document_id, version, chunk_index, source_index),
    FOREIGN KEY (document_id, version, chunk_index)
        REFERENCES document_chunks(document_id, version, chunk_index) ON DELETE CASCADE,
    FOREIGN KEY (document_id, version, page_number)
        REFERENCES document_pages(document_id, version, page_number) ON DELETE CASCADE,
    FOREIGN KEY (document_id, version, element_index)
        REFERENCES document_elements(document_id, version, element_index) ON DELETE CASCADE
);
