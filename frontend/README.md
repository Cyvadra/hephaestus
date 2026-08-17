# Hephaestus Frontend

React + TypeScript chat interface for the Hephaestus API.

## Development

Start the backend from the repository root:

```sh
go run ./cmd/hephaestus
```

Then start the Vite development server:

```sh
cd frontend
npm install
npm run dev
```

Requests under `/api` are proxied to `http://localhost:9016`.

## Deployment

`npm run preview` listens on `0.0.0.0:5173` and proxies `/api` to the
loopback-only backend at `127.0.0.1:9016`. Terminate HTTPS in a reverse proxy
before exposing the preview server to an untrusted network.

The frontend uses browser URLs for projects, chats, and configuration pages.
Configure the production static host or reverse proxy to serve `index.html` for
unknown non-API paths, while continuing to forward `/api` to the backend. This
SPA fallback is required for refreshing or directly opening a nested URL.

## Checks

```sh
npm run lint
npm run build
```

## Localization

UI resources live in `src/i18n/locales.ts`. The active language is persisted in
browser storage and defaults to Chinese for Chinese browser locales, otherwise
English. Use `useTranslation()` and semantic keys for user-facing text; do not
translate API values, user-authored content, or configuration payloads.

Run the candidate inventory while migrating a feature:

```sh
npm run i18n:audit
```
