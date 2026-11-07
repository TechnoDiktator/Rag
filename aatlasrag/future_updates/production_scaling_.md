# AtlasRAG --- Distributed Multimodal Document Intelligence Platform

A production-style, event-driven Retrieval-Augmented Generation (RAG)
platform designed to ingest, process, index, and search large PDF
collections. AtlasRAG supports both bulk and continuous ingestion,
multimodal PDF understanding (text, tables, charts, and images), hybrid
retrieval, citation-aware answers, and independently scalable processing
workers.

The design target is a corpus of **one million PDFs**. Capacity figures
in this README are illustrative planning assumptions, not measured
performance claims.

------------------------------------------------------------------------

## Table of Contents

1.  [Project Goals](#1-project-goals)
2.  [High-Level Architecture](#2-high-level-architecture)
3.  [Technology Choices](#3-technology-choices)
4.  [Ingestion Architecture](#4-ingestion-architecture)
5.  [PDF Processing and Multimodal
    Extraction](#5-pdf-processing-and-multimodal-extraction)
6.  [Chunking Strategy](#6-chunking-strategy)
7.  [Kafka Architecture and Event
    Contracts](#7-kafka-architecture-and-event-contracts)
8.  [Worker Architecture](#8-worker-architecture)
9.  [Database Schemas](#9-database-schemas)
10. [Retrieval Architecture](#10-retrieval-architecture)
11. [Scalability Calculations](#11-scalability-calculations)
12. [Deployment Architecture](#12-deployment-architecture)
13. [Observability and Evaluation](#13-observability-and-evaluation)
14. [Security and Multi-Tenancy](#14-security-and-multi-tenancy)
15. [Implementation Roadmap](#15-implementation-roadmap)
16. [Portfolio Engineering
    Deliverables](#16-portfolio-engineering-deliverables)
17. [Recommended Build Order](#17-recommended-build-order)

------------------------------------------------------------------------

## 1. Project Goals

AtlasRAG is intended to demonstrate the engineering of a distributed AI
system, not just a chatbot connected to a vector database.

### Core requirements

-   Ingest PDFs in bulk and continuously.
-   Process digitally generated, scanned, and mixed PDFs.
-   Extract text, headings, tables, charts, images, and layout
    information.
-   Preserve page numbers, bounding boxes, and references to original
    assets.
-   Generate embeddings and maintain searchable indexes.
-   Support semantic and lexical retrieval.
-   Generate answers with document- and page-level citations.
-   Track document versions and ingestion state.
-   Recover from worker failures and duplicate events.
-   Scale ingestion workers independently by workload.
-   Measure ingestion throughput and retrieval quality.

### Architectural principles

1.  Separate ingestion, retrieval, and control-plane responsibilities.
2.  Store original PDFs and extracted assets in object storage.
3.  Use PostgreSQL as the metadata and lifecycle system of record.
4.  Use Kafka to decouple asynchronous processing stages.
5.  Scale parser, vision, embedding, and indexing workers independently.
6.  Make every processing stage retryable and idempotent.
7.  Enforce document permissions before retrieval, not in the LLM.
8.  Evaluate retrieval quality instead of relying on subjective answer
    quality.

------------------------------------------------------------------------

## 2. High-Level Architecture

The system has three logical planes.

### 2.1 Ingestion plane

Accepts PDFs, extracts their contents, enriches multimodal elements,
creates chunks, generates embeddings, and updates search indexes.

### 2.2 Retrieval plane

Accepts user queries, searches dense and lexical indexes, fuses and
reranks candidates, expands context, and generates grounded answers.

### 2.3 Control plane

Tracks document metadata, versions, permissions, processing jobs,
failures, and system health.

### Logical architecture

``` text
                         Clients / Admin UI
                    Upload | Bulk Import | Search
                                |
                                v
                      Go API Gateway (Gin)
              Auth | Upload | Search | Job Status
                         /             \
                        /               \
                       v                 v
              INGESTION PLANE       RETRIEVAL PLANE
              ---------------      ---------------
              Bulk Importer         Query Processing
              Continuous Upload     Dense Retrieval
                    |               BM25 Retrieval
                    v                    |
              PostgreSQL + Outbox        v
                    |               RRF Fusion
                    v               Cross-Encoder Reranker
                  Kafka                  |
                    |                    v
                    v               Context Expansion
             Python Worker Pools         |
             - PDF Parser                v
             - Vision Enrichment       LLM
             - Chunking                  |
             - Embedding                 v
             - Indexing             Answer + Citations
                    |
                    v
          ---------------------------
          | Shared Data Infrastructure |
          |----------------------------|
          | PostgreSQL                 |
          | MinIO / S3                 |
          | Qdrant                     |
          | OpenSearch                 |
          | Redis                      |
          | Kafka                      |
          ---------------------------

             Observability:
       OpenTelemetry | Prometheus | Grafana
                    LangSmith
```

This is a logical view. During development, several services can run on
the same machine through Docker Compose.

------------------------------------------------------------------------

## 3. Technology Choices

  --------------------------------------------------------------------------
  Layer                   Technology                 Responsibility
  ----------------------- -------------------------- -----------------------
  API gateway             Go + Gin                   Upload, search,
                                                     authentication, job
                                                     status

  Internal ML services    Python + FastAPI           Parsing, chunking,
                                                     embedding

  Event streaming         Kafka                      Asynchronous processing
                                                     and decoupling

  Object storage          MinIO / S3                 Original PDFs, page
                                                     images, extracted
                                                     assets

  Metadata database       PostgreSQL                 Documents, versions,
                                                     jobs, outbox

  Vector database         Qdrant                     Dense semantic
                                                     retrieval

  Search engine           OpenSearch                 BM25 keyword retrieval

  Cache                   Redis                      Query cache, metadata
                                                     cache, rate limiting

  PDF parsing             Docling / Unstructured     Layout, tables, text
                                                     extraction

  OCR                     PaddleOCR or Tesseract     Scanned-page extraction

  Text embeddings         OpenAI                     Text vectors
                          `text-embedding-3-small`   

  Visual retrieval        ColPali-family model       Page-level visual
                          (later phase)              search

  Reranking               BGE reranker               Candidate relevance
                                                     ranking

  LLM                     GPT-4o or another          Multimodal enrichment
                          vision-capable model       and answer generation

  Deployment              Docker Compose →           Development and
                          Kubernetes                 scale-out

  Observability           OpenTelemetry, Prometheus, Tracing, metrics,
                          Grafana                    dashboards

  LLM tracing/evaluation  LangSmith                  LLM workflow tracing
                                                     and evaluation
  --------------------------------------------------------------------------

### Why this combination?

-   Go demonstrates backend engineering, concurrency, and service
    design.
-   Python provides the document-processing and ML ecosystem.
-   Kafka demonstrates distributed event-driven architecture.
-   PostgreSQL provides durable state and transactional consistency.
-   Qdrant and OpenSearch provide complementary dense and lexical
    retrieval.

------------------------------------------------------------------------

## 4. Ingestion Architecture

Both bulk and continuous ingestion should use the same downstream
processing pipeline.

### 4.1 Bulk ingestion

For a large corpus, do not upload every PDF through the API one at a
time. Build a separate bulk ingestion utility.

``` text
Bulk Importer
     |
     v
Scan source directory / bucket
     |
     v
Compute SHA-256 content hash
     |
     v
Check for existing document
     |
     v
Store original PDF in MinIO / S3
     |
     v
Register document + job + outbox event
in PostgreSQL transaction
     |
     v
Outbox Publisher
     |
     v
Kafka: document.uploaded
     |
     v
Shared processing pipeline
```

The bulk importer should:

1.  Scan the source directory or object-storage bucket.
2.  Compute a content hash, such as SHA-256.
3.  Check whether the document already exists.
4.  Upload or reference the original PDF in object storage.
5.  Register the document and ingestion job in PostgreSQL.
6.  Publish an ingestion event through the outbox mechanism.

Content hashing helps avoid processing identical files repeatedly.

### 4.2 Continuous ingestion

For new documents arriving through the application:

1.  Client sends a PDF to the Go API.
2.  API validates file type and size.
3.  API stores the PDF in MinIO.
4.  API creates a document record and outbox event in PostgreSQL.
5.  Outbox publisher publishes to Kafka.
6.  Workers process the document asynchronously.
7.  API exposes a job-status endpoint.

Example request:

``` http
POST /api/v1/documents
Content-Type: multipart/form-data
```

Example response:

``` json
{
  "document_id": "uuid",
  "status": "QUEUED"
}
```

Status endpoint:

``` http
GET /api/v1/documents/{document_id}/status
```

Possible states:

``` text
UPLOADED
QUEUED
PARSING
ENRICHING
CHUNKING
EMBEDDING
INDEXING
COMPLETED
FAILED
```

### 4.3 Transactional Outbox Pattern

A common failure scenario:

1.  The API successfully inserts a document into PostgreSQL.
2.  Kafka is temporarily unavailable.
3.  The event is never published.
4.  The document remains registered but is never processed.

The transactional outbox solves this:

-   Insert the document and its outbox event in the same PostgreSQL
    transaction.
-   A separate publisher polls unpublished outbox events.
-   It publishes each event to Kafka.
-   It marks the event as published after successful delivery.

Kafka publishing and PostgreSQL updates are not one distributed
transaction in this design. A publisher may send an event twice if it
crashes after publishing but before marking the event as published.
Consumers must therefore be idempotent.

------------------------------------------------------------------------

## 5. PDF Processing and Multimodal Extraction

A PDF is not simply a text file. It can contain:

-   Digitally generated text.
-   Scanned pages.
-   Tables.
-   Charts and diagrams.
-   Images.
-   Mixed layouts and multi-column content.

The pipeline should preserve relationships between these elements.

### 5.1 PDF processing flow

``` text
PDF Parser Worker
       |
       v
Document / Page Classification
       |
       +----------------+----------------+----------------+
       |                |                |
       v                v                v
   Text PDF        Scanned PDF       Mixed PDF
       |                |                |
       v                v                v
 Native Text           OCR          Page-level Routing
 Extraction             |                |
       +----------------+----------------+
                        |
                        v
             Layout and Element Extraction
                        |
            +-----------+-----------+
            |           |           |
            v           v           v
        Paragraphs    Tables      Images / Charts
            |           |           |
            +-----------+-----------+
                        |
                        v
               Multimodal Enrichment
                        |
                        v
             Structured Document Output
```

### 5.2 Parser choice

Two candidate parsers should be benchmarked:

-   **Docling:** a candidate for layout-aware extraction and table
    handling.
-   **Unstructured:** useful for element-based partitioning and a
    continuation from an existing prototype.

Use PyMuPDF for fast PDF inspection, page metadata, and selective
extraction. Use OCR only where native text extraction is insufficient.

### 5.3 Structured element representation

For example, a PDF page might produce:

``` json
{
  "document_id": "doc-123",
  "page_number": 12,
  "elements": [
    {
      "type": "heading",
      "text": "Revenue Analysis",
      "bbox": [50, 80, 500, 120]
    },
    {
      "type": "paragraph",
      "text": "Revenue increased by 18%...",
      "bbox": [50, 130, 500, 250]
    },
    {
      "type": "table",
      "asset_key": "docs/doc-123/tables/table-1.html",
      "description": "Quarterly revenue by region",
      "bbox": [50, 270, 500, 600]
    },
    {
      "type": "image",
      "asset_key": "docs/doc-123/images/image-1.png",
      "description": "Bar chart showing revenue growth...",
      "bbox": [50, 610, 500, 850]
    }
  ]
}
```

Bounding boxes, page numbers, and asset references make it possible to
trace a retrieved chunk back to its location in the original PDF.

### 5.4 Two levels of multimodal retrieval

#### Level 1: Caption-based multimodal RAG

-   Extract images, charts, and tables.
-   Use a vision-capable LLM to generate descriptions.
-   Embed those descriptions using the text embedding model.
-   Retrieve them through normal text vector search.

This is useful for questions such as:

> Which page contains a chart showing quarterly revenue growth?

However, this is not true image-vector retrieval. The image is
represented by generated text.

#### Level 2: Visual embedding retrieval

Add a model such as ColPali or a compatible visual document retrieval
model.

-   Convert PDF pages into visual representations.
-   Generate visual embeddings or late-interaction representations.
-   Store them in a suitable visual retrieval index.
-   Search visually relevant pages alongside text chunks.

This can help with questions such as:

> Find the page containing a diagram with three interconnected service
> components.

Implement Level 1 first, then add Level 2 as a measured enhancement.

------------------------------------------------------------------------

## 6. Chunking Strategy

Avoid blindly splitting extracted PDF text every 500 characters.

Use hierarchical chunking.

``` text
Document
   |
   v
Parent Chunk — Section
   |
   +--------------------+
   |                    |
   v                    v
Child Chunk 1       Child Chunk 2
Paragraphs          Table + representation
   |
   v
Child Chunk 3
Chart description + nearby explanation
```

Suggested starting configuration:

  Parameter                              Initial value
  ------------------------------ ---------------------
  Child chunk size                          500 tokens
  Child overlap                              50 tokens
  Parent chunk size                1,500--2,000 tokens
  Initial retrieval candidates                 50--100
  Reranked results                               5--10

These are starting points, not universal optimal values. Evaluate them
against the PDF dataset.

Each chunk should preserve:

-   `document_id`
-   `document_version`
-   `chunk_id`
-   `parent_chunk_id`
-   `page_start`
-   `page_end`
-   `chunk_type`
-   `content`
-   `asset_key`, where applicable
-   `embedding_model`
-   `tenant_id`, if supporting multiple tenants

For tables, preserve structured HTML or JSON as well as a text
representation. Table row-column relationships can be lost if a table is
flattened into plain text without structure.

------------------------------------------------------------------------

## 7. Kafka Architecture and Event Contracts

Kafka connects the processing stages.

### 7.1 Topics

  ---------------------------------------------------------------------------------------
  Topic                             Producer          Consumer          Purpose
  --------------------------------- ----------------- ----------------- -----------------
  `document.uploaded`               API / bulk        Parser            New document
                                    importer                            registered

  `document.parse.requested`        Orchestrator      Parser            Start extraction

  `document.parsed`                 Parser            Enrichment        Extraction
                                                                        completed

  `document.enrichment.requested`   Orchestrator      Vision worker     Process images
                                                                        and tables

  `document.enriched`               Vision worker     Chunker           Enrichment
                                                                        completed

  `document.chunked`                Chunker           Embedding worker  Chunks created

  `embedding.requested`             Chunker           Embedding worker  Generate
                                                                        embeddings

  `embedding.completed`             Embedding worker  Indexer           Vectors generated

  `document.indexed`                Indexer           Orchestrator      All indexes
                                                                        updated

  `document.failed`                 Any worker        Orchestrator      Stage failure

  `document.dlq`                    Retry handler     DLQ handler       Exhausted
                                                                        failures
  ---------------------------------------------------------------------------------------

Not every topic is necessary on day one. Some stages can be combined
initially and split as throughput requirements become clearer.

### 7.2 Event format

Use a versioned event envelope:

``` json
{
  "event_id": "uuid",
  "event_type": "document.parse.requested",
  "event_version": 1,
  "occurred_at": "2026-11-06T12:00:00Z",
  "correlation_id": "uuid",
  "document_id": "uuid",
  "document_version": 1,
  "payload": {
    "object_key": "documents/abc/report.pdf",
    "content_hash": "sha256-hash",
    "mime_type": "application/pdf"
  }
}
```

Do not send PDF bytes through Kafka. Store files in MinIO and send
object references.

### 7.3 Partitioning strategy

For document-processing topics, key messages by `document_id`.

This gives ordering for events concerning the same document within a
topic partition.

An initial development configuration:

  Topic group              Initial partitions
  ---------------------- --------------------
  Upload / parse                           12
  Enrichment                                6
  Chunking / embedding                     12
  Indexing                                  6

These are starting values for a test environment, not a calculated
production configuration.

A consumer group cannot actively process more partitions than it has
assigned. For example, 12 partitions allow at most 12 active consumers
in one consumer group at a time.

Ordering is guaranteed only within a partition, not across different
topics. The document state machine must validate that an event is
appropriate for the document's current version and stage.

### 7.4 Reliability model

Use:

-   At-least-once delivery.
-   Idempotent consumers.
-   Retry topics or controlled delayed retries.
-   Dead-letter queues.
-   Consumer lag monitoring.
-   Per-document processing checkpoints.
-   Version-aware event validation.

A deterministic chunk ID can be generated as:

``` text
SHA256(document_id + document_version + chunk_index)
```

This prevents retries from creating duplicate chunks for the same
document version.

A worker should not acknowledge an event before its result is durable
and the next-stage event has been safely handed off. Use an outbox or
equivalent durable handoff.

For example, if the embedding worker successfully generates vectors but
crashes before publishing `embedding.completed`, the system needs a way
to resume without losing completed work.

------------------------------------------------------------------------

## 8. Worker Architecture

Do not create one giant worker that parses PDFs, calls vision models,
generates embeddings, and writes to every database.

Different stages have different resource profiles. Separate them so they
can scale independently.

### 8.1 Worker pools

  -----------------------------------------------------------------------
  Worker                  Responsibilities        Main bottleneck
  ----------------------- ----------------------- -----------------------
  PDF parser              Classify pages, extract CPU, memory, OCR
                          layout, text, and       
                          tables                  

  Vision                  Analyze images, charts, Model API limits or GPU
                          complex tables;         
                          generate descriptions   

  Chunking                Build hierarchical      CPU, memory
                          chunks and attach page  
                          references              

  Embedding               Batch text and visual   API throughput or GPU
                          embedding requests      

  Indexing                Upsert vectors into     Database I/O
                          Qdrant and text into    
                          OpenSearch              
  -----------------------------------------------------------------------

### 8.2 Worker implementation details

**PDF parser**

-   Process one document as a unit initially.
-   Use page-level parallelism only when it helps.
-   Set memory and page-count limits.
-   Send unusually large or corrupted PDFs through a separate handling
    path.

**Vision worker**

-   Do not send every page to the vision model.
-   Send only extracted figures, charts, and complex tables that need
    interpretation.
-   Cache results using an asset hash and model version.
-   Keep the original asset in object storage.

**Embedding worker**

-   Batch multiple chunks per API request where supported.
-   Apply rate limiting and exponential backoff.
-   Store model name and vector dimensions.
-   Make the worker safe to retry.

**Indexer**

-   Use batch upserts.
-   Keep Qdrant and OpenSearch indexing idempotent.
-   Only mark a document `COMPLETED` once all required indexes have
    succeeded.

### 8.3 Worker scaling

  Worker       Scale using
  ------------ -----------------------------
  PDF parser   CPU utilization + queue lag
  Vision       Request backlog + API quota
  Chunker      Queue lag + processing time
  Embedding    Token rate + batch backlog
  Indexer      Queue lag + write latency

Do not scale workers only because Kafka lag is increasing. If Qdrant is
already overloaded, adding indexer replicas may make the problem worse.

------------------------------------------------------------------------

## 9. Database Schemas

Use PostgreSQL as the system of record for document lifecycle and
metadata. Use Qdrant for vector search.

### 9.1 Documents

``` sql
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE documents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    filename TEXT NOT NULL,
    object_key TEXT NOT NULL,
    content_hash VARCHAR(64) NOT NULL,

    mime_type TEXT NOT NULL,
    file_size_bytes BIGINT,
    page_count INTEGER,

    current_version INTEGER NOT NULL DEFAULT 1,
    status VARCHAR(32) NOT NULL DEFAULT 'UPLOADED',

    tenant_id UUID,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (tenant_id, content_hash)
);

CREATE INDEX idx_documents_status
ON documents(status);

CREATE INDEX idx_documents_tenant
ON documents(tenant_id);
```

### 9.2 Document versions

Keep version history. Reprocessing a PDF with a new parser or embedding
model should not silently overwrite the old version.

``` sql
CREATE TABLE document_versions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    document_id UUID NOT NULL REFERENCES documents(id),
    version INTEGER NOT NULL,

    object_key TEXT NOT NULL,
    content_hash VARCHAR(64) NOT NULL,

    parser_version TEXT,
    embedding_model TEXT,
    status VARCHAR(32) NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(document_id, version)
);
```

### 9.3 Pages

``` sql
CREATE TABLE document_pages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    document_id UUID NOT NULL REFERENCES documents(id),
    version INTEGER NOT NULL,
    page_number INTEGER NOT NULL,

    extracted_text TEXT,
    page_asset_key TEXT,
    extraction_method TEXT,

    metadata JSONB DEFAULT '{}'::jsonb,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(document_id, version, page_number)
);
```

### 9.4 Chunks

``` sql
CREATE TABLE document_chunks (
    id UUID PRIMARY KEY,

    document_id UUID NOT NULL REFERENCES documents(id),
    version INTEGER NOT NULL,

    parent_chunk_id UUID,
    chunk_index INTEGER NOT NULL,

    page_start INTEGER,
    page_end INTEGER,

    chunk_type VARCHAR(32) NOT NULL,
    content TEXT NOT NULL,
    token_count INTEGER,

    metadata JSONB DEFAULT '{}'::jsonb,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(document_id, version, chunk_index)
);

CREATE INDEX idx_chunks_document_version
ON document_chunks(document_id, version);

CREATE INDEX idx_chunks_metadata
ON document_chunks USING GIN(metadata);
```

For the initial design, PostgreSQL stores chunk content and metadata
while Qdrant stores the vector and its retrieval payload. Avoid
maintaining duplicate content in several places without a clear reason.

### 9.5 Ingestion jobs

``` sql
CREATE TABLE ingestion_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    document_id UUID NOT NULL REFERENCES documents(id),
    version INTEGER NOT NULL,

    stage VARCHAR(32) NOT NULL,
    status VARCHAR(32) NOT NULL,

    retry_count INTEGER NOT NULL DEFAULT 0,
    error_message TEXT,

    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_jobs_status_stage
ON ingestion_jobs(status, stage);
```

### 9.6 Transactional outbox

``` sql
CREATE TABLE outbox_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    aggregate_id UUID NOT NULL,
    event_type VARCHAR(100) NOT NULL,
    payload JSONB NOT NULL,

    published_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_outbox_unpublished
ON outbox_events(created_at)
WHERE published_at IS NULL;
```

For a portfolio implementation, these six tables are a good starting
point. Add explicit permissions, audit logs, asset records, and deletion
jobs as the system evolves.

### 9.7 Qdrant payload

Each vector point should have a payload resembling:

``` json
{
  "document_id": "uuid",
  "document_version": 1,
  "chunk_id": "uuid",
  "tenant_id": "uuid",
  "page_start": 12,
  "page_end": 13,
  "chunk_type": "table",
  "embedding_model": "text-embedding-3-small",
  "object_key": "documents/doc-123/report.pdf"
}
```

For visual retrieval, use a separate collection or named-vector
configuration designed for the visual model's representation. Do not
assume text and visual embeddings can be compared directly in the same
vector space.

------------------------------------------------------------------------

## 10. Retrieval Architecture

The retrieval pipeline should combine semantic and lexical retrieval.

``` text
                    User Query
                        |
                        v
                 Query Processing
              Normalize / Extract Filters
                        |
               +--------+--------+
               |                 |
               v                 v
            Qdrant            OpenSearch
         Dense Search         BM25 Search
               |                 |
               +--------+--------+
                        |
                        v
                RRF Result Fusion
                        |
                        v
              Cross-Encoder Reranker
                        |
                        v
                 Context Expansion
             Parent / Neighbor / Assets
                        |
                        v
                       LLM
                        |
                        v
                Answer + Citations
```

### 10.1 Why hybrid search?

Consider:

> What is the revenue of product X in FY2025?

Dense retrieval can understand the meaning of the question. BM25 can be
effective for exact terms such as product names, model numbers,
financial identifiers, and technical codes.

Combine them using Reciprocal Rank Fusion (RRF):

\[ RRF(d)=`\sum`{=tex}\_{r `\in `{=tex}R}`\frac{1}{k+rank_r(d)}`{=tex}
\]

Where:

-   \(d\) is a candidate document or chunk.
-   \(R\) is the set of retrieval result lists.
-   \(k\) is a smoothing constant.

Then rerank the best candidates with a cross-encoder.

### 10.2 Retrieval pseudocode

``` python
async def retrieve(query, filters):
    dense_results = await qdrant_search(
        query=query,
        filters=filters,
        limit=50
    )

    keyword_results = await opensearch_search(
        query=query,
        filters=filters,
        limit=50
    )

    candidates = reciprocal_rank_fusion(
        dense_results,
        keyword_results
    )

    reranked = await reranker.rank(
        query=query,
        documents=candidates[:50]
    )

    context = await expand_context(
        reranked[:10]
    )

    return context
```

For queries that specifically ask about visual elements, add a third
retrieval source: visual page search.

### 10.3 Citation-aware generation

The LLM should receive structured evidence, not just text:

``` json
{
  "chunk_id": "chunk-123",
  "document_id": "doc-123",
  "filename": "annual-report.pdf",
  "page_start": 12,
  "page_end": 13,
  "content": "Revenue increased by 18%...",
  "asset_key": null
}
```

The answer should return references such as:

``` text
Revenue increased by 18%.

Source: annual-report.pdf, page 12.
```

Validate that citations refer to actual retrieved evidence. If evidence
is insufficient, the system should say so rather than inventing an
answer.

### 10.4 API endpoints

  Endpoint                              Purpose
  ------------------------------------- ------------------------
  `POST /api/v1/documents`              Upload PDF
  `POST /api/v1/ingestion/bulk`         Start bulk ingestion
  `GET /api/v1/documents/{id}`          Document metadata
  `GET /api/v1/documents/{id}/status`   Processing status
  `DELETE /api/v1/documents/{id}`       Request deletion
  `POST /api/v1/search`                 Search documents
  `POST /api/v1/chat`                   RAG question answering
  `GET /api/v1/jobs/{id}`               Job details

------------------------------------------------------------------------

## 11. Scalability Calculations

Define a baseline workload before choosing machines or replicas.

These are illustrative assumptions, not measured properties of the
eventual corpus.

### 11.1 Corpus assumptions

  Parameter                   Assumption
  ------------------------- ------------
  Total PDFs                   1,000,000
  Average pages per PDF               10
  Total pages                 10,000,000
  Average chunks per page              2
  Total chunks                20,000,000
  Average PDF size                  2 MB
  Total original storage            2 TB
  Embedding dimensions             1,536
  Vector data type               float32

### 11.2 Vector storage

Each vector contains 1,536 float32 values:

\[ 1536 `\times 4`{=tex} = 6144`\text{ bytes}`{=tex} \]

For 20 million vectors:

\[ 20,000,000 `\times 6144`{=tex} =122.88`\text{ GB}`{=tex} \]

  Configuration     Raw vector copies
  --------------- -------------------
  One copy                  122.88 GB
  Two copies                245.76 GB
  Three copies              368.64 GB

These figures exclude ANN index structures, payloads, database overhead,
replicas, backups, and operational headroom. Actual Qdrant memory and
disk requirements must be benchmarked.

### 11.3 Ingestion throughput

Suppose the entire corpus should be ingested within different time
windows.

  Target completion     PDFs/sec   Chunks/sec
  ------------------- ---------- ------------
  30 days                   0.39         7.72
  7 days                    1.65        33.07
  24 hours                 11.57       231.48

For a portfolio project, a seven-day target is a reasonable design
scenario to benchmark. It is not necessary to obtain one million real
PDFs to demonstrate the architecture.

### 11.4 Parser worker concurrency

Assume:

-   Target throughput = 12 PDFs/sec.
-   Average parsing time = 8 seconds.

Using Little's Law:

\[ L=`\lambda `{=tex}W \]

\[ L=12`\times8`{=tex}=96 \]

Approximately 96 concurrent processing slots are needed to sustain the
assumed throughput.

At 70% utilization:

\[ C=`\frac{96}{0.7}`{=tex}`\approx137`{=tex} \]

This is a concurrency estimate, not a count of 137 servers. Benchmark
how many documents one worker can safely process in parallel based on
memory, CPU, and scanned-page ratio.

### 11.5 Embedding throughput

For 20 million chunks over seven days:

\[ `\frac{20,000,000}{7\times24\times3600}`{=tex}
`\approx33.07`{=tex}`\text{ chunks/sec}`{=tex} \]

With a batch size of 100 chunks:

\[
`\frac{33.07}{100}`{=tex}`\approx0.33`{=tex}`\text{ batches/sec}`{=tex}
\]

The average depends on token counts, model rate limits, API latency,
batch limits, and retry rates. Image captions, table descriptions, and
other enriched content may increase the total number of embedding
inputs.

### 11.6 Query throughput

Assume 100 concurrent users, each issuing one query every 10 seconds:

\[ QPS=`\frac{100}{10}`{=tex}=10 \]

An initial benchmark target:

  Metric                            Initial target
  ------------------------------- ----------------
  Sustained query load                      10 QPS
  Burst load                                50 QPS
  Concurrent client connections                100
  Retrieval candidates                     50--100
  Final context chunks                       5--10

These are load-test goals, not performance guarantees. Measure p50, p95,
and p99 latency separately for retrieval and full LLM responses.

### 11.7 Likely bottlenecks

  Component           Likely bottleneck
  ------------------- ---------------------------------
  PDF parser          CPU and memory
  OCR                 CPU/GPU
  Vision enrichment   Model throughput and cost
  Embedding           Token rate and API limits
  Qdrant              RAM, ANN index, disk I/O
  OpenSearch          Heap, disk, indexing throughput
  Kafka               Partitions, consumer lag, disk
  PostgreSQL          Write throughput and indexes
  Object storage      Throughput and request volume

**One million documents does not automatically require one million-way
parallelism.** Size each stage based on its own throughput and
processing time.

------------------------------------------------------------------------

## 12. Deployment Architecture

### 12.1 Stage 1: Docker Compose

Start with a single-machine development environment.

Suggested repository structure:

``` text
atlasrag/
│
├── services/
│   ├── api-gateway/          # Go + Gin
│   ├── ingestion-service/    # Python
│   ├── pdf-parser/
│   ├── vision-worker/
│   ├── chunking-worker/
│   ├── embedding-worker/
│   ├── indexing-worker/
│   └── retrieval-service/
│
├── shared/
│   ├── events/
│   ├── schemas/
│   └── models/
│
├── infrastructure/
│   ├── docker-compose.yml
│   ├── kafka/
│   ├── postgres/
│   ├── qdrant/
│   └── opensearch/
│
├── evaluation/
│   ├── golden_dataset.json
│   └── evaluate.py
│
├── benchmarks/
│   ├── ingestion/
│   └── retrieval/
│
└── docs/
    ├── architecture.md
    └── adr/
```

The first Docker Compose deployment should include:

-   Go API.
-   Python workers.
-   Kafka.
-   PostgreSQL.
-   MinIO.
-   Qdrant.
-   OpenSearch.
-   Redis.
-   Prometheus and Grafana.

Start with two to four worker replicas across the processing pipeline.
Simulate higher throughput later.

### 12.2 Stage 2: Kubernetes

Once the system works locally:

-   Deploy stateless APIs and workers as separate Deployments.
-   Use Horizontal Pod Autoscaler for CPU and memory where appropriate.
-   Use KEDA for Kafka consumer-lag-based scaling.
-   Use persistent storage or managed services for stateful systems.
-   Use separate node pools for CPU-heavy parsing and GPU workloads.
-   Keep secrets in Kubernetes Secrets or an external secrets manager.
-   Use readiness and liveness probes.
-   Apply resource requests and limits.

Do not scale workers just because Kafka lag is increasing. If Qdrant is
already overloaded, adding indexer replicas may make the problem worse.

Scale based on both queue backlog and downstream capacity.

### 12.3 Stage 3: Production-style resilience

Implement:

-   Replicated Kafka brokers.
-   PostgreSQL backups and point-in-time recovery.
-   Qdrant replication and snapshots.
-   OpenSearch snapshots.
-   Object storage versioning and lifecycle rules.
-   Network policies and service authentication.
-   Per-tenant authorization.
-   Rate limits for expensive model calls.
-   Retention and deletion workflows.

For document deletion, remove the PDF and extracted assets, invalidate
cached results, and delete matching records from Qdrant and OpenSearch.
Track deletion as a job so partial failures can be retried.

------------------------------------------------------------------------

## 13. Observability and Evaluation

A portfolio system should demonstrate whether the RAG system is working,
not just that it can answer questions.

### 13.1 Observability

Use OpenTelemetry for distributed traces across:

``` text
Upload API
  → Outbox publisher
  → Kafka
  → Parser worker
  → Enrichment worker
  → Chunking worker
  → Embedding worker
  → Indexing worker
  → Qdrant / OpenSearch
  → Retrieval API
  → LLM
```

Track these metrics:

  Area             Metrics
  ---------------- -------------------------------------------
  Ingestion        PDFs/sec, pages/sec, failures
  Kafka            Consumer lag, retries, DLQ count
  Parsing          Seconds/PDF, OCR ratio, extraction errors
  Embedding        Tokens/sec, batch size, API errors
  Indexing         Upserts/sec, indexing latency
  Retrieval        Recall@K, p95 latency
  LLM              Tokens, latency, cost/query
  Infrastructure   CPU, RAM, disk, network

Grafana dashboards should include ingestion throughput, queue backlog,
worker health, index health, and retrieval performance.

LangSmith can be useful for tracing and evaluating the LLM-related
portion of the pipeline. It complements rather than replaces
infrastructure-level observability.

### 13.2 Retrieval evaluation

Create a golden dataset of questions with known relevant documents,
pages, and chunks.

Example:

``` json
{
  "query": "What was the revenue growth in FY2025?",
  "relevant_documents": ["doc-123"],
  "relevant_pages": [12],
  "relevant_chunks": ["chunk-456"]
}
```

Measure:

  Metric              What it measures
  ------------------- -----------------------------------------------
  Recall@10           Did relevant evidence appear in the top 10?
  Precision@10        How many top 10 results were relevant?
  MRR                 How early was the first relevant result?
  nDCG                Were relevant results ranked well?
  Faithfulness        Is the answer supported by retrieved context?
  Citation accuracy   Do citations point to the correct source?
  Answer relevance    Does the response answer the question?

Run experiments comparing:

1.  Dense retrieval only.
2.  BM25 only.
3.  Hybrid retrieval.
4.  Hybrid + reranking.
5.  Hybrid + reranking + parent context.
6.  Hybrid + visual retrieval.

This provides measurable evidence for architectural decisions.

------------------------------------------------------------------------

## 14. Security and Multi-Tenancy

Even for a portfolio project, include a basic security model.

-   JWT-based API authentication.
-   `tenant_id` on document metadata and retrieval payloads.
-   Tenant-aware filters in both Qdrant and OpenSearch.
-   Authorization checks before returning document content or signed
    object URLs.
-   Encryption in transit and at rest.
-   Audit logs for uploads, searches, and deletions.
-   PDF validation, file-size limits, and malware scanning.
-   Protection against prompt injection inside retrieved documents.

**Never rely on the LLM to enforce document permissions.** Apply
authorization filters before retrieving content.

For document updates, use version-aware indexing. A search should not
accidentally return chunks from an older document version after a newer
version becomes active.

------------------------------------------------------------------------

## 15. Implementation Roadmap

A proposed eight-week development plan:

### Week 1 --- Refactor the existing RAG prototype

-   Convert the notebook into reusable Python modules.
-   Implement PDF classification and extraction.
-   Preserve page numbers and element types.
-   Implement hierarchical chunking.
-   Generate deterministic chunk IDs.

**Deliverable:** Local PDF processing pipeline.

### Week 2 --- Storage and metadata

-   Set up PostgreSQL and MinIO.
-   Implement document upload and content hashing.
-   Create document, version, and job schemas.
-   Implement document status tracking.

**Deliverable:** Persistent document management.

### Week 3 --- Kafka and distributed ingestion

-   Introduce Kafka.
-   Implement the transactional outbox.
-   Build the bulk importer.
-   Build parser and chunking workers.
-   Implement retries, DLQ, and idempotency.

**Deliverable:** Asynchronous ingestion pipeline.

### Week 4 --- Embeddings and indexing

-   Implement batched embedding generation.
-   Add Qdrant.
-   Add OpenSearch.
-   Implement indexing workers.
-   Add document-version-aware indexing.

**Deliverable:** Distributed searchable corpus.

### Week 5 --- Retrieval and RAG

-   Implement the Go retrieval API.
-   Implement dense + BM25 search.
-   Add RRF and reranking.
-   Add parent-context expansion.
-   Generate page-level citations.

**Deliverable:** Hybrid RAG search API.

### Week 6 --- Multimodal enrichment and evaluation

-   Add vision-based chart and image descriptions.
-   Preserve table structure.
-   Add golden evaluation datasets.
-   Measure Recall@K, MRR, and citation accuracy.
-   Add tracing and dashboards.

**Deliverable:** Multimodal RAG with evaluation.

### Week 7 --- Deployment and scaling

-   Build Docker Compose deployment.
-   Add Kubernetes manifests.
-   Add worker autoscaling.
-   Test retries, worker crashes, and broker failures.
-   Implement load tests.

**Deliverable:** Resilient deployment.

### Week 8 --- Benchmarking and documentation

-   Benchmark 10K and 100K documents.
-   Profile parser and indexing throughput.
-   Test retrieval at increasing corpus sizes.
-   Document capacity estimates.
-   Record architectural decisions and trade-offs.

**Deliverable:** Portfolio-ready engineering project.

The eight weeks are a proposed development schedule, not a guarantee of
completing a fully production-hardened million-document deployment.
Demonstrate the architecture at smaller corpus sizes and use load tests
and capacity estimates to explain how it scales.

------------------------------------------------------------------------

## 16. Portfolio Engineering Deliverables

The finished project should demonstrate the following:

  Capability              What to demonstrate
  ----------------------- ---------------------------------------
  Backend engineering     Go APIs, PostgreSQL, authentication
  Distributed systems     Kafka, retries, idempotency, outbox
  AI engineering          PDF understanding, embeddings, vision
  Search engineering      Hybrid retrieval, RRF, reranking
  Data engineering        Bulk ingestion, versioning, pipelines
  Scalability             Worker pools, batching, benchmarks
  Reliability             DLQ, recovery, checkpoints
  MLOps / observability   OpenTelemetry, Grafana, evaluation
  Deployment              Docker, Kubernetes, autoscaling

Publish architecture decision records (ADRs), for example:

-   Why Kafka instead of synchronous processing?
-   Why Qdrant and OpenSearch instead of a single vector database?
-   Why hierarchical chunking?
-   Why use a transactional outbox?
-   Why separate OCR workers from vision workers?
-   Why use caption-based retrieval before adding visual embeddings?

These documents demonstrate an understanding of trade-offs, not just
familiarity with tools.

------------------------------------------------------------------------

## 17. Recommended Build Order

Do not implement every component simultaneously.

### Milestone 1 --- Distributed RAG

Build first:

-   Go API.
-   Python workers.
-   Kafka.
-   PostgreSQL.
-   MinIO.
-   Qdrant.

Support bulk and continuous PDF ingestion, text extraction, chunking,
embeddings, and search.

### Milestone 2 --- Multimodal intelligence

Add:

-   OCR.
-   Table extraction.
-   Chart and image descriptions.
-   Visual asset management.
-   Page-level citations.

### Milestone 3 --- Production-style scale

Add:

-   OpenSearch hybrid retrieval.
-   Reranking.
-   Kubernetes.
-   Autoscaling.
-   Failure recovery.
-   Evaluation and load testing.

### Initial model and database choices

Keep the first version relatively simple:

-   Use `text-embedding-3-small` for text and generated image/table
    descriptions.
-   Use Qdrant for dense vectors.
-   Use OpenSearch for BM25.
-   Use GPT-4o or another vision-capable model for selective enrichment.
-   Add ColPali-style visual retrieval only after the text-and-caption
    pipeline is working and evaluated.

This avoids prematurely building expensive visual embedding
infrastructure before measuring whether it improves retrieval.

------------------------------------------------------------------------

## Final Project Objective

The end result should be more than a multimodal chatbot.

It should be a **distributed document intelligence platform** with:

-   Reliable bulk and continuous ingestion.
-   Event-driven processing.
-   Multimodal PDF understanding.
-   Hybrid retrieval and reranking.
-   Page-level citations.
-   Document versioning and access control.
-   Retryable and idempotent workers.
-   Observability and retrieval evaluation.
-   Documented scalability assumptions and benchmarks.

### First coding milestone

Start with these four pieces, in order:

1.  **PostgreSQL schema and event contracts** --- establish document
    identity, versioning, and processing state.
2.  **Go upload API and bulk importer** --- get PDFs into MinIO and
    register them.
3.  **Kafka + Python parser worker** --- process documents
    asynchronously and persist extracted output.
4.  **Chunking and embedding workers** --- build the searchable index.

The recommended first implementation task is the repository structure,
Docker Compose infrastructure, PostgreSQL migrations, and Kafka event
schemas. Once those contracts are stable, worker implementations can be
developed independently.
