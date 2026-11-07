# AtlasRAG: local multimodal PDF question answering

AtlasRAG is a **development prototype** that uploads PDFs, extracts and chunks
their contents, optionally indexes text and image/table descriptions, retrieves
relevant chunks with hybrid search, and asks GPT-4o to answer using those
chunks. The Go API also serves the browser interface; a Python worker handles
ingestion. The notebooks in the repository are experiments, not the backend.

> **Local development only.** There is no authentication, tenant isolation,
> rate limiting, spending limit, production deployment configuration, or
> automatic reprocessing. Do not expose the services to untrusted clients.
> Indexing and querying OpenAI are optional, billable, and send data externally.
> Check the citations: valid citation numbers do not guarantee factual support.

## Contents

- [Screenshots](#screenshots)
- [Architecture](#architecture)
- [Upload and ingestion](#upload-and-ingestion)
- [Retrieval and answer generation](#retrieval-and-answer-generation)
- [Database schema](#database-schema)
- [Run locally on Windows](#run-locally-on-windows)
- [Try the complete workflow](#try-the-complete-workflow)
- [API reference](#api-reference)
- [Operations and troubleshooting](#operations-and-troubleshooting)
- [Limitations and further documentation](#limitations-and-further-documentation)

## Screenshots

The images below are examples from the local UI; actual output depends on the
document, the indexed content, and model response.

**Home and upload**

![AtlasRAG home and document workspace](images/Homepage.png)

**Upload, status and chunk workspace**

![Upload and document status panels](images/chunking.png)

**Hybrid retrieval and generated answer**

![Generated answer with source citations](images/hybrid_rerranking_and_retreival.png)

The source panel shows retrieved chunks, descriptions and page links.
PDF page navigation depends on the browser and does not highlight a chunk.

## Architecture

```mermaid
flowchart LR
    Browser["Browser: UI"]
    API["Go API on host: HTTP and outbox publisher"]
    DB[("PostgreSQL and pgvector")]
    Store[("MinIO: PDFs and extracted images")]
    Kafka["Kafka: document.uploaded"]
    Worker["Python worker: parse, chunk, index"]
    OpenAI["OpenAI: embeddings and GPT-4o"]
    Browser -->|"Upload, status, chunks, search, answer"| API
    API -->|"Documents, jobs, outbox, search"| DB
    API -->|"Store and stream PDFs"| Store
    API -->|"Publish outbox events"| Kafka
    Kafka -->|"Consume event"| Worker
    Worker -->|"Read PDF and write images"| Store
    Worker -->|"Pages, chunks, sources, vectors"| DB
    Worker -.->|"Optional descriptions and embeddings"| OpenAI
    API -.->|"Query embedding and answer"| OpenAI
```

| Component | Runs locally | Responsibility |
| --- | --- | --- |
| Browser | Same origin as API | Upload, poll status, inspect chunks, search and display answers. Static files are embedded in Go. |
| Go API | Host process | HTTP endpoints, PDF upload, outbox publishing, retrieval, model calls and PDF streaming. |
| PostgreSQL / pgvector | Docker Compose | Lifecycle, jobs, events, parsed content, chunk sources and vectors. |
| MinIO | Docker Compose | Original PDF objects and extracted image objects; local S3-compatible storage, not AWS. |
| Kafka | Docker Compose | Carries `document.uploaded` events, keyed by document ID. |
| Python worker | Docker Compose | Validates events and PDFs, extracts content, chunks it, optionally indexes it. |
| OpenAI | External service | `text-embedding-3-small` embeddings and `gpt-4o` vision descriptions / answers. |

## Upload and ingestion

```mermaid
sequenceDiagram
    participant B as Browser
    participant A as Go API
    participant M as MinIO
    participant P as PostgreSQL
    participant K as Kafka
    participant W as Python worker
    participant O as OpenAI
    B->>A: POST one PDF
    A->>A: Validate size and PDF signature, calculate SHA-256
    A->>M: Save original PDF object
    A->>P: Transaction: document, version, parse job, outbox event
    A-->>B: 202 with document ID and QUEUED
    A->>K: Publish document.uploaded from outbox
    K-->>A: Acknowledge publish
    A->>P: Mark outbox event published
    K->>W: Deliver event keyed by document ID
    W->>P: Mark parse job RUNNING and document PARSING
    W->>M: Download PDF and verify size and hash
    W->>W: Parse pages, optional elements, and chunks
    opt Unstructured extraction
        W->>M: Save extracted image objects
    end
    opt OpenAI indexing enabled
        W->>O: Describe image or table chunks with GPT-4o
        O-->>W: Description
        W->>O: Embed searchable chunk text
        O-->>W: 1536-dimensional vectors
    end
    W->>P: Transaction: replace outputs and mark COMPLETED
    W->>K: Commit offset after processing
    B->>A: GET document status and chunks
    A->>P: Read current version and results
    A-->>B: Status, chunks and source page references
```

The outbox publisher runs concurrently with HTTP handling, so publication may
occur before the client receives the `202`; the diagram shows the logical
steps, not a strict ordering between those independent activities.

### Processing details

1. **Upload:** `POST /api/v1/documents` accepts one multipart `file`, checks
   the signature and the configured size limit (50 MiB by default), stages
   and hashes it, then writes `documents/<document-id>/v1/source.pdf` to
   MinIO. The document/version, `PARSE` job and event are registered together.
   A duplicate shared-content SHA-256 returns HTTP 409.
2. **Outbox:** the event contains metadata and the object key, not PDF bytes.
   The publisher marks it published after Kafka acknowledgement. A crash
   between publish and marking can produce duplicates; this is not exactly-once
   delivery. The [event example](contracts/events/document.uploaded.v1.example.json)
   and [schema](contracts/events/document.uploaded.v1.schema.json) define v1.
3. **Validation and parsing:** the worker verifies the event, object size and
   hash, then reads pages with `pypdf`. Encrypted or unparseable PDFs fail.
   `pypdf` alone does not perform OCR or extract image/table structure.
4. **Optional multimodal extraction:** `PDF_EXTRACTOR=unstructured` uses
   high-resolution extraction with Poppler and Tesseract in the worker image.
   Extracted element metadata and table HTML are saved in PostgreSQL; image
   bytes are placed in MinIO. Model weights may download on first use.
5. **Chunking:** pypdf splits within each page at up to 3,000 characters.
   Unstructured uses title-based chunking with 3,000 maximum characters,
   2,400 soft limit and 500 combine-under threshold. Sources retain page and
   optional element references; image-only elements can produce empty-text chunks.
6. **Optional indexing:** for each chunk, GPT-4o describes referenced images
   and tables if present. The worker embeds chunk text plus its description
   using `text-embedding-3-small` and stores a 1,536-dimensional vector.
   Image bytes themselves are not embedded. Limitations include 10 images per
   chunk, 10 MiB per image and 30,000 total table-HTML characters per chunk.
7. **Persistence and acknowledgement:** output rows and completion status are
   written transactionally. Offset commit follows processing; invalid PDF
   failures are recorded as `FAILED`, whereas infrastructure/extraction/indexing
   errors can stop the worker and be retried on restart.

`COMPLETED` means the **configured** processing path finished; it does not
guarantee embeddings exist. Indexing is disabled by default, and enabling it
later does not automatically reprocess existing documents.

## Retrieval and answer generation

```mermaid
flowchart TD
    Q["Question, optional document ID, top_k"] --> V["Validate request"]
    V --> E["OpenAI query embedding: text-embedding-3-small"]
    E --> C["Current-version completed chunks with matching embedding model"]
    C --> S["Exact pgvector cosine ranking: top 50"]
    C --> B["BM25 keyword ranking: top 50"]
    S --> R["Weighted reciprocal rank fusion"]
    B --> R
    R --> T["Return top_k ranked chunks"]
    T --> Choice{"Requested action"}
    Choice -->|"Search"| Search["Return chunks and metadata"]
    Choice -->|"Answer, no chunks"| Empty["Return insufficient evidence without GPT-4o call"]
    Choice -->|"Answer, chunks found"| Limit["Limit each text and description to 2000 Unicode characters"]
    Limit --> Model["GPT-4o: question and numbered excerpts"]
    Model --> Check["Validate JSON and numeric citation IDs"]
    Check --> Result["Answer, insufficient-evidence flag and retrieved sources"]
```

**Search.** Each query is embedded by OpenAI. PostgreSQL ranks the scoped
corpus by exact cosine distance and separately by BM25 over lowercased
alphanumeric tokens in `searchable_text`, including image/table descriptions.
BM25 uses `k1=1.2`, `b=0.75`, with corpus statistics computed within the
selected document scope. Weighted reciprocal rank fusion combines up to 50
candidates from each ranking:

```text
score = 0.7 / (60 + vector_rank) + 0.3 / (60 + keyword_rank)
```

A missing rank contributes zero. The score is a rank-fusion score, **not** a
probability. There is no calibrated relevance threshold: even weak matches may
be returned. This implementation scans the corpus and tokenizes at query time;
there is no HNSW/ANN or persistent lexical search index.

**Answer.** The API sends the question and bounded text/description excerpts
to GPT-4o and asks it to treat sources as untrusted data, cite numbered sources
and report insufficient evidence when appropriate. The API checks response
shape, nonempty answer and numeric citation IDs; a sufficient answer must have
at least one valid citation. It cannot verify that claims are supported or that
every claim has a citation. The response includes all retrieved sources, not
only cited ones, and flags truncated text and descriptions. Provider errors or
invalid answers return an error. For an empty corpus, the query embedding is
still requested, but the GPT-4o answer call is skipped.

## Database schema

The authoritative definitions are the [SQL migrations](migrations/). The
diagram shows declared foreign-key relationships; `outbox_events.aggregate_id`
identifies the document in the event but is **not** a database foreign key.

```mermaid
erDiagram
    DOCUMENTS ||--o{ DOCUMENT_VERSIONS : has
    DOCUMENT_VERSIONS ||--o{ INGESTION_JOBS : has
    DOCUMENT_VERSIONS ||--o{ DOCUMENT_PAGES : contains
    DOCUMENT_VERSIONS ||--o{ DOCUMENT_ELEMENTS : contains
    DOCUMENT_VERSIONS ||--o{ DOCUMENT_CHUNKS : contains
    DOCUMENT_CHUNKS ||--o{ DOCUMENT_CHUNK_SOURCES : cites
    DOCUMENT_PAGES ||--o{ DOCUMENT_CHUNK_SOURCES : referenced_by
    DOCUMENT_ELEMENTS o|--o{ DOCUMENT_CHUNK_SOURCES : optionally_referenced_by
    DOCUMENT_CHUNKS ||--o| DOCUMENT_CHUNK_EMBEDDINGS : indexed_as
    DOCUMENTS {
        uuid id PK
        uuid tenant_id
        text filename
        text object_key
        text content_hash
        text mime_type
        bigint file_size_bytes
        integer page_count
        integer current_version
        text status
        timestamptz created_at
        timestamptz updated_at
    }
    DOCUMENT_VERSIONS {
        uuid id PK
        uuid document_id FK
        integer version
        text object_key
        text content_hash
        text parser_version
        text embedding_model
        text status
        timestamptz created_at
    }
    INGESTION_JOBS {
        uuid id PK
        uuid document_id FK
        integer version FK
        text stage
        text status
        integer retry_count
        text error_message
        timestamptz started_at
        timestamptz completed_at
        timestamptz created_at
    }
    DOCUMENT_PAGES {
        uuid id PK
        uuid document_id FK
        integer version FK
        integer page_number
        text extracted_text
        text extraction_method
        timestamptz created_at
    }
    DOCUMENT_ELEMENTS {
        uuid document_id PK,FK
        integer version PK,FK
        integer element_index PK
        integer page_number
        text category
        jsonb content
        text image_object_key
    }
    DOCUMENT_CHUNKS {
        uuid document_id PK,FK
        integer version PK,FK
        integer chunk_index PK
        text content
    }
    DOCUMENT_CHUNK_SOURCES {
        uuid document_id PK,FK
        integer version PK,FK
        integer chunk_index PK,FK
        integer source_index PK
        integer page_number FK
        integer element_index FK
    }
    DOCUMENT_CHUNK_EMBEDDINGS {
        uuid document_id PK,FK
        integer version PK,FK
        integer chunk_index PK,FK
        text embedding_model
        text vision_model
        text description
        text searchable_text
        vector embedding
    }
    OUTBOX_EVENTS {
        uuid id PK
        uuid aggregate_id
        text event_type
        smallint event_version
        jsonb payload
        timestamptz created_at
        timestamptz published_at
    }
```

| Table | Keys, constraints and purpose |
| --- | --- |
| `documents` | UUID PK; optional `tenant_id`; separate unique SHA-256 indexes for shared and tenant content; current version, filename, object key, MIME, size, optional page count, lifecycle status and timestamps. Tenant assignment/isolation is not implemented. |
| `document_versions` | UUID PK; FK to document; unique `(document_id, version)`; PDF key/hash, optional parser/embedding models and status. This composite unique key is referenced by dependent tables. |
| `ingestion_jobs` | UUID PK; composite FK to version; unique `(document_id, version, stage)`; stage, job status, retries, error and timing. Current processing uses a `PARSE` job for the configured path. |
| `outbox_events` | UUID PK; event type/version, document aggregate ID, JSON envelope, timestamps; payload fields are checked against envelope columns. Partial index finds unpublished events. No FK to `documents`. |
| `document_pages` | UUID PK; unique `(document_id, version, page_number)`; extracted text and extraction method; pages are 1-based. |
| `document_elements` | Composite PK `(document_id, version, element_index)`; category, JSON content, page number and optional MinIO image key. Elements are optional and generated by Unstructured. |
| `document_chunks` | Composite PK `(document_id, version, chunk_index)`; text content; chunk indices are 0-based in the database/API. |
| `document_chunk_sources` | Composite PK `(document_id, version, chunk_index, source_index)`; composite FKs to the chunk and page, plus optional FK to an element. Preserves references when a chunk spans sources. |
| `document_chunk_embeddings` | Composite PK/FK to chunk; model names, optional description, nonempty `searchable_text`, and `vector(1536)`. At most one indexed row per chunk. |

Dependent version/chunk/page/element FKs use `ON DELETE CASCADE`. The migration
schema allows several future lifecycle stages; it does not imply that
independent workers for each stage exist. Current ordinary statuses are
`QUEUED`, `PARSING`, `COMPLETED` and `FAILED`.

| Migration | Adds |
| --- | --- |
| `000001` | `pgcrypto`, documents, versions, jobs, outbox and indexes. |
| `000002` | Page text. |
| `000003` | Structured elements and image object keys. |
| `000004` | Chunks and source references. |
| `000005` | `vector` extension and chunk embeddings. |

## Run locally on Windows

### 1. Prerequisites

- Docker Desktop running with Compose, and Go **1.22 or newer** on the host.
- A MinIO Community Edition source checkout at
  `<repo-root>\minio-src\minio-src`; Compose builds it using the local
  [Dockerfile](infra/minio/Dockerfile). The checkout is unmaintained upstream
  and this image is for development only.
- An OpenAI API key only for indexing, search and answer generation. Processing
  with default pypdf and indexing disabled does not need it. Never commit keys.
- Optional: Python 3.10+ and dependencies to run worker tests on the host; Docker
  builds the worker environment. OCR tools are inside the worker image.

If the MinIO checkout is missing, the following creates it at the path Compose
expects, using the source revision used in this local setup:

```powershell
# Run from the repository root; skip this if you already have the checkout.
New-Item -ItemType Directory -Force .\minio-src | Out-Null
git clone https://github.com/minio/minio.git .\minio-src\minio-src
git -C .\minio-src\minio-src switch --detach 7aac2a2c5b7c882e68c1ce017d8256be2feea27f
```

### 2. Configure and start infrastructure

In PowerShell, from the repository root:

```powershell
cd .\aatlasrag\infra
# Only copy on first setup; do not overwrite an existing .env.
if (-not (Test-Path .env)) { Copy-Item .env.example .env }
docker compose up -d --build
docker compose ps
```

The first build may take several minutes. Compose starts PostgreSQL 17 with
pgvector, MinIO, Kafka and the worker. Fresh PostgreSQL volumes automatically
run the five forward migrations; existing volumes do **not** rerun
initialization scripts. For an existing volume, inspect the schema, back up
data if needed, and apply missing migrations **once in order**, following the
[migration guide](migrations/README.md) and
[PostgreSQL image/collation guidance](infra/README.md). Never rerun applied
migrations or destroy volumes merely to upgrade.

Default host addresses: PostgreSQL `localhost:5433`, MinIO API
`localhost:9000`, MinIO Console `http://localhost:9001`, Kafka
`localhost:9092`. The Compose worker uses `postgres:5432`, `minio:9000` and
`kafka:29092` internally.

### 3. Create the storage bucket

Open `http://localhost:9001`, log in with `MINIO_ROOT_USER` and
`MINIO_ROOT_PASSWORD` from `infra/.env`, and create the configured
`S3_BUCKET` (default `atlasrag-documents`) if it does not exist. The Go API
checks the bucket and PostgreSQL on startup. Defaults in `.env.example` are
for local development only.

### 4. Configure optional extraction and indexing

For multimodal indexing, edit the ignored `infra/.env` and set:

```dotenv
PDF_EXTRACTOR=unstructured
OPENAI_INDEXING_ENABLED=true
OPENAI_API_KEY=<your-key>
```

Replace the placeholder privately; never commit the actual key. `pypdf` with
indexing enabled indexes text but does not extract image/table structure.
Enabling indexing sends text, table HTML and images to OpenAI and incurs
charges. Only use documents you are authorized to send. Recreate the worker
after configuration changes:

```powershell
# Run from aatlasrag\infra
docker compose up -d --build python-worker
docker compose logs -f python-worker
```

Stop following logs with Ctrl+C; this does not stop the container. Existing
documents are not automatically reprocessed, so configure indexing **before**
uploading documents you want to search. Repeated retries may incur charges.

### 5. Start the Go API and frontend

Open a second PowerShell window; environment variables are per process.
The following values match the unmodified local Compose defaults; if you
changed `infra/.env`, use matching values. To enable **search and answers**,
also set `OPENAI_API_KEY` in this same API shell before starting the API;
the worker's Compose `.env` is not automatically loaded by the host API.
For example, enter the key privately:

```powershell
$key = Read-Host "OpenAI API key" -AsSecureString
$env:OPENAI_API_KEY = [System.Net.NetworkCredential]::new("", $key).Password
Remove-Variable key
```

Then configure and start the API:

```powershell
$env:DATABASE_URL = "postgres://atlasrag:local-dev-postgres-password@localhost:5433/atlasrag?sslmode=disable"
$env:S3_ENDPOINT = "localhost:9000"
$env:S3_ACCESS_KEY = "atlasrag-local"
$env:S3_SECRET_KEY = "local-dev-minio-password"
$env:S3_BUCKET = "atlasrag-documents"
$env:S3_USE_SSL = "false"
$env:HTTP_ADDR = "127.0.0.1:18080"
cd <repo-root>\aatlasrag\apps\api-go
go run .\cmd\api
```

Replace `<repo-root>` with the actual path. The default Kafka broker is
`localhost:9092`; set `KAFKA_BROKERS` if needed. Keep the process running and open
`http://localhost:18080/`. If `HTTP_ADDR` is omitted, use port `8080`.
No separate UI server is needed. Stop the API with Ctrl+C in its terminal.
Restart it after changing its environment or embedded frontend files.

## Try the complete workflow

1. Enable indexing before upload if you want retrieval; choose Unstructured
   for image/table descriptions. Ensure the worker is running.
2. Open the UI, upload one PDF and save the document ID. Status is polled
   automatically; the same ID can be entered later.
3. Wait for `COMPLETED`. Inspect the chunk table and page references; a
   completed document without embeddings is not searchable.
4. Enter a query and optionally the document ID. Choose **Search chunks** to
   inspect results or **Generate answer** to use GPT-4o on those results.
5. Follow the numbered citations to retrieved source cards and open the
   source PDF. Verify the claims against the PDF, especially for tables,
   images, truncated excerpts and weak matches.

For a quick database inspection using the default local database:

```powershell
$doc = "<document-uuid>"
docker exec atlasrag-postgres psql -U atlasrag -d atlasrag -c "SELECT id, filename, status, page_count FROM documents WHERE id = '$doc'::uuid;"
docker exec atlasrag-postgres psql -U atlasrag -d atlasrag -c "SELECT count(*) AS chunks FROM document_chunks WHERE document_id = '$doc'::uuid;"
docker exec atlasrag-postgres psql -U atlasrag -d atlasrag -c "SELECT count(*) AS embeddings FROM document_chunk_embeddings WHERE document_id = '$doc'::uuid;"
```

Replace the placeholder with your own UUID. Counts are for all versions; the
search endpoint only uses indexed chunks from a completed current version.
An example response from the user interface confirms a live answer was
produced for one Transformer question; it is not a quality evaluation.

## API reference

All endpoints are unauthenticated and should be used locally only. API handler
errors generally return JSON objects with an `error` field. `GET /healthz` reports the API process, not end-to-end
worker, broker or model readiness.

| Method and path | Behavior |
| --- | --- |
| `GET /` | Embedded frontend; assets at `/app.css` and `/app.js`. |
| `GET /healthz` | Process health response. |
| `POST /api/v1/documents` | Multipart field `file`; returns HTTP 202, `document_id` and `QUEUED`. Optional `X-Correlation-ID` must be a UUID. |
| `GET /api/v1/documents/{id}/status` | Current document/version status and parse-job details. |
| `GET /api/v1/documents/{id}/chunks?offset=0` | Current version, up to 20 chunks and `has_more`; offsets range from 0 to 1,000,000. |
| `GET /api/v1/documents/{id}/pdf?version=1` | Original PDF for the requested version; supports byte ranges. |
| `POST /api/v1/search` | Hybrid ranked results with chunk text, descriptions, pages, ranks and scores. |
| `POST /api/v1/answer` | Same search input; returns generated answer, insufficient-evidence flag and numbered retrieved sources. |

Search and answer request (omit `document_id` to search all indexed documents):

```json
{"query":"What is the transformer architecture?", "top_k":3}
```

`query` must contain 1–2,000 Unicode characters after trimming; `top_k`
defaults to 3 and must be 1–20. Optional `document_id` must be a UUID. Example
PowerShell invocation, from a separate terminal while the API is running:

```powershell
$body = @{ query = "What is the transformer architecture?"; top_k = 3 } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri http://localhost:18080/api/v1/answer -ContentType "application/json" -Body $body
```

This makes billable OpenAI calls and sends the query and retrieved excerpts to
OpenAI. A representative response shape is:

```json
{
  "answer": "The answer is supported by this excerpt [1].",
  "insufficient_evidence": false,
  "sources": [
    {
      "id": 1,
      "document_id": "00000000-0000-0000-0000-000000000001",
      "filename": "example.pdf",
      "version": 1,
      "chunk_index": 0,
      "text": "Example evidence.",
      "description": null,
      "pages": [1],
      "score": 0.016,
      "vector_rank": 1,
      "keyword_rank": null,
      "text_truncated": false,
      "description_truncated": false
    }
  ]
}
```

This is illustrative, not an actual model result. Invalid requests return
400, missing API key returns 503, provider failures/invalid model output
return 502, and database failures return 500. See the [API guide](apps/api-go/README.md)
for details.

## Operations and troubleshooting

| Symptom | Check |
| --- | --- |
| UI does not load | Open the port in `HTTP_ADDR` (normally `http://localhost:18080/` in this guide); check that the Go process is still running. A separate temporary test port is not the application. |
| Port already in use | Choose another loopback `HTTP_ADDR` and open that port; do not kill an unknown process. |
| API startup fails | Check PostgreSQL and MinIO container health, connection settings and that the bucket exists. |
| Document stays queued | Check `docker compose ps`, API publisher logs, Kafka and `docker compose logs --tail 100 python-worker`. |
| Document failed or worker restarts | Inspect status/error and worker logs. Invalid PDFs can be marked failed; infrastructure/model errors may be retried and can repeat charges. |
| No search results | Confirm indexing was enabled before upload, document status is completed, embeddings exist for the current version and the filter ID is correct. |
| Search or answer returns 503 | Set `OPENAI_API_KEY` in the Go API process environment and restart that process. The worker key is separate. |
| Search or answer returns 502 | Inspect API logs for provider or invalid-response errors; check credentials, network and provider availability without sharing keys. |
| PDF opens on the wrong page | PDF viewers may ignore page fragments; exact chunk highlighting is not implemented. |

From `aatlasrag\infra`, `docker compose logs -f python-worker` follows worker
logs and `docker compose down` stops the stack while retaining named volumes.
`docker compose down --volumes` **permanently deletes local PostgreSQL, Kafka
and MinIO data**; use only if explicitly intended. The worker is needed for
new ingestion, not for querying already indexed documents. Reprocessing,
orphan cleanup, dead-letter handling and bounded retries remain future work.

## Limitations and further documentation

- This is a local prototype; no production deployment, authentication,
  authorization, tenant isolation, budget control or rate limiting.
- Evaluate retrieval relevance, answer support and extraction/OCR quality on
  representative documents. No throughput or thousands-of-documents scaling
  claim has been established.
- Retries and Kafka redelivery can repeat costly model calls; image objects can
  be orphaned after failure or reprocessing. There is no automatic cleanup.
- Search uses exact cosine and query-time BM25 without vector/lexical indexes;
  performance should be measured before scaling.
- See the [repository overview](../README.md), [Go API](apps/api-go/README.md),
  [worker](apps/workers-python/README.md), [infrastructure](infra/README.md),
  [migrations](migrations/README.md), and [event contract](contracts/events/README.md).
  The longer [design notes](multimodal_README.md) include ideas not necessarily
  implemented in the running system.
