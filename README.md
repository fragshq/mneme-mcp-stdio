# mneme-mcp-stdio

A minimal **workbench** MCP server (stdio transport) for trying out Mneme ideas. It runs on Redis, Postgres or plain memory, as chosen by whoever starts it. It isn't production code: there's no auth. It runs single-tenant unless `--tenant` pins it to one tenant (see below). An optional [web dashboard](#dashboard) browses every tenant and edits records.

## Tools

The tools come from Mneme's `tools` package: names, descriptions written for the model, input schemas and handlers. This server only maps them onto MCP, in `newServer` in `main.go`, so other clients of Mneme offer the same tools with the same wording.

| Tool | Does |
|---|---|
| `list_record_types` | lists every record type with its latest version |
| `define_record_type` | defines or redefines a record type (`name`, `schema`, optional `title`, `description`, `id_strategy`, `indexes`); each call creates a new version. Records get UUIDv7 IDs by default, and the tool's text tells the model to set `id_strategy` only when the user asks for a specific ID format |
| `get_record_type` | returns a record type's definition (latest, or a given `version`), its versions, and the fields known across all versions |
| `validate_record_type` | checks a definition without storing it |
| `preview_id` | shows what an ID will look like for a `record_type`, without consuming sequence numbers |
| `insert_record` | inserts `data` into `record_type`, with optional `labels`; it is validated against the latest schema and gets an ID from the record type's ID strategy |
| `list_records` | pages through records, with an optional `filter` (Mneme filter JSON), `sort`, `limit` and `cursor`; each page returns `limit`, `total`, `hasMore` and `nextCursor` |
| `count_records` | counts a `record_type`'s records, optionally only those matching a `filter` |
| `get_record` | fetches one record by `record_type` and `id` |
| `list_labels` | lists the distinct labels of a `record_type`'s records, each with its record count, optionally only among records matching a `filter` |
| `update_record` | replaces a record entirely, `data` and `labels` (labels left out are removed); the new `data` must fit the latest schema, and the record moves to that version (the ID and `createdAt` stay, `updatedAt` is refreshed) |
| `delete_record` | permanently deletes a record by `record_type` and `id` |
| `run_script` | runs read-only JavaScript (`code`) over the records and returns its result plus any `console.log` lines; see below |

### `run_script`

`run_script` is for questions that one `list_records` filter can't answer: joins across record types, counts and totals, grouping, computed fields, or reshaping the output. It wraps Mneme's `script` package, and its tool description is `script.Guide`, so the model learns the environment from the tool itself. A script looks like this:

```js
const risks = list("risk", {filter: {op: "gte", field: "severity", value: 3}, sort: "-severity"})
return risks.map(r => ({id: r.id, title: r.data.title}))
```

- **Functions:** `list(type, {filter, sort, limit})` returns every match as an array, `count(type, filter)` returns how many records match, `get(type, id)` returns a record or `null`, plus `types()`, `describe(type)` and `console.log`.
- **Read-only.** There is nothing that writes.
- **Limits:** the library defaults, which are 5 seconds, 10,000 records read, 1,000 storage calls and 1 MiB of output.
- **Errors:** a syntax error, an uncaught exception or a limit comes back as a tool error, followed by whatever the script logged.

The server also sends `tools.Instructions` as its MCP server instructions: cross-tool guidance such as checking existing record types before defining one, and when to count, list or script. Clients that support instructions show it to the model when it connects.

Every successful result is a JSON object, and comes back twice: as pretty-printed JSON text, and as the same object in `structuredContent`.

Errors (unknown record type, a record failing the schema, invalid input, and so on) come back as MCP tool errors (`isError: true`) with the reason in the text.

## Run

```sh
go build -o mneme-mcp-stdio .        # mneme-mcp-stdio.exe on Windows
```

### Configuration

Every setting is a command-line flag and an environment variable: the flag name upper-cased, with dashes as underscores, after `MNEME_`. A flag wins over its variable, and the variable over the default. `mneme-mcp-stdio --help` lists them all.

| Flag | Variable | Default |
|---|---|---|
| `--storage` | `MNEME_STORAGE` | `redis` |
| `--redis-addr` | `MNEME_REDIS_ADDR` | `localhost:6379` |
| `--pg-dsn` | `MNEME_PG_DSN` | none; a URL such as `postgres://user:pw@localhost:5432/db?sslmode=disable` |
| `--prefix` | `MNEME_PREFIX` | `mneme-workbench` (Redis key prefix) or `mneme_` (Postgres table prefix, which must be a plain identifier) |
| `--tenant` | `MNEME_TENANT` | none, which means single-tenant; otherwise a tenant name of letters, digits, `_`, `.` or `-` (at most 64) |
| `--max-record-types` | `MNEME_MAX_RECORD_TYPES` | `10` record types per tenant; `0` means unlimited |
| `--max-records-per-type` | `MNEME_MAX_RECORDS_PER_TYPE` | `100` records per record type, per tenant; `0` means unlimited |
| `--dashboard` | `MNEME_DASHBOARD` | `off`; `on` (or a bare `--dashboard`) serves the [dashboard](#dashboard) at `http://127.0.0.1:47080/`, and a `host:port` serves it there |

`--storage` picks the backend:

| `--storage` | Storage | Needs |
|---|---|---|
| `redis` (default) | Redis hashes, one key space | `--redis-addr` |
| `memory` | process memory; everything is lost when the server exits | nothing |
| `postgres-shared` | Postgres, every record type in one table (`<prefix>records`) with a `record_type` column | `--pg-dsn` |
| `postgres-per-type` | Postgres, one table per record type (`<prefix>rec_<type>`), created when the type is first defined | `--pg-dsn` |

### Quotas

Each tenant, and the single-tenant default space, may hold at most `--max-record-types` record types with at most `--max-records-per-type` records each. Past either limit, `define_record_type` and `insert_record` fail with a tool error saying which limit was hit, and the dashboard answers 409. New versions of an existing record type and updates of existing records are always allowed, and deleting records or record types makes room again. A malformed or negative value stops the server at startup rather than silently lifting the cap. The limits are checked just before each write, so many concurrent writers could overshoot them by a few.

### Tenancy

By default the server is single-tenant. Pass `--tenant acme` (or set `MNEME_TENANT=acme`) to turn on the storage plugin's tenancy and run every operation as tenant `acme`:

- **Redis:** keys move under `{<prefix>:acme}`.
- **Postgres:** records carry `tenant = 'acme'`.
- **Memory:** the tenant gets its own in-memory store. Each server process has its own memory anyway, so this only matters for parity with the other backends.

Each tenant, including the single-tenant default, sees only its own record types, records and ID sequences, even on the same database and prefix. So you can register the server several times with different tenants against one database. For example, `claude mcp add mneme-acme -- /path/to/mneme-mcp-stdio --tenant acme ...` and `claude mcp add mneme-globex -- /path/to/mneme-mcp-stdio --tenant globex ...` give two isolated workspaces. `run_script` is scoped the same way.

A server process serves exactly one tenant. Choosing the tenant per request would need an authenticated transport, which this stdio workbench doesn't have.

With `postgres-per-type`, `define_record_type`'s `indexes` become real expression indexes. `postgres-shared`, Redis and memory validate or ignore them but create none, so the shared table doesn't fill up with per-type indexes.

### Local databases

`docker-compose.yml` runs both databases. They're bound to `127.0.0.1` on deliberately unusual ports, so they don't collide with other projects on 5432 or 6379:

| Service | Address | Credentials |
|---|---|---|
| Postgres 17 | `localhost:47432` | user `mneme`, password `mneme`, database `mneme` |
| Redis 7 | `localhost:46379` | none (append-only file on) |

```sh
docker compose up -d --wait   # start, returning once both are healthy
docker compose down           # stop, keep the data
docker compose down -v        # stop and wipe the data
```

Data survives restarts in the named volumes `mneme-workbench_postgres-data` and `mneme-workbench_redis-data`.

The server's built-in `--redis-addr` default is still `localhost:6379`, not the compose port. Always pass the address explicitly, as the examples below do, or the server will connect to whatever else is on 6379.

### Register with Claude Code

Pick one backend:

```sh
# Redis
claude mcp add mneme -- /absolute/path/to/mneme-mcp-stdio \
  --storage redis --redis-addr localhost:46379

# Postgres, every record type in one table
claude mcp add mneme -- /absolute/path/to/mneme-mcp-stdio \
  --storage postgres-shared \
  --pg-dsn 'postgres://mneme:mneme@localhost:47432/mneme?sslmode=disable'

# Postgres, one table per record type
claude mcp add mneme -- /absolute/path/to/mneme-mcp-stdio \
  --storage postgres-per-type \
  --pg-dsn 'postgres://mneme:mneme@localhost:47432/mneme?sslmode=disable'
```

Environment variables work too, for example `claude mcp add mneme -e MNEME_STORAGE=memory -- /absolute/path/to/mneme-mcp-stdio`.

Both Postgres modes can share the compose database if they use different prefixes, for example `--prefix shared_` and `--prefix pertype_`. Their tables then never mix.

### Other clients

For example Claude Desktop: add an entry to `mcpServers`, with the settings in `args`, in `env`, or both.

```json
"mneme": {
  "command": "/absolute/path/to/mneme-mcp-stdio",
  "args": ["--storage", "postgres-shared"],
  "env": {
    "MNEME_PG_DSN": "postgres://mneme:mneme@localhost:47432/mneme?sslmode=disable"
  }
}
```

For Redis, use `"args": ["--storage", "redis", "--redis-addr", "localhost:46379"]` instead. For a throwaway session with no database at all, use `"args": ["--storage", "memory"]`.

### Docker

Each version tag also publishes a container image for `linux/amd64` and `linux/arm64`, so you don't need Go or a downloaded binary:

```
ghcr.io/fragshq/mneme-mcp-stdio:latest     # newest release
ghcr.io/fragshq/mneme-mcp-stdio:0.2.0      # a given version (also 0.2, and 1 from v1 on)
```

If the package is private, log in first with a GitHub token that has `read:packages`: `echo $TOKEN | docker login ghcr.io -u <user> --password-stdin`.

The image's entrypoint is the server itself, so it takes the same flags and `MNEME_*` variables as the binary (pass variables with `-e`). The server speaks MCP over stdio, so the MCP client starts the container itself, the same way it would start the binary:

- **`-i`** keeps stdin open. MCP requests come in on it.
- **`--rm`** removes the container when the client disconnects.
- **No `-t`**. A TTY would mangle the protocol on stdout.

#### With the compose databases

From inside a container, `localhost` is the container itself, not your machine. So the server joins the compose stack's network, `mneme-workbench_default` (declared at the bottom of `docker-compose.yml`), and reaches the databases by service name on their **internal** ports, `redis:6379` and `postgres:5432`. The host ports 46379 and 47432 aren't used here.

```sh
docker compose up -d --wait

# Redis
claude mcp add mneme -- docker run -i --rm --network mneme-workbench_default \
  ghcr.io/fragshq/mneme-mcp-stdio:latest \
  --storage redis --redis-addr redis:6379

# Postgres, every record type in one table
claude mcp add mneme -- docker run -i --rm --network mneme-workbench_default \
  ghcr.io/fragshq/mneme-mcp-stdio:latest \
  --storage postgres-shared \
  --pg-dsn 'postgres://mneme:mneme@postgres:5432/mneme?sslmode=disable'

# Postgres, one table per record type
claude mcp add mneme -- docker run -i --rm --network mneme-workbench_default \
  ghcr.io/fragshq/mneme-mcp-stdio:latest \
  --storage postgres-per-type \
  --pg-dsn 'postgres://mneme:mneme@postgres:5432/mneme?sslmode=disable'

# Memory: no database, no network
claude mcp add mneme -- docker run -i --rm ghcr.io/fragshq/mneme-mcp-stdio:latest --storage memory
```

Everything else works as it does for the binary. Add `--tenant acme` for a tenant, or use `-e MNEME_STORAGE=memory` in place of the flag. The data lands in the same compose volumes, so [Inspect the data](#inspect-the-data) works unchanged, and so does switching between the binary and the image.

For Claude Desktop and other clients, the command is `docker`:

```json
"mneme": {
  "command": "docker",
  "args": [
    "run", "-i", "--rm", "--network", "mneme-workbench_default",
    "ghcr.io/fragshq/mneme-mcp-stdio:latest",
    "--storage", "redis", "--redis-addr", "redis:6379"
  ]
}
```

#### With databases on the host

For a Redis or Postgres that runs on your machine but outside the compose stack, use `host.docker.internal` in place of `localhost`, for example `--redis-addr host.docker.internal:6379`. Docker Desktop (macOS, Windows) resolves that name by itself. On Linux, also pass `--add-host=host.docker.internal:host-gateway` to `docker run`.

#### Dashboard

Inside a container, the dashboard's default address `127.0.0.1:47080` can only be reached from within that container. Make it listen on all of the container's interfaces, and publish the port on your machine's loopback only:

```sh
claude mcp add mneme -- docker run -i --rm --network mneme-workbench_default \
  -p 127.0.0.1:47080:47080 \
  ghcr.io/fragshq/mneme-mcp-stdio:latest \
  --storage redis --redis-addr redis:6379 --dashboard 0.0.0.0:47080
```

Then open `http://127.0.0.1:47080/`. Because the port is published on `127.0.0.1`, nothing beyond your machine can reach it. Two caveats:

- The server logs its warning about a non-loopback address, because from inside the container it is one.
- For the same reason, it skips the host-name check that blocks DNS rebinding.

Publish on `127.0.0.1` only, never a bare `-p 47080:47080`. Only one registration can publish the port at a time.

#### Build the image yourself

The fragshq modules are private, so the build needs a GitHub token that can read them. It goes in as a BuildKit secret and is never stored in the image:

```sh
GH_TOKEN=... docker build --secret id=gh_token,env=GH_TOKEN --build-arg VERSION=dev -t mneme-mcp-stdio .
```

Then use `mneme-mcp-stdio` in place of `ghcr.io/fragshq/mneme-mcp-stdio:latest` in the commands above.

### Dashboard

Pass `--dashboard` (or set `MNEME_DASHBOARD=on`) and the server also serves a web dashboard at `http://127.0.0.1:47080/` (or pass `--dashboard=127.0.0.1:9000` for another port). It's plain HTML, JS and CSS embedded in the binary, so there's nothing else to install or run.

- **Every tenant.** A picker lists the default space and every tenant that holds record types, whichever tenant the MCP server itself is pinned to (it's marked "MCP server"). Redis and Postgres list them with the plugins' `Tenants`. Memory storage lives inside the process, so there the dashboard lists only the tenants this process holds data for.
- **Record types:** every version's definition (title, description, ID strategy, indexes, JSON Schema) and the fields across all versions.
- **Designing record types:** define a new record type, or a new version of one, prefilled from the latest. A visual builder covers the common schemas (fields with a type, required, description, text formats, allowed values, minimum/maximum, arrays, nested objects, and whether an object accepts fields beyond its own, i.e. `additionalProperties: false`); anything beyond it (`oneOf`, `pattern`, an `additionalProperties` schema…) is edited as JSON. **Validate** checks a definition without storing it.
- **Deleting record types:** deletes every version, every record and the ID counters of a record type, after you type its name to confirm. A type defined again under that name starts over from v1, with IDs from 1. Only the dashboard can do this: there's no MCP tool for it.
- **Records:** a sortable table with a column per top-level field, label chips with counts that filter the list, a free-form filter in Mneme filter JSON, and cursor paging.
- **Data entry:** insert, edit and delete records. The form is generated from the latest JSON Schema: text, number, date, e-mail and URL inputs, selects for enums, checkboxes for booleans, nested objects, and arrays with add/remove. Anything it can't render as plain inputs (`oneOf`, `anyOf`, free-form objects) gets a JSON box, and a JSON mode edits the whole record as text. Labels have their own editor, with suggestions. When the server rejects a record, each schema issue appears next to the field it concerns.
- **Same rules as the tools.** Writes go through the Mneme engine: inserts and updates validate against the latest schema version, and an update is a full replace that moves the record to that version.

**Access.** There's no authentication. By default it listens on `127.0.0.1` only, and while bound to loopback it also rejects requests addressed to any other host name, which blocks DNS-rebinding attacks. Writes require a JSON body, so a cross-site form can't post to it. A `host:port` beyond loopback (such as `:9000`) exposes every tenant's data, read and write, to whoever can reach it; the server logs a warning when that happens.

If the port is taken, for example because another registration of this server already runs a dashboard, the server logs it and carries on without one. Enable the dashboard on one registration only: it shows every tenant anyway. Everything it logs goes to stderr, since stdout carries MCP.

### Inspect the data

```sh
docker compose exec postgres psql -U mneme -d mneme -c '\dt'     # tables
docker compose exec postgres psql -U mneme -d mneme -c 'SELECT record_type, id, data FROM mneme_records'
docker compose exec redis redis-cli --scan --pattern '{mneme-workbench}*'
```

## Develop

```
main.go                MCP server: tools, storage, optional dashboard
config.go              settings: cobra flags, each bound by viper to its MNEME_* variable
storage.go             backends from --storage, plus every tenant of them for the dashboard
dashboard.go           --dashboard and the HTTP listener
spec/openapi.yaml      the dashboard API, designed first
internal/webserver/    Echo server: api.gen.go (generated), server.go (handlers),
                       convert.go (edjson converters), ui/ (embedded HTML/JS/CSS)
Dockerfile             the container image (multi-arch, distroless)
.github/workflows/     release.yml: on a v* tag, tests, then release binaries and the GHCR image
```

The dashboard API is design-first: edit `spec/openapi.yaml`, then regenerate `internal/webserver/api.gen.go` with oapi-codegen, which is pinned as a Go tool in `go.mod`:

```sh
go generate ./internal/webserver/
```

Handlers implement the generated `ServerInterface` and copy domain values into the generated DTOs with edjson. The UI is plain ES modules with no build step: `app.js` (views), `form.js` (forms from JSON Schema), `dom.js` (DOM helpers), `style.css`.

The libraries are linked locally with `replace` in `go.mod`: `../mneme`, `../mneme-plugins/mneme-memory-storage`, `../mneme-plugins/mneme-redis-storage` and `../mneme-plugins/mneme-postgres-storage`.

`go test ./...` drives all thirteen tools through a real MCP client session over in-memory transports, once per backend, and checks `--tenant` isolation on each backend. `config_test.go` checks how flags, variables and defaults resolve. It also checks that the dashboard reaches every tenant on each backend, and tests its API (`internal/webserver`) over HTTP. Memory always runs, and so does Redis, on miniredis. The two Postgres backends run only when `MNEME_PG_DSN` is set, each in a fresh schema that's dropped afterwards; otherwise they skip, and `-v` shows which ran. With the compose stack up:

```sh
MNEME_PG_DSN='postgres://mneme:mneme@localhost:47432/mneme?sslmode=disable' go test -v ./...
```
