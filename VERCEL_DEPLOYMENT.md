# Vercel + Neon deployment

The production path is one Vercel Go Function serving the embedded frontend and JSON API under one
origin, with durable state in external PostgreSQL. The function never starts an HTTP listener and
never writes application state to the Vercel filesystem. `api/index.go` exports Vercel's required
`http.HandlerFunc`; `vercel.json` routes API and frontend requests to that single function.

The implementation follows Vercel's current [Go runtime](https://vercel.com/docs/functions/runtimes/go),
[project configuration](https://vercel.com/docs/project-configuration/vercel-json), and
[rewrite](https://vercel.com/docs/routing/rewrites) documentation. Vercel's Go runtime is currently
beta, reads the Go version from `go.mod`, and expects exported handlers under `api/`.

## Architecture

Each browser's random `catalog_session` cookie is the PostgreSQL key. One row contains a version and
the complete session `State` as JSONB: products, runs, checkpoint, counters, changes, issues and
scenario identity. This keeps the assessment model easy to inspect. It is not intended for a very
large production catalog, where normalized tables would be more efficient.

Every batch is calculated outside a database transaction. Commit then opens a short transaction,
locks the session row with `SELECT ... FOR UPDATE`, verifies the version and atomically updates both
the JSONB document and version. A competing function using an older version fails with
`concurrent_update`; it cannot overwrite newer work. The frontend processes at most one supplier
batch per request and requests `/api/continue` while the API returns `more: true`. No goroutine or
open transaction must survive a response.

## Local PostgreSQL development

Go 1.23 and Docker are required. Start an isolated local database:

```sh
docker run -d --name supplier-catalog-postgres \
  -e POSTGRES_PASSWORD=localdev \
  -e POSTGRES_DB=supplier_catalog \
  -p 127.0.0.1:5432:5432 \
  -v supplier-catalog-postgres-data:/var/lib/postgresql/data \
  postgres:16-alpine
```

Set local-only environment variables and apply the explicit migration:

```sh
export DATABASE_URL='postgres://postgres:localdev@127.0.0.1:5432/supplier_catalog?sslmode=disable'
export DATABASE_DIRECT_URL="$DATABASE_URL"
export PUBLIC_ORIGIN='http://localhost:8080'
go run ./cmd/migrate
PORT=8080 go run ./cmd/server
```

Open <http://localhost:8080>. When `DATABASE_URL` is set, `cmd/server` uses PostgreSQL; otherwise it
retains the JSON backend for lightweight local compatibility. The CLI always uses its explicit JSON
file selected with `--db`.

Use a different database for tests—never production:

```sh
docker exec supplier-catalog-postgres createdb -U postgres supplier_catalog_test
export TEST_DATABASE_URL='postgres://postgres:localdev@127.0.0.1:5432/supplier_catalog_test?sslmode=disable'
go test -count=1 -race ./...
```

Integration tests skip only when `TEST_DATABASE_URL` is absent. They create unique session IDs and
do not drop schemas or databases.

## Migrations

Versioned SQL lives in `internal/postgres/migrations`. Migrations are embedded in `cmd/migrate`,
recorded in `catalog_schema_migrations`, and protected by a PostgreSQL advisory transaction lock.
They are never executed automatically by an HTTP request.

For production, run migrations deliberately from a trusted workstation or CI environment using the
direct Neon URL:

```sh
export DATABASE_DIRECT_URL='postgresql://USER:PASSWORD@DIRECT_HOST/DB?sslmode=require'
go run ./cmd/migrate
unset DATABASE_DIRECT_URL
```

Review every new migration before running it. The current migration is additive. Existing local JSON
demo sessions are not imported into PostgreSQL and are not deleted; migrating those disposable demo
files is outside scope.

## Neon setup

1. In Neon, create a project in a region close to the Vercel function region.
2. Keep the production branch for production. Create a separate development/test branch or project;
   never run tests against production.
3. In **Connection Details**, copy the pooled connection string (hostname contains `-pooler`) for
   runtime requests. Store it as `DATABASE_URL`.
4. Copy the direct connection string for explicit migrations. Keep it locally as
   `DATABASE_DIRECT_URL`; the Vercel function does not need it.
5. Ensure both URLs require TLS (`sslmode=require`). Do not commit either value.
6. Run `go run ./cmd/migrate` with the direct production URL before the first deployment.

Neon recommends pooled connections for request-driven/serverless traffic and direct connections for
migrations, dumps and work requiring session state. The Go pool is deliberately bounded to four
connections per warm function instance with zero minimum idle connections and short lifetimes.

## Vercel project setup

1. Push the completed changes to GitHub when ready.
2. In Vercel, select **Add New → Project**, import
   `https://github.com/leduc121/supplier-catalog-sync`, and leave the project root at the repository
   root. Use **Other** / no framework preset; `vercel.json` supplies routing.
3. In **Settings → Environment Variables**, add:

   - `DATABASE_URL`: Neon pooled URL. Use the production branch value for Production and a separate
     Neon development branch value for Preview/Development.
   - `PUBLIC_ORIGIN`: exact canonical production origin, for example
     `https://catalog.example.com`, in the Production environment. For local `vercel dev`, set it to
     the exact local origin.

4. Preview functions deliberately derive their one allowed origin from Vercel's system-provided
   `VERCEL_URL` only when `VERCEL_ENV=preview`. They do not accept arbitrary Origin headers. Configure
   Preview `DATABASE_URL` separately so previews never mutate production.
5. Add the production custom domain in **Settings → Domains** before setting `PUBLIC_ORIGIN` to it.
   Use one canonical hostname when reviewing mutations; another hostname will be rejected by design.
6. Deploy through Vercel's GitHub integration. No deploy workflow is included because this repository
   has no `VERCEL_TOKEN`, `VERCEL_ORG_ID`, or `VERCEL_PROJECT_ID`.

## Post-deployment verification

Replace `DEPLOYED_URL` with the actual tested Vercel URL; do not include a trailing slash in
`PUBLIC_ORIGIN`.

```sh
curl --fail --show-error --silent "https://DEPLOYED_URL/healthz"
curl -I "https://DEPLOYED_URL/"
curl -i -X POST "https://DEPLOYED_URL/api/start" \
  -H 'Origin: https://attacker.example' \
  -H 'Content-Type: application/json' \
  --data '{"scenario":"happy","fail_after":-1}'
```

Health must return `{"status":"ok"}`. The cross-origin request must return HTTP 403. In browser
developer tools, both cookies must be `Secure` and `SameSite=Lax`; `catalog_session` must also be
`HttpOnly`.

Then follow **Using the Web Dashboard** in README.md:

1. Run **Normal catalog** with **After batch 2** and confirm **INCOMPLETE** at `2 / 3`.
2. Refresh or open another request after the previous function invocation has ended. The same browser
   must still show the persisted `2 / 3` checkpoint and three products.
3. Select **Resume update** and confirm **Completed** at `3 / 3` with four products.
4. Repeat the feed and verify no duplicate products; run the other scenarios and inspect Report.

No public deployment has been performed or claimed by this repository change.
