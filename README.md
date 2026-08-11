# Hephaestus

Hephaestus is a single-user LLM and human interaction framework with a Go backend and React frontend.

## Backend configuration

The backend reads environment variables from the process and an optional `.env` file in the working directory. At minimum, configure:

```sh
HEPHAESTUS_POSTGRES_DSN="host=127.0.0.1 user=hephaestus password=... dbname=hephaestus sslmode=disable"
HEPHAESTUS_DEEPSEEK_API_KEY="..."
HEPHAESTUS_FIRECRAWL_API_KEY="fc-..."
```

Run the backend with:

```sh
go run ./cmd/hephaestus
```

## Registry configuration

Identity, impression, tool group, concierge, workflow, and job definitions
load from `HEPHAESTUS_CONFIG_DIR` (default `./config`) first. Persisted
Postgres records then replace a same-named record of the same type as a
complete configuration, rather than a field-by-field overlay. The final merged
registry must pass all reference checks before the process starts.

Database-only management endpoints are available under
`/api/v1/configurations/:kind`, where `:kind` is one of `identities`,
`impressions`, `tool-groups`, `concierges`, `workflows`, or `jobs`:

| Method | Path | Meaning |
| --- | --- | --- |
| `GET` | `/configurations/:kind` | List persisted records only. |
| `POST` | `/configurations/:kind` | Create a persisted record, including a same-named static override. |
| `GET` | `/configurations/:kind/:name` | Get a persisted record. |
| `PUT` | `/configurations/:kind/:name` | Replace a persisted record; path and payload names must match. |
| `DELETE` | `/configurations/:kind/:name` | Delete a persisted record. |

Each write validates the complete static-plus-database registry before its
transaction commits. After commit, the validated registry is atomically made
active for new requests and subsequent chat turns; an in-progress turn keeps
the snapshot it started with. Deleting a database override immediately falls
back to the static definition loaded at process startup.

## File uploads and OCR

Chat messages support up to five attachments. Files are stored below the
session project's `uploads/YYYY-MM-DD` directory. Small, whitelisted text
files are included in the prompt; `jpg`, `jpeg`, `png`, and `bmp` files are
sent to Baidu OCR when both credentials are configured. Other files remain
available to the agent by their project-relative path and size.

| Variable | Default | Description |
| --- | --- | --- |
| `HEPHAESTUS_BAIDU_OCR_API_KEY` | none | Optional Baidu OCR API key; must be set together with the secret. |
| `HEPHAESTUS_BAIDU_OCR_SECRET_KEY` | none | Optional Baidu OCR secret key. |
| `HEPHAESTUS_UPLOAD_TEXT_EXTENSIONS` | `md,markdown,txt,csv,json,yaml,yml,toml,xml` | Comma-separated extensions eligible for direct prompt inclusion. |
| `HEPHAESTUS_UPLOAD_IMAGE_EXTENSIONS` | `jpg,jpeg,png,bmp` | Comma-separated extensions eligible for OCR. |
| `HEPHAESTUS_UPLOAD_INLINE_TEXT_MAX_BYTES` | `10240` | Maximum text-file size included in a prompt. |
| `HEPHAESTUS_UPLOAD_OCR_IMAGE_MAX_BYTES` | `4194304` | Maximum image size sent to OCR. |
| `HEPHAESTUS_UPLOAD_FILE_MAX_BYTES` | `52428800` | Maximum size of one uploaded file. |
| `HEPHAESTUS_UPLOAD_TOTAL_MAX_BYTES` | `262144000` | Maximum aggregate attachment size for one message. |
| `HEPHAESTUS_UPLOAD_MAX_FILES` | `5` | Maximum attachment count for one message. |

When OCR is unavailable or fails, the file is still stored and its path and
size are added to the prompt. The chat view surfaces the extraction warning.

## Web fetch providers

`web_fetch` uses Firecrawl by default and falls back to a local headless browser when Firecrawl fails. The available settings are:

| Variable | Default | Description |
| --- | --- | --- |
| `HEPHAESTUS_WEB_FETCH_PROVIDER` | `firecrawl` | Primary provider: `firecrawl` or `local`. |
| `HEPHAESTUS_FIRECRAWL_API_KEY` | none | Required when the primary provider is `firecrawl`. |
| `HEPHAESTUS_WEB_FETCH_CHROME_PATH` | auto-detected | Optional path to a Chrome or Chromium executable used by the local provider. |
| `HEPHAESTUS_WEB_FETCH_MAX_CHARS` | `16000` | Captured page text cap: passed to the summarizer, or returned truncated when no LLM is configured. |
| `HEPHAESTUS_WEB_FETCH_SUMMARY_MAX_CHARS` | `4000` | When an LLM is available, fetched content over this cap is condensed to a digest within it before reaching the agent. |

When `HEPHAESTUS_DEEPSEEK_API_KEY` is set, `web_fetch` condenses pages that exceed `HEPHAESTUS_WEB_FETCH_SUMMARY_MAX_CHARS` via a direct LLM summarization call (see `internal/transform`), returning at most a digest of that length. Summarization failures degrade to the truncated raw text. `web_search` condenses result lists that exceed `HEPHAESTUS_WEB_SEARCH_SUMMARY_MAX_CHARS` the same way, preserving every result's URL.

The local provider requires Chrome, Chromium, or `headless-shell` to be installed on the host. To run without Firecrawl:

```sh
HEPHAESTUS_WEB_FETCH_PROVIDER=local \
HEPHAESTUS_WEB_FETCH_CHROME_PATH=/usr/bin/chromium \
go run ./cmd/hephaestus
```

The local browser sends all page, redirect, and subresource traffic through a loopback filtering proxy that rejects private and local network destinations. Firecrawl requests send the target URL and fetched page content through Firecrawl's infrastructure; use the local provider when that data must not be shared with a third party.

## Checks

```sh
make test
make vet
make build
```