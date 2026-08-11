# Hephaestus

Hephaestus is a single-user AI agent framework with a Go API and a React chat
interface. It combines persistent, project-scoped conversations with
configurable agent personas, tools, plugins, and human approval for sensitive
runtime operations.

## What It Does

- Runs multi-turn LLM chat sessions with streaming responses, regeneration,
	continuation, and editable assistant messages.
- Keeps sessions in projects, with project-scoped uploads and file access.
- Selects identities, concierges, impressions, tool groups, and plugins per
	session so one installation can support distinct agent workflows.
- Lets agents search chat history, create and list projects, search the web,
	fetch web pages, and optionally run shell commands.
- Supports local OpenAI-compatible model servers alongside DeepSeek. Model
	routing uses an identity's `preferred_model`; a local model takes precedence
	when it has the same advertised ID as DeepSeek.
- Stores small, approved text attachments in the conversation context and can
	extract text from supported images using Baidu OCR.
- Provides slash commands for session control and runtime selection, including
	`/help`, `/status`, `/list`, `/switch`, `/activate`, `/deactivate`, `/clear`,
	`/new`, `/stop`, and `/interact`.

## Run It

Start the API from the repository root:

```sh
go run ./cmd/hephaestus
```

The API listens at `http://127.0.0.1:9016` by default. Interactive API
documentation is available at `http://127.0.0.1:9016/swagger/index.html`.

For the React interface, start Vite in a separate terminal:

```sh
cd frontend
npm install
npm run dev
```

The development server proxies `/api` requests to the backend.

## Use It

Create a project and a session through the chat interface or the API, then
send a message. The selected concierge supplies the initial session setup;
use slash commands in the message box to inspect or change its runtime
settings. Commands are handled locally and are neither stored in chat history
nor sent to the model.

Use `/list <kind>` to enumerate available `identity`, `impression`,
`toolgroup`, `plugin`, `concierge`, `session`, `job`, `workflow`, and `project`
entries. `/detail <kind> <id>` shows an entry, `/switch` changes the identity
or concierge, and `/activate` or `/deactivate` adjusts impressions, tool
groups, and plugins. When an operation asks for approval, respond with
`/interact approve` or `/interact deny`.

### Web Tools

`web_search` always queries DuckDuckGo and Sogou and can combine optional
Brave, Tavily, SerpAPI, and SearXNG providers. It returns up to seven diverse
results. `web_fetch` retrieves a page with Firecrawl by default and falls back
to a local headless browser when Firecrawl fails. Long search results and page
content are condensed when an LLM is available; otherwise the content is
truncated to the configured limit.

The local browser requires Chrome, Chromium, or `headless-shell`. Its page,
redirect, and subresource requests pass through a loopback filtering proxy
that blocks private and local network destinations. Firecrawl sends the target
URL and fetched content through Firecrawl's infrastructure.

### Files and OCR

Each chat message accepts up to five attachments. Hephaestus stores them in
the session project's `uploads/YYYY-MM-DD` directory. Text files with allowed
extensions and within the configured size limit are included directly in the
prompt. Supported images are sent to Baidu OCR when both OCR credentials are
available. Other files, and files whose OCR fails, remain available by their
project-relative path and size; the chat response reports an extraction
warning when applicable.

### Registry API

Static identity, impression, tool-group, concierge, workflow, and job
definitions load at startup. Persisted records replace a same-named static
definition of the same type as a complete record. Every change is validated
against the complete merged registry before it commits, then becomes active
atomically for new turns. Removing an override restores the static definition.

Manage persisted records at `/api/v1/configurations/:kind`, where `:kind` is
`identities`, `impressions`, `tool-groups`, `concierges`, `workflows`, or
`jobs`:

| Method | Path | Operation |
| --- | --- | --- |
| `GET` | `/configurations/:kind` | List persisted records. |
| `POST` | `/configurations/:kind` | Create a record or a static override. |
| `GET` | `/configurations/:kind/:name` | Read a persisted record. |
| `PUT` | `/configurations/:kind/:name` | Replace a persisted record. |
| `DELETE` | `/configurations/:kind/:name` | Delete a persisted record or override. |

## Environment Reference

The backend reads process environment variables and an optional `.env` file in
the working directory. Required values are marked below; use either a DeepSeek
key or a local model URL. Firecrawl is required only when it is the selected
web-fetch provider.

| Variable | Default | Effect |
| --- | --- | --- |
| `HEPHAESTUS_POSTGRES_DSN` | required | PostgreSQL connection string for sessions, chat history, registry overrides, and runtime data. |
| `HEPHAESTUS_DEEPSEEK_API_KEY` | required unless local model URL is set | Enables DeepSeek models and LLM-based web-content condensation. |
| `HEPHAESTUS_LOCAL_MODEL_URL` | none | Base URL of an OpenAI-compatible local model server; trailing `/` is removed. |
| `HEPHAESTUS_LOCAL_MODEL_API_KEY` | none | Optional API key for the local model server. |
| `HEPHAESTUS_CONFIG_DIR` | `./config` | Directory containing static registry definitions. |
| `HEPHAESTUS_LISTEN_ADDR` | `127.0.0.1:9016` | HTTP server bind address. |
| `HEPHAESTUS_PROJECTS_ROOT` | `./data/projects` | Root directory for named projects and their uploads. Supports `~`. |
| `HEPHAESTUS_PROJECT_ACCESS_OVERRIDE` | `false` | Allows filesystem tools to access paths outside the project and system temporary directory. |
| `HEPHAESTUS_SHELL_ENABLED` | `false` | Enables the shell tool. |
| `HEPHAESTUS_FIXED_PLUGINS` | `session_summary` | Comma-separated plugins that run for every session and cannot be disabled. |
| `HEPHAESTUS_WECOM_WEBHOOK_URL` | none | WeCom webhook that receives warning and error notifications. |
| `HEPHAESTUS_WEB_FETCH_PROVIDER` | `firecrawl` | Primary page-fetch provider: `firecrawl` or `local`. |
| `HEPHAESTUS_FIRECRAWL_API_KEY` | required for `firecrawl` | Firecrawl API key. |
| `HEPHAESTUS_WEB_FETCH_CHROME_PATH` | auto-detected | Chrome or Chromium executable for the local fetch provider. |
| `HEPHAESTUS_WEB_FETCH_MAX_CHARS` | `16000` | Maximum captured page text before summarization or raw truncation. |
| `HEPHAESTUS_WEB_FETCH_SUMMARY_MAX_CHARS` | `4000` | Maximum size of an LLM-generated page digest. |
| `HEPHAESTUS_WEB_SEARCH_BRAVE_API_KEYS` | none | Comma-separated Brave Search API keys. |
| `HEPHAESTUS_WEB_SEARCH_TAVILY_API_KEYS` | none | Comma-separated Tavily API keys. |
| `HEPHAESTUS_WEB_SEARCH_SERPAPI_API_KEYS` | none | Comma-separated SerpAPI keys. |
| `HEPHAESTUS_WEB_SEARCH_SERPAPI_ENGINE` | `google_light` | SerpAPI search engine identifier. |
| `HEPHAESTUS_WEB_SEARCH_SEARXNG_BASE_URL` | none | Base URL for an optional SearXNG provider. |
| `HEPHAESTUS_WEB_SEARCH_SUMMARY_MAX_CHARS` | `4000` | Maximum size of an LLM-generated search-result digest. |
| `HEPHAESTUS_BAIDU_OCR_API_KEY` | none | Baidu OCR API key; must be set with the OCR secret. |
| `HEPHAESTUS_BAIDU_OCR_SECRET_KEY` | none | Baidu OCR secret key; must be set with the OCR API key. |
| `HEPHAESTUS_UPLOAD_TEXT_EXTENSIONS` | `md,markdown,txt,csv,json,yaml,yml,toml,xml` | Comma-separated text extensions eligible for prompt inclusion. |
| `HEPHAESTUS_UPLOAD_IMAGE_EXTENSIONS` | `jpg,jpeg,png,bmp` | Comma-separated image extensions eligible for OCR. |
| `HEPHAESTUS_UPLOAD_INLINE_TEXT_MAX_BYTES` | `10240` | Largest text file included directly in a prompt. |
| `HEPHAESTUS_UPLOAD_OCR_IMAGE_MAX_BYTES` | `4194304` | Largest image sent to OCR. |
| `HEPHAESTUS_UPLOAD_FILE_MAX_BYTES` | `52428800` | Largest individual attachment. |
| `HEPHAESTUS_UPLOAD_TOTAL_MAX_BYTES` | `262144000` | Largest combined attachment payload per message. |
| `HEPHAESTUS_UPLOAD_MAX_FILES` | `5` | Maximum attachments per message. |

The OCR image limit cannot exceed the per-file limit, and the per-file limit
cannot exceed the total message limit.

## Checks

```sh
make test
make vet
make build
```