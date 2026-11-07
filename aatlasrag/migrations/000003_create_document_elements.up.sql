CREATE TABLE document_elements (
    document_id UUID NOT NULL,
    version INTEGER NOT NULL,
    element_index INTEGER NOT NULL CHECK (element_index >= 0),
    page_number INTEGER NOT NULL CHECK (page_number >= 1),
    category TEXT NOT NULL,
    content JSONB NOT NULL CHECK (jsonb_typeof(content) = 'object'),
    image_object_key TEXT,
    PRIMARY KEY (document_id, version, element_index),
    FOREIGN KEY (document_id, version)
        REFERENCES document_versions(document_id, version)
        ON DELETE CASCADE
);
