# Supplier Catalog Sync

A command-line tool that keeps a local copy of a supplier's product catalog up to date.
Staff start an update, then see whether it completed, what changed, and which problems
still need attention. Updates survive duplicated or out-of-order data, supplier outages
and application crashes: the existing catalog is never erased, an interrupted update can
be resumed without redoing finished work, and a partial update is never reported as
complete.

Written in Go 1.22 using only the standard library. The supplier is simulated with
fictional JSON files; no network access and no live supplier account are needed.

- **Demo video:** `<paste link here>`
- **Production design (not implemented):** see [Production on AWS](#production-on-aws-not-implemented)

## Contents

1. [Requirements and where they are covered](#requirements-and-where-they-are-covered)
2. [Quick start](#quick-start)
3. [Using the Web Dashboard](#using-the-web-dashboard)
4. [Public deployment](#public-deployment)
5. [Demo scenarios](#demo-scenarios)
5. [Command reference](#command-reference)
6. [How it works](#how-it-works)
7. [What happens when something goes wrong](#what-happens-when-something-goes-wrong)
8. [Project layout](#project-layout)
9. [Assumptions](#assumptions)
10. [Unfinished work and known limitations](#unfinished-work-and-known-limitations)
11. [Production on AWS (not implemented)](#production-on-aws-not-implemented)
12. [AI usage](#ai-usage)

## Requirements and where they are covered

| Requirement from the assessment | How it is handled | Test that proves it |
|---|---|---|
| Staff start an update and see whether it completed, what changed, which problems need attention | `start`, `status`, `report` commands. The report lists counters, each change (before -> after) and a "Needs attention" section with the raw records | `TestHappyPathCounters`, `TestBadRecordsBecomeIssues` |
| New, changed and withdrawn products | Decided per record by `merge.Decide` using the supplier revision | `TestDecide` |
| Repeated information must not create duplicates | Product key is the SKU; same revision and same content is `unchanged` | `TestSameUpdateTwice`, `TestOutOfOrder` |
| Information may arrive out of order | Only the revision matters, never arrival order | `TestOutOfOrder` |
| Importing the same update twice creates no duplicates | Second import: nothing created, changed, withdrawn or relisted | `TestSameUpdateTwice` |
| Older information must not replace newer information | Lower revision is ignored (`stale ignored`) | `TestDecide` (older info), `TestOutOfOrder` |
| Older information must not restore a withdrawn product | Withdrawn products stay as rows (tombstones). An older "active" record has a lower revision, so it is ignored | `TestDecide` (old active cannot resurrect), `TestOutOfOrder` |
| A supplier outage must not erase the existing catalog | Products are never deleted; an outage only marks the run `interrupted` | `TestImmediateOutageKeepsCatalog` |
| An application interruption must not erase the catalog | State is replaced atomically, one batch at a time | `TestCrashCanResume`, `TestCommitSurvivesReopen` |
| Resume after restart without losing completed work | The run stores a cursor (next batch) saved together with each batch | `TestOutageResumeEqualsClean`, `TestCrashCanResume`, `TestCancelledContextCanResume` |
| A partial update is never reported as complete | `completed` is set only after the cursor proves every declared batch was applied | `TestMissingBatchNeverCompleted` |

## Quick start

Requires Go 1.22 or newer. No third-party modules.

```sh
go test -count=1 ./...      # run all tests
go vet ./...
go build -o catalog ./cmd/catalog
go build -o catalog-server ./cmd/server
```

The `--db` flag selects the catalog file (default `catalog.json`) and goes **before** the
command. Each example below uses its own file so the runs do not affect each other.
The catalog file is created on first use and is ignored by git.

```sh
./catalog --db demo.json start --file testdata/happy.json
./catalog --db demo.json status
./catalog --db demo.json report
./catalog --db demo.json products
```

## Using the Web Dashboard

The web server embeds its English-language dashboard and the four fictional feeds in one Go
binary. It serves the UI and JSON API from the same origin. Each browser receives a securely
generated session identifier and a separate catalog under `DATA_DIR`; no request can supply a
filesystem path. Start it with:

```sh
PORT=8080 DATA_DIR=./data go run ./cmd/server
```

Open <http://localhost:8080> after the server starts. On a deployment, open the HTTPS URL configured
by the operator. The navigation has four screens:

- **Catalog** lists persisted products, including retained withdrawn products, and filters by text
  or status.
- **Updates** starts a fictional supplier feed and shows its checkpoint, status and counters.
- **Report** selects a run and shows counters, before/after changes and **Needs attention** issues.
- **Demo guide** summarizes the six assessor workflows and explains restart recovery.

To run a normal update, open **Updates**, choose **Normal catalog** under **Supplier scenario**, leave
**Supplier-outage simulation** at **No simulated outage**, and select **Start update**. The result is
**Completed** at `3 / 3`: four products are created, A1 is updated and A2 is withdrawn. Open
**Catalog** to filter by **Withdrawn**, or open **Report** and select the run to inspect **Changes**.

To prove outage recovery, use a fresh or reset browser session:

1. In **Updates**, choose **Normal catalog** and **After batch 2**, then select **Start update**.
2. Confirm the status is **INCOMPLETE**, the checkpoint is `2 / 3`, and three committed products are
   still visible in **Catalog**.
3. Select **Resume update**. Confirm **Completed** at `3 / 3`; Catalog now contains four products and
   A2 is **withdrawn**.
4. In **Report**, select that run and inspect its counters and before/after **Changes**.

Other demonstrations use **No simulated outage** unless stated otherwise:

- Run **Normal catalog** again: no duplicate products or changes are created; the counters show four
  **Unchanged** and two **Stale ignored** records.
- Run **Repeated and out of order**: B1 remains **withdrawn** at revision 3 and older active records
  do not restore it; B2 keeps its newer price.
- Run **Invalid records**: the run is **Completed with issues**. **Report** shows four **Needs
  attention** entries (three invalid records and one revision conflict), while valid C1 and C4 are
  retained.
- Run **Missing final batch**: it stays **INCOMPLETE** at `2 / 3`; selecting **Resume update** remains
  incomplete because batch 3 is intentionally absent.

Use **Reset this session**, type `RESET`, and select **Reset session** to remove only the current
browser session's catalog, history, changes and issues. Each browser session has a separate catalog.
The session identifier is a cookie; clearing cookies or switching browser profiles can lose access
to the previous demo session even though its JSON file remains on the server.

The UI's **Supplier-outage simulation** returns an error after a safely committed batch; it does not
terminate the Go process. To verify a real application interruption, leave an update **INCOMPLETE**,
restart the application container without removing its data volume, refresh the same browser, and
select **Resume update**. The same session must still show `2 / 3` before it completes.

The file store is protected by one server-wide mutex. Run exactly one server instance for a given
`DATA_DIR`; the assessment has no distributed lock or shared database. Browser sessions and their
catalog JSON files survive server restarts when that directory is persistent.

To verify restart recovery manually, start an outage demo, stop the server, restart it with the same
`DATA_DIR`, then use Resume in the same browser. `TestSeparateProcessExitCanResume` also terminates a
separate process immediately after batch 2 commits, reopens the same file and resumes it.

For a local container check, first provide the domain required by the production-oriented Compose
file and create its deliberately external data volume:

```sh
cp .env.example .env
# Edit DOMAIN and APP_IMAGE in .env.
docker volume create supplier-catalog-data
docker compose up --build -d
docker compose down
```

The image is built in two stages, runs as a non-root user and stores session data in `/data`.
`GET /healthz` returns only `{"status":"ok"}` and reveals no session data.

GitHub Actions verifies formatting, vet, race tests and both binaries. It is CI automation, not a
permanent backend host. GitHub Pages cannot run this Go server, and hosts with temporary filesystems
are unsuitable unless persistent storage is mounted.

## Public deployment

Two deployment options are supported:

### 1. Vercel + Neon PostgreSQL (Serverless)

A single Vercel Go Serverless Function serves the embedded dashboard and JSON API under the same origin, with all durable state persisted in an external PostgreSQL database (such as Neon). Every batch update is committed atomically using optimistic locking and version checks, perfectly matching serverless execution constraints. See **[VERCEL_DEPLOYMENT.md](VERCEL_DEPLOYMENT.md)** for architecture, database migrations, Neon setup, and Vercel deployment instructions.

### 2. Single-host Container (Ubuntu + Caddy)

One Ubuntu server running one `catalog` container behind Caddy. Only Caddy publishes ports 80/443; session data is stored in the external Docker volume `supplier-catalog-data`. See **[DEPLOYMENT.md](DEPLOYMENT.md)** for server preparation, DNS, first deployment, update, rollback, backup, restore and verification commands.

No public URL is recorded because no production credentials or domains have been pre-provisioned in this repository. Exact verification and setup steps are provided in the respective deployment guides.

## Demo scenarios

Prices are stored as integer cents and printed as decimals (5000 cents is shown as 50.00).

### 1. Supplier outage, then resume

```sh
./catalog --db outage.json start --file testdata/happy.json --fail-after 2
./catalog --db outage.json status
./catalog --db outage.json start --file testdata/happy.json     # refused: a run is still open
./catalog --db outage.json resume
```

Expected: the first command stops after batch 2 with `INCOMPLETE - interrupted (2/3 batches)`
and exit code 1. The 3 products from batches 1-2 are kept. The second `start` is refused.
`resume` continues from batch 3 and ends with `COMPLETED (3/3 batches)`.
`--fail-after N` is a demo flag that simulates the supplier going down after N batches.

### 2. Application crash, then resume

```sh
./catalog --db crash.json start --file testdata/happy.json --crash-after 2
./catalog --db crash.json status
./catalog --db crash.json resume
```

Expected: the process exits immediately after batch 2 is saved (exit code 3).
`status` says `INCOMPLETE` (the run is still marked `running`, which on startup means the
previous process died). `resume` finishes with `COMPLETED (3/3 batches)` and the result
equals an uninterrupted run. `--crash-after N` is a demo flag that calls `os.Exit(3)` right
after batch N is committed.

### 3. Same update imported twice

```sh
./catalog --db repeat.json start --file testdata/happy.json
./catalog --db repeat.json start --file testdata/happy.json
./catalog --db repeat.json products
```

Expected: the second run reports 0 new, 0 changed, 0 withdrawn, 0 relisted,
4 unchanged and 2 ignored (older than what is stored). `products` still shows exactly 4 products.

### 4. Repeated and out-of-order data, withdrawn products

```sh
./catalog --db order.json start --file testdata/dup_out_of_order.json
./catalog --db order.json products
```

Expected: 3 products. `B1` is `withdrawn` at revision 3: a withdrawal arrived first and
older "active" records for it arrived later and were ignored. `B2` keeps price 50.00 although
an older, cheaper record arrived after it. Duplicated records are counted as unchanged.

### 5. Bad records

```sh
./catalog --db bad.json start --file testdata/bad_records.json
./catalog --db bad.json report
```

Expected: `COMPLETED WITH ISSUES (2/2 batches)` and exit code 0, with 4 issues
(3 invalid records and 1 revision conflict), each listed with its raw record.
Only the valid products (`C1` and `C4`) are in the catalog, and `C1` keeps its stored name.

### 6. Supplier says 3 batches but delivers 2

```sh
./catalog --db missing.json start --file testdata/missing_batch.json
./catalog --db missing.json status
./catalog --db missing.json resume
```

Expected: `INCOMPLETE - interrupted (2/3 batches)` with exit code 1, with last error
"batch 3 not delivered". Resuming again still reports it as incomplete. It is never shown as completed.

## Command reference

| Command | What it does |
|---|---|
| `start --file FEED.json` | Start a new update. Refused while an earlier run is still open |
| `resume` | Continue the open (running or interrupted) update from its saved cursor |
| `abort` | Give up on the open update. Batches already applied are kept |
| `status` | One-line state of the latest update |
| `report [--run N]` | Counters, list of changes and a "Needs attention" section (default: latest run) |
| `products [--status active\|withdrawn]` | List the local catalog |

Demo-only flags for `start` and `resume`: `--fail-after N` (simulate outage) and
`--crash-after N` (simulate a killed process).

Exit codes: `0` success (including completed with issues); `1` update incomplete, `start`
refused, or another runtime error; `2` bad usage or unknown command.

Run states:

| State | Meaning |
|---|---|
| `running` | In progress, or the process died while running (resume it) |
| `interrupted` | Stopped early; can be resumed |
| `completed` | Every declared batch was applied, no issues |
| `completed_with_issues` | Every declared batch was applied, some records need attention |
| `aborted` | Closed by staff; applied work was kept |

Feed file format (`testdata/*.json`):

```json
{
  "total_batches": 3,
  "batches": [
    {"seq": 1, "records": [
      {"sku": "A1", "name": "T-shirt", "price_cents": 10000, "status": "active", "revision": 1}
    ]}
  ]
}
```

A withdrawn record may omit `name` and `price_cents`. If `total_batches` is missing or 0,
the number of batches in the file is used.

## How it works

### The merge rule

`internal/merge/merge.go` is a pure function that compares the stored product with an
incoming record. Only the supplier-assigned `revision` matters:

| Situation | Decision |
|---|---|
| SKU not stored yet | insert (also for a withdrawal of an unknown SKU) |
| incoming revision is lower | ignore as stale: never overwrites, never resurrects |
| same revision, same content | unchanged (a repeat) |
| same revision, different content | conflict: keep the stored version, record an issue |
| incoming revision is higher | update (withdrawn -> active is counted as a relist) |

Because arrival order is never used, repeated or shuffled deliveries give the same final
catalog. A product is never deleted because it is missing from a feed. Withdrawn products stay
as tombstones, which is what stops old "active" data from bringing them back.

### One batch, one commit

```mermaid
flowchart TD
    A[start] --> B{open run exists?}
    B -- yes --> R[refuse: resume or abort first]
    B -- no --> C[create run: running, cursor = batch 1]
    C --> D[fetch batch at cursor]
    D -- supplier error / missing batch / Ctrl-C --> I[mark interrupted<br/>catalog untouched]
    D -- ok --> E[apply records to a copy of the state<br/>products, changes, issues, counters]
    E --> F[cursor + 1, then ONE atomic commit]
    F --> G{all declared batches applied?}
    G -- no --> D
    G -- yes --> H[completed or completed_with_issues]
```

Everything a batch changes (products, change log, issues, counters **and the cursor**) is
written in a single atomic commit. There is never a saved state where the data was applied
but the cursor did not move, or the other way round. Storage is one JSON file: each commit
writes a temporary file in the same directory, flushes it to disk, then renames it over the
real file. A rename is atomic, so after a crash the file holds either the old state or the
new state, never a half-written one.

If a batch were ever applied twice, the merge rule would turn every record into
"unchanged" or "stale", so no duplicates could appear. Two independent safeguards cover
the same risk.

A run is marked complete only after a final check that the number of applied batches equals
the number the supplier declared. Any other ending is `interrupted` or `running`.

## What happens when something goes wrong

| Event | State on disk | What staff see | Next step |
|---|---|---|---|
| Supplier is down before batch N | Batches before N applied, run `interrupted` | `INCOMPLETE - interrupted`, exit 1 | `resume` when the supplier is back |
| Process killed after a commit | Batches up to that one applied, run still `running` | `INCOMPLETE`, `status` explains | `resume` continues at the next batch |
| Process killed while saving | Old state (the rename never happened) | Same as above, last finished batch | `resume` redoes the unfinished batch |
| Ctrl-C (SIGINT/SIGTERM) | Cancelled between batches, run `interrupted` | `INCOMPLETE - interrupted` | `resume` |
| Supplier delivers fewer batches than declared | Delivered batches applied | `INCOMPLETE`, never COMPLETED | `resume` once the rest arrives |
| Feed total changes between start and resume | Applied batches kept, run `interrupted` | Error naming both totals | `abort` and start again |
| Invalid or conflicting record | Batch continues, record stored as an issue | `COMPLETED WITH ISSUES` | Review "Needs attention" |
| Catalog file is corrupt | File is never overwritten | Clear error, exit 1 | Restore the file from a backup |
| Feed JSON is malformed | No run is created | Clear error, exit 1 | Fix the feed |

## Project layout

```
cmd/catalog/            CLI: main.go (commands), print.go (status and report output)
cmd/server/             HTTP server: configuration, timeouts and graceful shutdown
internal/domain/        plain types: Record, Product, Run, Issue, Change, Validate
internal/merge/         Decide(): the revision rule (pure function) + table tests
internal/store/         one-JSON-file state with atomic Commit + tests
internal/supplier/      Client interface and FileClient (reads a feed, can simulate an outage)
internal/syncer/        the engine: Start, Resume, Abort, batch loop + integration tests
internal/webapp/        session-safe HTTP API, embedded dashboard/feeds + workflow tests
testdata/               fictional feeds: happy, dup_out_of_order, bad_records, missing_batch
```

Tests (23): `TestDecide`; `TestCommitSurvivesReopen`, `TestCloneIndependent`,
`TestCorruptFileNotOverwritten`; twelve tests in `internal/syncer`
(happy path, same update twice, out of order, outage then resume equals a clean run, immediate
outage keeps the catalog, in-process and separate-process crash recovery, missing batch, bad records,
cancelled context and abort); and seven HTTP tests covering outage/resume, session isolation,
restart persistence, concurrent starts, request security, public HTTPS origin handling and invalid
origin configuration. Tests also pass with `-race`.

## Assumptions

- The supplier assigns a revision to every record, increasing per product. Revisions are
  the only definition of "newer".
- Money is integer cents; no currency handling.
- Same revision with different content is a conflict. The stored version is kept and the case is
  recorded for a human instead of guessing which record is right.
- A withdrawn record may omit name and price. When a product is withdrawn, its last known
  name and price are kept.
- A record with a higher revision than a withdrawal is a legitimate relist.
- Only one application process uses a catalog file at a time.
- One web server instance owns a `DATA_DIR`; all mutable operations are serialized in that process.
- Browser sessions are isolated by 256-bit random identifiers. Production config marks the cookies
  `Secure`; both cookies use `SameSite=Lax`, and the session identifier is `HttpOnly`.
- A run found in state `running` at startup means the previous process died, so it can be resumed.
- A malformed feed (invalid JSON) is rejected before any run is created.
- The feed path stays available and still represents the same logical update when a run is resumed.
- If a feed repeats a batch number, the first matching batch is used (deterministic, not rejected,
  because the assessment says information may be repeated).
- Batches are processed in order, one at a time.

## Unfinished work and known limitations

- **Storage:** the whole state is one JSON file read fully into memory, so it will not scale to a
  large catalog. A production version would use PostgreSQL (or SQLite) with one transaction per
  batch; the engine's commit boundary is already shaped for that.
- **Commit durability:** a failure before the rename leaves file and memory unchanged. After the
  rename the new state is the real one; flushing the directory entry is best-effort.
- **Crash testing is simulated:** `--crash-after` exits right after a commit. Safety for a crash in
  the middle of a write relies on atomic rename and was reasoned about, not tested with real fault
  injection or power loss.
- **No locking:** two processes on the same file are not prevented.
- **Single web instance:** the in-process mutex does not coordinate multiple containers. Production
  needs PostgreSQL or another transactional shared store before horizontal scaling.
- **Supplier client:** only a file-based fake. No HTTP client, retry or backoff, and no rate limiting.
- **Issues:** they are tied to the run that produced them. There is no manual resolve, no
  lifecycle across runs, and no cap on how many are stored.
- **Duplicate batch numbers in a feed** are not rejected; the first one wins.
- No scheduler, notifications or authenticated user accounts.
- **Demo sessions are not user accounts:** random session cookies isolate demos, but there is no login,
  access control, expiry cleanup or administrative session management.

## Production on AWS (not implemented)

This is a design sketch, not something that was built or deployed. The tool is a batch
job, and all progress lives in the database, so a container that dies can simply be started
again and resume from the saved cursor. EventBridge would start a Step Functions workflow that runs
the Go worker on ECS Fargate (not Lambda, because a large update can exceed 15 minutes), with retries
on failure. RDS PostgreSQL would replace the JSON file, using one transaction per batch.
Secrets Manager would hold the supplier credentials, CloudWatch would collect logs and metrics,
and SNS would notify staff when an update fails or issues need attention. GitHub Actions would build
the image and push it to ECR.

```mermaid
flowchart LR
    EB[EventBridge Scheduler] --> SF[Step Functions<br/>retry and resume]
    SF --> W[Go sync worker<br/>ECS Fargate]
    W --> DB[(RDS PostgreSQL<br/>catalog, runs, issues)]
    W -->|via NAT Gateway| SUP[Supplier API]
    SM[Secrets Manager] -.-> W
    W -.-> CW[CloudWatch logs and metrics]
    CW --> SNS[SNS alerts]
    GH[GitHub Actions] --> ECR[ECR image registry]
    ECR --> W
```

## AI usage

AI tools were used to help interpret the assessment, draft and review the Go implementation, design
and implement the web dashboard, and generate tests and documentation. The work was checked by
running formatting, vet, race tests, both builds, API workflows and browser inspection. I reviewed
the resulting design decisions and can explain and modify the revision merge, tombstones, per-batch
commit, resume cursor, session isolation and HTTP request protections.
