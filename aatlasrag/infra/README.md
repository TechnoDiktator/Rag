# Local development infrastructure

This Compose setup runs PostgreSQL and builds MinIO Community Edition from
your local source checkout. MinIO provides the S3 API, so an AWS account is
not needed. Create the bucket once through the MinIO Console after startup.

The Compose file expects the MinIO repository at
`<repository-root>/minio-src/minio-src`, beside `aatlasrag`. It uses
`infra/minio/Dockerfile` because the cloned repository's default Dockerfile
expects prebuilt release artifacts and an upstream base image. The custom
Dockerfile compiles the checked-out source with Go 1.24 in a Linux build
container.
The cloned Community Edition repository is marked unmaintained upstream; use
this locally built image for learning/development, not as a production storage
service.

## Start the services

From PowerShell, in this `infra` directory:

```powershell
Copy-Item .env.example .env
docker compose up -d --build
docker compose ps
```

The first MinIO build downloads the Go build image and compiles the source, so
it may take several minutes. On the first start with an empty database volume,
Compose applies the forward migrations from `migrations/`. Initialization
scripts do not run again when the database volume already exists. To add the
new chunk tables to an existing local database that already has migrations
000001 through 000003, run this once from `infra/`:

```powershell
Get-Content ..\migrations\000004_create_document_chunks.up.sql -Raw |
  docker exec -i atlasrag-postgres psql -v ON_ERROR_STOP=1 -U atlasrag -d atlasrag
```

For an older database, first apply any missing migrations in numerical order;
see the migrations README. Initialization scripts only run on a new volume.

The Compose stack also starts Kafka and the Python PDF worker. The Go API runs
on the host and connects to Kafka at `localhost:9092`; the worker uses the
internal Compose hostname `kafka:29092`.

The worker image contains Poppler and Tesseract for the optional Unstructured
extractor; there is no separate Poppler service. To enable it, add
`PDF_EXTRACTOR=unstructured` to `infra/.env`, then run
`docker compose up -d --build python-worker`. Existing documents are not
automatically reprocessed. See the worker README for limitations.

## Optional OpenAI indexing and pgvector

Compose uses `pgvector/pgvector:pg17-trixie`, a PostgreSQL 17 image with
pgvector. The explicit Debian Trixie variant matches the collation library
used by the original local `postgres:17` image in this environment; the
generic `pg17` image uses Debian Bookworm and caused a collation mismatch.
On an existing PostgreSQL 17 volume, initialization scripts do not rerun.
Back up the database before changing the database image, then recreate the
PostgreSQL service using the same volume:

```powershell
docker compose up -d --no-deps postgres
```

After switching, verify that each existing database's recorded collation
version matches its actual version before resuming writes. A matching version
does not repair indexes or data modified while the versions mismatched; review
and rebuild affected objects as appropriate using the backup. Do not simply
refresh the recorded collation version to suppress a warning.

Verify database health and that migrations 000001 through 000004 exist, then
apply 000005 once using the command in the migrations README. Never remove the
volume to perform this upgrade. PostgreSQL upgrades across major versions
require a separate migration process.

To opt in to billable indexing, set `OPENAI_INDEXING_ENABLED=true` and
`OPENAI_API_KEY` in the ignored `.env` file and rebuild/recreate the worker.
Document text, tables and images are sent to OpenAI. Leave the feature
disabled for local-only processing. See the worker README for cost, retry and
privacy limitations.

## Create the MinIO bucket

Open `http://localhost:9001` and sign in with `MINIO_ROOT_USER` and
`MINIO_ROOT_PASSWORD` from `infra/.env` (defaults are `atlasrag-local` and
`local-dev-minio-password`). Create a bucket named `S3_BUCKET` from that file
(default: `atlasrag-documents`). The Go API will fail startup until this bucket
exists.

To stop the services while retaining data:

```powershell
docker compose down
```

To also delete the local database and uploaded PDFs, remove their named volumes:

```powershell
docker compose down --volumes
```

**Warning:** removing volumes permanently deletes the local database and
objects. Do not use that command if you need to keep your data.

The database, MinIO API, and MinIO console ports are bound to `127.0.0.1` and
are intended only for local development. The default credentials are local
development values; do not use them in a deployed environment.
PostgreSQL is exposed on host port `5433` by default to avoid conflicting with
a local PostgreSQL installation on port `5432`. Change `POSTGRES_HOST_PORT` in
`infra/.env` if you want to use another available port.

## Connect the Go API from the host

Set these variables in the shell where you run the API:

```powershell
$env:DATABASE_URL = "postgres://atlasrag:local-dev-postgres-password@localhost:5433/atlasrag?sslmode=disable"
$env:S3_ENDPOINT = "localhost:9000"
$env:S3_ACCESS_KEY = "atlasrag-local"
$env:S3_SECRET_KEY = "local-dev-minio-password"
$env:S3_BUCKET = "atlasrag-documents"
$env:S3_USE_SSL = "false"
```

If you change values in `infra/.env`, use those same values for the API
environment variables. The MinIO console is available at
`http://localhost:9001`.
The Go API defaults to `KAFKA_BROKERS=localhost:9092`; set that variable if
you changed Kafka's published host port.

The Go API currently runs on the host. Therefore `S3_ENDPOINT=localhost:9000`
is correct. If you later add the API as a Compose service, change its endpoint
to `minio:9000`, which is the MinIO service name on the internal Compose
network; do not use `localhost` from inside that API container.

To run the API from a second PowerShell window, set the API variables above,
then run:

```powershell
# Use this if the default :8080 port is already in use.
$env:HTTP_ADDR = ":18080"
Set-Location ..\apps\api-go
go run .\cmd\api
```

The `.env` file is git-ignored; keep any changed local credentials there and
never commit real passwords.
