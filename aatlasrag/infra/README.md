# Local development infrastructure

This Compose setup runs PostgreSQL and MinIO locally. MinIO provides the S3
API, so an AWS account is not needed. It also creates the `atlasrag-documents`
bucket automatically.

## Start the services

From PowerShell, in this `infra` directory:

```powershell
Copy-Item .env.example .env
docker compose up -d
docker compose ps
```

On the first start with an empty database volume, Compose applies the initial
forward migration from `migrations/`. This initialization script does not run
again when the database volume already exists; use a migration tool for later
schema changes. The `create-minio-bucket` setup container waits for MinIO,
creates the configured bucket if necessary, and then exits.

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

## Connect the Go API from the host

Set these variables in the shell where you run the API:

```powershell
$env:DATABASE_URL = "postgres://atlasrag:local-dev-postgres-password@localhost:5432/atlasrag?sslmode=disable"
$env:S3_ENDPOINT = "localhost:9000"
$env:S3_ACCESS_KEY = "atlasrag-local"
$env:S3_SECRET_KEY = "local-dev-minio-password"
$env:S3_BUCKET = "atlasrag-documents"
$env:S3_USE_SSL = "false"
```

If you change values in `infra/.env`, use those same values for the API
environment variables. The MinIO console is available at
`http://localhost:9001`.

To run the API from a second PowerShell window, set the API variables above,
then run:

```powershell
Set-Location ..\apps\api-go
go run .\cmd\api
```

The `.env` file is git-ignored; keep any changed local credentials there and
never commit real passwords.
