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

## Checks

```sh
npm run lint
npm run build
```
