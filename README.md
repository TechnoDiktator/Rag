# AtlasRAG

AtlasRAG is an in-progress, event-driven document ingestion backend for
multimodal retrieval-augmented generation (RAG). A Go API accepts PDFs and
records their lifecycle; a Python worker parses and chunks their contents.
Optional OpenAI-based enrichment creates image and table descriptions and
stores text embeddings in PostgreSQL with pgvector.

The repository also contains earlier LangChain/Chroma experiments and a
multimodal notebook. They are prototypes, not the deployed AtlasRAG backend.

> **Current scope:** PDF upload, asynchronous processing, chunk storage and
> optional vector creation are implemented. A lightweight frontend supports
> uploads and status tracking. Search, answer generation and production
> deployment are not yet implemented in AtlasRAG.
> The optional indexing path has been tested with mocked OpenAI calls and an
> isolated pgvector database; a live OpenAI/worker-container run remains
> unverified.

## Architecture

```text
                         Local development

    Client
      |
      | PDF upload / status request
      v
 +---------------------+      source PDFs       +----------------------+
 | Go API              |----------------------->| MinIO                |
 | HTTP + outbox       |                        | PDFs and image assets|
 | publisher           |                        +----------+-----------+
 +----+-----------+----+                                   ^
      |           |                                        |
      | document  | document.uploaded                      | PDF / images
      | and event |                                        |
      v           v                                        |
 +------------+  +-------------------+        +------------+-----------+
 | PostgreSQL |  | Kafka             |------->| Python worker          |
 | + pgvector |  | document.uploaded |        | parse, chunk, index    |
 | metadata,  |  +-------------------+        +------------+-----------+
 | chunks and |       event consumer                       |
 | vectors    |<-------------------------------------------+
 +------------+    pages, elements, chunks and optional vectors
                                                           |
                                                           | optional:
                                                           | content and images
                                                           v
                                                   +------------------+
                                                   | OpenAI API       |
                                                   | vision + vectors |
                                                   +------------------+
```

In the local configuration, PostgreSQL, MinIO, Kafka and the Python worker
run in Docker Compose; the Go API runs on the host. MinIO uses a local Docker
volume and an image built from a separate Community Edition source checkout.
No AWS account is required. The local MinIO source is unmaintained upstream
and is intended for development, not as a hardened production image.

| Component | Responsibility |
| --- | --- |
| Go API | Validates and stores uploads, registers documents, publishes outbox events and serves status requests. |
| PostgreSQL | Stores document versions, parse jobs, events, pages, elements, chunks and source references. |
| pgvector | Stores optional 1,536-dimensional chunk embeddings in PostgreSQL. |
| MinIO | Stores original PDFs and extracted image assets behind an S3-compatible API. |
| Kafka | Carries `document.uploaded` events from the API to the worker. |
| Python worker | Consumes events, verifies and parses PDFs, chunks content and optionally generates descriptions and embeddings. |
| OpenAI, optional | `gpt-4o` describes images/tables; `text-embedding-3-small` embeds searchable text. |

## Document processing flow

```text
Client           Go API             PostgreSQL       Kafka       Python worker
  |                |                    |               |               |
  |-- upload PDF ->|                    |               |               |
  |                |-- store PDF in MinIO               |               |
  |                |-- document + job + outbox event -->|               |
  |<- 202 + ID ----|                    |               |               |
  |                |-- publish outbox event ----------->|               |
  |                |                    |               |-- event ----->|
  |                |                    |               |               |-- read PDF from MinIO
  |                |                    |               |               |-- parse and chunk
  |                |                    |               |               |-- optional OpenAI calls
  |                |                    |<------------- save results ----|
  |                |                    |               |<- commit offset
  |-- get status ->|-- read status ---->|               |               |
  |<- status ------|                    |               |               |
```

1. **Upload:** `POST /api/v1/documents` accepts one multipart PDF in the
   `file` field. The API checks its PDF signature and size, computes SHA-256,
   and stores the original in MinIO.
2. **Register and publish:** the API records the document, version, parse job
   and `document.uploaded` outbox event in one PostgreSQL transaction. A
   publisher in the API sends the event to Kafka; the event carries the object
   key, not PDF bytes. Duplicate shared-content uploads return `409 Conflict`.
3. **Parse:** the worker consumes the event, downloads the object and verifies
   its size and hash. The default `pypdf` mode extracts page text. Optional
   `unstructured` high-resolution mode also extracts structured elements,
   tables and images, with OCR support where applicable.
4. **Chunk:** `pypdf` text is split within page boundaries. Unstructured uses
   title-based chunking with a 3,000-character maximum, 2,400-character soft
   threshold and 500-character combination threshold. Chunk sources preserve
   page and element references; image bytes live in MinIO, not vector
   metadata.
5. **Optional indexing:** when explicitly enabled, image/table chunks are
   described by `gpt-4o`. Chunk text and any description are embedded using
   `text-embedding-3-small` and stored in pgvector. Plain text does not need
   a vision call. The text embedding model does not directly interpret images.
6. **Persist and acknowledge:** pages, elements, chunks, source links and
   optional vectors are written in a database transaction. The worker commits
   the Kafka offset after processing. Status is available through
   `GET /api/v1/documents/{document_id}/status`.

The current `COMPLETED` status means the configured processing path completed.
It does **not** by itself guarantee embeddings exist or that the document is
searchable. With indexing disabled, no OpenAI calls or embeddings are made.

## Data and reliability

```text
documents -> document_versions -> ingestion_jobs / document_pages
                       |          document_elements -> MinIO image keys
                       +--------> document_chunks -> document_chunk_sources
                                       |
                                       +-> document_chunk_embeddings (optional)

outbox_events -> Kafka document.uploaded -> worker
```

- The outbox reduces the risk of losing an event between document
  registration and publication. Kafka delivery can still be duplicated;
  document outputs are replaced transactionally on reprocessing.
- Permanent PDF validation failures are recorded as failed. Infrastructure,
  extraction, indexing or database errors stop the worker without
  acknowledging the message. The current restart behavior can repeatedly
  retry a failing message; a dead-letter queue and bounded retries are not
  implemented.
- Image uploads happen before database persistence. Failed or repeated
  processing may leave unreferenced MinIO objects; automatic cleanup is not
  implemented.
- The database is the source of truth for document and job status. Migrations
  are numbered SQL scripts; existing Docker volumes do not automatically
  apply newly added initialization scripts.

## Local development

### Prerequisites

- Docker Desktop with Compose, Go, and a local checkout of MinIO Community
  Edition at `minio-src/minio-src`. See the exact build requirements in
  [infrastructure setup](aatlasrag/infra/README.md).
- An existing MinIO bucket, created through the local console.
- PostgreSQL with pgvector for migration `000005`. Fresh Compose volumes use
  the configured pgvector image; an existing PostgreSQL volume requires the
  upgrade and migration steps in the infrastructure guide.
- Python 3.12 and the worker dependencies for running Python tests outside
  Docker. The worker image itself includes Poppler and Tesseract; these are
  **not** separate Compose services.

### Start the stack

From PowerShell:

```powershell
cd aatlasrag\infra
# Only copy if you don't already have a .env you want to keep.
Copy-Item .env.example .env
docker compose up -d --build
docker compose ps
```

Create the configured bucket at `http://localhost:9001`. Then configure and
start the Go API on the host using the instructions in the
[Go API guide](aatlasrag/apps/api-go/README.md) and
[infrastructure guide](aatlasrag/infra/README.md). Local defaults expose
PostgreSQL at `localhost:5433`, MinIO at `localhost:9000`, and Kafka at
`localhost:9092`; the worker uses Compose service names internally.
Open the frontend at `http://localhost:8080/` (or use the port configured by
`HTTP_ADDR`, such as `http://localhost:18080/`). It is served by the Go API
and uses the same-origin upload and status endpoints. Save the document ID to
look it up again; the frontend does not maintain a document list.

For existing volumes, apply any missing migrations **once, in order**, as
described in the [migration guide](aatlasrag/migrations/README.md). Back up
data before changing the PostgreSQL image. Do not remove Docker volumes to
apply migrations.

### Extraction and indexing options

| Setting | Default | Effect |
| --- | --- | --- |
| `PDF_EXTRACTOR` | `pypdf` | Use `unstructured` for layout, image/table extraction and OCR. |
| `OPENAI_INDEXING_ENABLED` | `false` | Enable billable image/table descriptions and text embeddings. |
| `OPENAI_API_KEY` | unset | Required only when OpenAI indexing is enabled; keep it in ignored `infra/.env`. |

Set these in `aatlasrag/infra/.env` and rebuild/recreate the worker after
changing configuration. Unstructured model weights may download on first use
and can require significant CPU, memory and time. Previously processed
documents are **not** automatically reprocessed when settings change.

**Privacy and cost:** enabling OpenAI indexing sends chunk text, table HTML
and image content to OpenAI and incurs API charges. The current Kafka
redelivery/restart behavior may repeat chargeable requests; there is no
per-chunk billing cache, budget limit or dead-letter queue. Only enable it for
documents you are authorized to send. See the [worker guide](aatlasrag/apps/workers-python/README.md)
for size limits and error behavior.

### API overview

| Endpoint | Purpose |
| --- | --- |
| `GET /healthz` | API process health response. |
| `POST /api/v1/documents` | Upload one PDF as multipart field `file`; returns `202` and a document ID. |
| `GET /api/v1/documents/{document_id}/status` | Read the document and parse-job status. |

An optional `X-Correlation-ID` upload header must contain a UUID. The API
currently has **no authentication or tenant assignment** and should not be
exposed to untrusted clients as-is.

## Repository layout

```text
Rag/
|-- aatlasrag/
|   |-- apps/api-go/          Go HTTP API, embedded frontend and Kafka publisher
|   |-- apps/workers-python/  PDF extraction, chunking and optional indexing
|   |-- contracts/events/     Versioned Kafka event schema and example
|   |-- infra/                Local Compose setup and MinIO build recipe
|   |-- migrations/           PostgreSQL and pgvector schema migrations
|   `-- docs/adr/             Architecture decision records
|-- multi_modal_rag_notebook/ Multimodal Unstructured / Chroma prototype
`-- rag_practice/             Earlier text-RAG and retrieval experiments
```

The notebook uses its own Chroma-based experimental pipeline; AtlasRAG's
backend stores optional vectors in PostgreSQL instead. The notebook's saved
outputs are examples, not proof that all PDF types work.

## Limitations and next steps

- Build a retrieval endpoint that embeds queries with the **same model** as
  stored vectors, retrieves relevant chunks and returns page/asset citations.
- Add grounded answer generation and evaluate retrieval quality with real
  questions and varied PDFs, including scanned documents.
- Add a reprocessing workflow, authentication, observability, bounded retries,
  a dead-letter queue, and safe cleanup for orphaned objects.
- Test the worker container and live OpenAI integration end to end. Status and
  retry handling currently reflect a local-development ingestion pipeline.
- Design deployment separately. A future Render deployment could self-host
  MinIO with persistent storage, but must address backups, security, broker
  hosting, service credentials and operating costs. No Render deployment
  configuration is implemented here.

## Further reading

- [AtlasRAG application overview](aatlasrag/README.md)
- [Go API](aatlasrag/apps/api-go/README.md)
- [Python worker](aatlasrag/apps/workers-python/README.md)
- [Local infrastructure](aatlasrag/infra/README.md)
- [Database migrations](aatlasrag/migrations/README.md)
- [Event contract](aatlasrag/contracts/events/README.md)
- [Multimodal design notes](aatlasrag/multimodal_README.md)
