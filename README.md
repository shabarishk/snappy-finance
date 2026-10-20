# Snappy Finance

A self-hosted personal finance app scaffold using Go, React, PostgreSQL, and Plaid.

## What is included

- Single-user password login with signed, HTTP-only session cookies.
- Plaid Link token creation and public-token exchange.
- AES-GCM encryption of stored Plaid access tokens.
- Cursor-based Plaid Transactions Sync with added, modified, and removed transaction handling.
- Periodic sync worker and a manual sync action.
- React dashboard for linked accounts and recent transactions.
- Docker Compose deployment with PostgreSQL data persisted in a named volume.

## Run locally with Docker

1. Copy `.env.example` to `.env`.
2. Set unique values for `APP_PASSWORD`, `SESSION_SECRET` (at least 32 characters), `APP_ENCRYPTION_KEY`, and `POSTGRES_PASSWORD`.
   Generate the encryption key with `openssl rand -base64 32`.
3. Add Plaid Sandbox credentials to `PLAID_CLIENT_ID` and `PLAID_SECRET` from the Plaid dashboard.
4. Start the app:

   ```sh
   docker compose up --build
   ```

5. Open `http://localhost:8080` and sign in with `APP_PASSWORD`.

The API is bound to loopback on port 8081 by default; the web container proxies `/api` to it. For Unraid, set `WEB_PORT` to the host port you want to publish. Keep the app private behind a VPN or a TLS-terminating reverse proxy; do not expose it directly to the internet over plain HTTP.

## Run frontend and backend separately

Start PostgreSQL, set the backend and PostgreSQL environment variables for your shell, and run the API. The Go driver uses `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, and `PGPASSWORD` if `DATABASE_URL` is not set:

```sh
cd backend
go run .
```

In another terminal:

```sh
cd frontend
npm install
npm run dev
```

Vite serves the UI on port 5173 and proxies API requests to `localhost:8081`. Set `API_PORT=127.0.0.1:8081` when running the API locally.

## Important deployment notes

- Plaid credentials are server-side only. Never put them in frontend environment variables.
- Back up PostgreSQL and the `APP_ENCRYPTION_KEY` separately. Losing the key makes stored Plaid access tokens unrecoverable.
- A database backup alone is not sufficient; protect backups as financial data.
- This scaffold uses periodic sync. A public Plaid webhook endpoint with signature verification is intentionally not enabled yet; configure one before relying on webhook-triggered updates.
- Configure Plaid Link products, country codes, and production access for your region before moving beyond Sandbox.
