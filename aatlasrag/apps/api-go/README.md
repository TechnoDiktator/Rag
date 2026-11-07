# Go API

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

The API checks PostgreSQL connectivity and that the configured bucket exists
when it starts. For local PostgreSQL and MinIO setup, see `infra/README.md`;
Compose applies the migration to a new database volume and creates the bucket.

Send one multipart file using the `file` field:

```text
POST /api/v1/documents
Content-Type: multipart/form-data
```

An optional `X-Correlation-ID` header must be a UUID. If omitted, the API
generates one. Successful uploads return `202 Accepted` with a `document_id`
and `QUEUED` status.

The API verifies the PDF signature, computes SHA-256, and stores the object
under a generated key. It then inserts the document, version, parse job, and
`document.uploaded` v1 outbox event in one PostgreSQL transaction. If database
registration fails, it attempts to delete the uploaded object. Identical
content in the shared (non-tenant) namespace returns `409 Conflict`.
If PostgreSQL reports an ambiguous commit result, the API retains the object
because the document transaction may have committed successfully.

This initial endpoint has no authentication or tenant assignment; add those
before exposing it to untrusted clients.

## Outbox publisher and status endpoint

The API polls `outbox_events` and publishes `document.uploaded` events to the
Kafka topic with the same name. Kafka messages are keyed by `document_id`.
After Kafka acknowledges a message, the API marks the outbox row as published.
If publishing succeeds but updating PostgreSQL fails, the event may be sent
again; consumers should deduplicate using `event_id`.

Query ingestion progress with:

```text
GET /api/v1/documents/{document_id}/status
```

The response includes document status/version/page count and the parse job
stage/status/error.
