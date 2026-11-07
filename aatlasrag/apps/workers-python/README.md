# Python PDF worker

The worker consumes `document.uploaded` v1 messages from Kafka, downloads the
referenced object from MinIO, extracts per-page text with pypdf, and stores page
text, chunks and parse status in PostgreSQL.

Compose runs this worker with internal service addresses (`postgres:5432`,
`minio:9000`, and `kafka:29092`). Run `docker compose logs -f python-worker`
from `infra/` to follow its output.

Kafka offsets are committed only after the worker finishes a message. The
database writes are idempotent for page/document/version keys, so a message
redelivered after a crash can be safely processed again. Infrastructure errors
are not acknowledged and cause the worker to exit so Compose can restart it.
Malformed or unsupported events also stop the worker rather than being silently
discarded; a dead-letter queue and bounded retry policy are follow-up work.

The default pypdf mode extracts text from digitally generated PDFs; it does not
perform OCR or extract image and table structure.

## Optional Unstructured extraction

Set `PDF_EXTRACTOR=unstructured` in `infra/.env` and rebuild the worker to use
the notebook's `hi_res` PDF extraction path. The default is `pypdf`; changing
the setting does not reprocess existing documents. Apply migration 000003
before enabling the new mode. Extracted elements, page numbers, and table HTML
are stored in PostgreSQL; image payloads are stored in MinIO with object keys
in `document_elements`. Element and page writes commit together. Image keys
include a content hash, so retries do not overwrite different images. Failures
or reprocessing can leave unreferenced objects; cleanup is not implemented.

The worker image installs Poppler, Tesseract (English), and libmagic. The
layout model may need to download weights on the first run. This extraction
stage does not produce searchable answers. OCR and table accuracy depend on the input; test on your
own documents before relying on the output.

## Chunks and source references

Every newly processed document is chunked in the same transaction that stores
its pages and completes the parse. Unstructured uses the notebook's
`chunk_by_title` settings: 3000 maximum characters, 2400 soft limit, and 500
minimum combination threshold. Each chunk links to its source elements and
pages in `document_chunk_sources`. Source element records retain table HTML
and MinIO image keys. Image-only elements omitted by the chunker are saved as
empty-text chunks with source references, ready for later image descriptions.

With the default pypdf extractor, text is divided into chunks of up to 3000
characters within each page. There are no title, table or image references in
that mode. Blank pages produce no chunks. Existing documents are not
automatically backfilled; changing extractor or chunking requires reprocessing
through a future workflow. Migration 000004 is required before running the new
worker. The current `COMPLETED` status indicates parsing and chunking only,
not retrieval readiness. By default, embeddings are not generated.

## Optional OpenAI descriptions and embeddings

After enabling pgvector and applying migration 000005 as described in the
infrastructure README, set `OPENAI_INDEXING_ENABLED=true` and `OPENAI_API_KEY`
in your ignored `infra/.env`, then rebuild and recreate the worker. Use
`PDF_EXTRACTOR=unstructured` to extract images and tables; pypdf only indexes
its extracted text. Enabling this feature sends chunk text, table HTML and
images to OpenAI and incurs API charges. Do not enable it for documents you
cannot send to that provider.

The worker describes image/table chunks with `gpt-4o`, embeds the text plus
description with `text-embedding-3-small` (1536 dimensions), and stores
vectors in `document_chunk_embeddings`. It stores references to original
assets separately; it does not embed image bytes. The Go API can search indexed
chunks; question answering is not implemented. Existing documents are not automatically
reprocessed.

API, validation or database failures prevent completion and are logged; the
current Kafka retry/restart behavior can repeat chargeable requests after a
failure or redelivery. The OpenAI client has automatic retries disabled, but
there is no durable per-chunk billing cache, budget limit, or dead-letter
queue. Disable indexing or stop the worker if retries occur. Large tables
(over 30000 characters), unsupported image types, images over 10 MiB, and
chunks with more than 10 images fail rather than being silently truncated.
When indexing is enabled, documents without searchable chunks fail rather
than receiving empty embeddings.

An empty Unstructured result marks the parse as failed, including for a blank
PDF. A nonempty result that produces no chunks or image references raises an
error. Other extraction, chunking or infrastructure errors stop the worker without
acknowledging the message and may cause repeated retries. The Kafka poll
interval defaults to 30 minutes (`KAFKA_MAX_POLL_INTERVAL_MS`); an extraction
taking longer may be redelivered. This is a local-development worker, not a
production job scheduling or retry solution.

Run the unit tests from this directory after installing the project:

```powershell
python -m pip install -e .
python -m unittest discover -s tests
```
