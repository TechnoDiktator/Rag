# Go API

## Document workspace

The Go API serves the HTML, CSS and JavaScript frontend at `/`. Open
`http://localhost:8080/` after starting the API, or use the port configured by
`HTTP_ADDR`. The page uploads one PDF and automatically polls its status;
you can also look up a previously returned document ID. Hybrid search finds
indexed chunks and a separate action generates answers. For completed documents, the chunk viewer
shows a scrollable table of chunk numbers, text previews and source pages
next to the original PDF. Expand a row to read the full text and any generated
image/table description. Pagination controls are above the table.
Select a page reference to navigate
the PDF; exact chunk highlighting is not available, and page navigation depends
on the browser's PDF viewer. PDFs may instead be opened in a separate tab.
Register and log in, then create or select a project. Members can add existing
users by email; every member has equal access and can add more members. The
JWT-based flow is a local-development prototype, not production security.

The frontend is embedded in the Go binary under `internal/web/static`, so it
does not require a separate server or a particular working directory.

## Upload a PDF

Run the API with these environment variables set:

- `DATABASE_URL` — PostgreSQL connection URL. For the local Compose setup,
  use `postgres://atlasrag:local-dev-postgres-password@localhost:5433/atlasrag?sslmode=disable`
  unless you changed `POSTGRES_HOST_PORT` in `infra/.env`.
- `S3_ENDPOINT` — S3-compatible endpoint, such as `localhost:9000`.
- `S3_ACCESS_KEY` and `S3_SECRET_KEY` — object-storage credentials.
- `S3_BUCKET` — an existing bucket for PDF objects.
- `S3_USE_SSL` — optional boolean, defaults to `false`.
- `KAFKA_BROKERS` — optional comma-separated broker addresses, defaults to
  `localhost:9092`.
- `MAX_UPLOAD_BYTES` — optional maximum PDF size in bytes, defaults to 50 MiB.
- `HTTP_ADDR` — optional listen address, defaults to `:8080`.
- `JWT_PRIVATE_KEY_PATH` — required path to a private Ed25519 PKCS#8 PEM key.
  Generate one with `go run .\cmd\keygen <new-path>` and protect it outside
  the repository. See the [root guide](../../../README.md) for setup.
- `OPENAI_API_KEY` — optional API key for query embeddings and answers. Set it in the
  **Go API process** environment to enable search and answers; the worker's `.env` is not
  automatically read by the host API. Without it, these endpoints return HTTP 503.

The API checks PostgreSQL connectivity and that the configured bucket exists
when it starts. For local PostgreSQL and MinIO setup, see the root guide;
Compose applies the migrations to a new database volume. Create the bucket
manually through the MinIO Console before starting the API.

Send one multipart file using the `file` field:

```text
POST /api/v1/projects/{project_id}/documents
Content-Type: multipart/form-data
```

An optional `X-Correlation-ID` header must be a UUID. If omitted, the API
generates one. Successful uploads return `202 Accepted` with a `document_id`
and `QUEUED` status.

The API verifies the PDF signature, computes SHA-256, and stores the object
under a generated key. It then inserts the document, version, parse job, and
`document.uploaded` v1 outbox event in one PostgreSQL transaction. If database
registration fails, it attempts to delete the uploaded object. Identical
content within the same project returns `409 Conflict`.
If PostgreSQL reports an ambiguous commit result, the API retains the object
because the document transaction may have committed successfully.

Project routes require a bearer JWT and current project membership. The API
checks the document belongs to the project. All members have equal access;
roles, revocation, rate limiting and production hardening are not implemented.

## Outbox publisher and status endpoint

The API polls `outbox_events` and publishes `document.uploaded` events to the
Kafka topic with the same name. Kafka messages are keyed by `document_id`.
After Kafka acknowledges a message, the API marks the outbox row as published.
If publishing succeeds but updating PostgreSQL fails, the event may be sent
again; consumers should deduplicate using `event_id`.

Query ingestion progress with:

```text
GET /api/v1/projects/{project_id}/documents/{document_id}/status
```

The response includes document status/version/page count and the parse job
stage/status/error.

## Viewing chunks and source PDFs

`GET /api/v1/projects/{project_id}/documents/{document_id}/chunks?offset=0` returns up to 20
chunks from the current document version, their source pages, optional
descriptions, and a `has_more` flag. It requires the chunk and embedding
tables from migrations 000004 and 000005, including when indexing is disabled.
`GET /api/v1/projects/{project_id}/documents/{document_id}/pdf?version=1` streams the original
PDF for a specific document version and supports byte ranges. These endpoints
require a bearer token and project membership. Do not expose this prototype to
untrusted clients.

## Hybrid chunk search

`POST /api/v1/projects/{project_id}/search` accepts JSON such as:

```json
{"query":"Which region grew fastest?", "top_k":3, "document_id":"b0b6af7e-2f24-415a-8164-e586977bbf80"}
```

`document_id` is optional (omit it to search all documents in the authorized project); `top_k` defaults
to 3 and must be 1–20. Queries must be nonempty and no longer than 2,000
characters. Each request sends the query to OpenAI using
`text-embedding-3-small` (1536 dimensions) and can incur charges. It does
not send stored chunks to OpenAI. Returned results contain chunk text,
optional descriptions, source pages, document/version identifiers, and the
hybrid score and individual ranks.

The corpus contains only completed documents' current-version chunks indexed
with `text-embedding-3-small`. It uses exact cosine distance in pgvector and
BM25 (`k1=1.2`, `b=0.75`) over lowercased alphanumeric tokens in searchable
text, including image/table descriptions. Each retrieves up to 50 candidates;
weighted reciprocal rank fusion combines them with vector weight 0.7, keyword
weight 0.3, and rank constant 60. BM25 corpus statistics are calculated within
the selected scope. The score is a rank-fusion score, not a similarity
probability. Results are returned even when relevance is weak; no relevance
threshold is implemented.

This is a baseline implementation: cosine and BM25 scan the scoped corpus and
BM25 tokenizes it at query time. There is no vector index or persistent lexical
index and no large-scale performance claim. Evaluate relevance and benchmark
before choosing indexed or approximate search. Search is scoped to the
authorized project before vector and BM25 ranking. There are no rate or budget
limits; keep the API local.

## Grounded answers

`POST /api/v1/projects/{project_id}/answer` accepts the same JSON as search. It retrieves chunks
and sends the question and retrieved chunk text and descriptions to OpenAI
GPT-4o. This adds a billable model call to the query-embedding call; do not
submit private documents or questions without appropriate consent. Each text
and description is capped at 2,000 Unicode characters. The response includes
`answer`, `insufficient_evidence`, and `sources`, whose numbered `id` values
correspond to references such as `[1]`. Source entries retain document,
version, page and ranking information, and indicate `text_truncated` and
`description_truncated`. All retrieved sources are returned, including
uncited ones.

When no chunks are found, the endpoint reports insufficient evidence without
calling GPT-4o. The model is instructed to abstain when evidence is insufficient;
valid numeric citation IDs are checked, but factual support and completeness
are not verified. Weak retrieval can still produce misleading answers, so check
the sources. Provider or invalid-answer failures return an error rather than a
fabricated answer. One live answer was observed; answer quality has not been
systematically evaluated. The API has no rate or budget controls.

Restart/rebuild the Go API to pick up server and embedded UI changes. The
Python worker is not needed to answer questions about already indexed documents;
it is needed for new uploads.
