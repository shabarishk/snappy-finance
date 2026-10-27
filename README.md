# Snappy Finance

A self-hosted personal finance app scaffold using Go, React, PostgreSQL, and Plaid.

## What is included

- Admin and household-user profiles with individual passwords and HTTP-only sessions.
- Shared household financial data, profile-specific dashboard card preferences, and admin-only Plaid/profile management.
- Administrators can hide selected transactions from household users while retaining access to them.
- Administrators can categorize expense transactions separately from income and transfer transactions, and exclude categories from monthly spending or income totals.
- Transportation/Mobility is available as an expense category. When categorizing a transaction with a stable automated match, administrators can apply the category to that item only or to all related past and future matches.
- Gaming is available as an expense category, and Plaid food-and-drink categories for other food and drink, fast food, restaurants, and coffee are mapped to Restaurants unless a transaction-specific or merchant rule overrides the bank category.
- The dashboard's first summary card defaults to expendable income, based on transactions assigned the Expendable income category, and can be toggled to total income.
- Dashboard income, spending, and net statistics default to the spend calendar from the 4th through the 3rd; switch to calendar month from Dashboard preferences.
- The Overview heading opens a calendar-month selector; its history begins one year before first use in the browser and grows as new months arrive.
- The dashboard's cash balance card totals depository accounts only.
- Overview adds the administrator-selected expendable cash balance, total current credit balance, statement balances due in the selected statistics period, and automatically calculated projected expendable income.
- Overview displays expense-category pie charts for the current spend month and the next two. Cash-account purchases are assigned to the 4th–3rd spend month containing the transaction date; credit purchases are assigned according to the card's configured statement closing day and payment due day.
- Selecting a category in the Overview liquidity pie chart or legend opens a scrollable list of the included transactions for that spend month.
- The next two spend-month charts also show projected income, scheduled expenses, incurred expenses, and projected liquidity remaining. Scheduled payments are assigned using each account's spend-month rules and removed from the scheduled total once matched as paid, avoiding double-counting. Remaining liquidity is unavailable until projected income assumptions are complete.
- The Overview payment tracker matches scheduled subscriptions and bills to posted transactions on the configured merchant and account, with three spend-month tabs synchronized to the liquidity pie chart. Fixed subscription amount increases require administrator confirmation; variable bills such as utilities match without an amount check. Matched payments and confirmed amount changes are retained for history.
- Each profile can independently exclude expense categories from their liquidity charts and forecast totals through the Overview Filter drawer; administrator-wide category exclusions continue to apply.
- Each profile can also exclude individual cash- or credit-account transactions from their own liquidity pie charts and incurred-expense forecast using the Transactions page.
- The Projections workspace forecasts the current spend cycle and the next 12 cycles from a fixed biweekly net paycheck, a twice-monthly net pay table, selected recurring federal holidays, and planned days off. It also displays one-time and recurring expenses due in each cycle, with weekly, monthly, every-two-months, or annual recurrence. The forecast period runs from the 4th through the 3rd, and each completed cycle's income amount is frozen.
- Administrators can configure the expendable cash account, each credit card's statement closing and payment due days, income assumptions, and scheduled expenses with merchant, payment account, and fixed or variable amount tracking. Financial Management displays retrieved Plaid statement balances by account and due date; balances can also be entered manually when an institution or existing Plaid connection does not provide them.
- Each profile can hide and reorder all eight Overview summary cards from the Filter drawer.
- Dashboard filters and summary-card preferences open in a slide-in drawer from the right.
- Applying a category to related items creates a reusable rule that updates similar existing transactions and applies to future syncs; applying it to one item leaves the rule and other transactions unchanged. Expense rules match the normalized first three merchant/description words; income rules match the normalized first three transaction-name words on the same account.
- The Administration page separates user management and financial management into tabs.
- Plaid Link token creation and public-token exchange.
- AES-GCM encryption of stored Plaid access tokens.
- Cursor-based Plaid Transactions Sync with added, modified, and removed transaction handling.
- Periodic sync worker and a manual sync action.
- React dashboard for linked accounts and a separate Transactions page with cash, credit, and loan account tabs and selectable 25/50/75/100-item pages.
- The Transactions page displays each transaction's account and supports multi-account filtering within the active account type.
- Docker Compose deployment with PostgreSQL data persisted in a named volume.

## Run locally with Docker

For a one-command Windows setup with fictional sample data and generated local credentials, run:

```powershell
.\scripts\start-demo.ps1
```

Open `http://localhost:8080` and use the sign-in credentials printed by the script. Press Ctrl+C to stop the app. The script stores demo-only credentials in `.env.demo` and uses a separate Compose project/database volume. To regenerate credentials, stop the app and run `.\scripts\start-demo.ps1 -ResetCredentials`; this removes the demo database volume and its demo data.

To configure manually:

1. Copy `.env.example` to `.env`.
2. Set `ADMIN_USERNAME` and `ADMIN_PASSWORD` for the first administrator, `SESSION_SECRET` (at least 32 characters), `APP_ENCRYPTION_KEY`, and `POSTGRES_PASSWORD`.
   Generate the encryption key with `openssl rand -base64 32`.
3. To preview the app without Plaid, set `DEMO_DATA=true`. This seeds fictional accounts and transactions; Plaid credentials are not needed. To use Plaid instead, leave `DEMO_DATA=false` and add Sandbox credentials to `PLAID_CLIENT_ID` and `PLAID_SECRET`.
4. Start the app:

   ```sh
   docker compose up --build
   ```

5. Open `http://localhost:8080` and sign in with the configured administrator username and password. The administrator can create household profiles from the Administration page.

The API is bound to loopback on port 8081 by default; the web container proxies `/api` to it. For Unraid, set `WEB_PORT` to the host port you want to publish. Keep the app private behind a VPN or a TLS-terminating reverse proxy; do not expose it directly to the internet over plain HTTP.

Demo rows are clearly labeled in the dashboard and excluded from Plaid sync. They are written once and remain in the PostgreSQL volume even after you turn `DEMO_DATA` off, so use a separate local database/Compose project for demo testing rather than enabling it on a database with real data.

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

## Publish and deploy on Unraid

The GitHub Actions workflow publishes a single app image to `ghcr.io/shabarishk/snappy-finance:latest` whenever changes are pushed to `main` (and version tags). The image contains the Go API and built React frontend; it does not contain PostgreSQL.

After the first successful workflow run, open the package under your GitHub profile's **Packages** page and set its visibility to **Public** if GitHub created it as private. The Unraid template pulls the public `latest` image.

### Add the Unraid launch template

On the Unraid server, download the template into the user templates folder:

```bash
mkdir -p /boot/config/plugins/dockerMan/templates-user
curl -fsSL https://raw.githubusercontent.com/shabarishk/snappy-finance/main/unraid/snappy-finance.xml \
  -o /boot/config/plugins/dockerMan/templates-user/my-snappy-finance.xml
```

Then open **Docker → Add Container** and select the `snappy-finance` user template. The launch form includes the PostgreSQL host, port, database, username, password, first-run administrator username and password, session secret, encryption key, and optional Plaid settings. Unraid saves these settings in the user template and reuses them when the container is restarted or upgraded.

On the first start against an empty database, the app creates the administrator profile from `ADMIN_USERNAME` and `ADMIN_PASSWORD` (at least 12 characters). The administrator can create regular household profiles from the Administration page. Both profiles see the shared accounts and transactions and control their own dashboard summary cards. Every profile can change their password from the Administration page. Only administrators can connect/sync Plaid, configure finance dashboard metrics, or create profiles. The legacy `APP_PASSWORD` value is accepted as a bootstrap password fallback for existing deployments.

Create the database and a PostgreSQL role for the app in your existing PostgreSQL container first. For a normal Unraid `bridge` network setup, set `PGHOST` to the Unraid server's LAN IP and `PGPORT` to the port that the PostgreSQL container publishes on the Unraid host. Do **not** use `localhost` or `127.0.0.1`; those addresses refer to the Snappy Finance container itself. If both containers share a user-defined Docker network, you can instead use the PostgreSQL container name and internal port.

The app stores all financial rows in that PostgreSQL database and does not need its own data volume. Keep `APP_ENCRYPTION_KEY` stable and back it up securely; changing it makes existing Plaid access tokens unreadable. `PGPASSWORD`, `APP_PASSWORD`, `SESSION_SECRET`, `APP_ENCRYPTION_KEY`, and `PLAID_SECRET` are masked fields in the template.

For demo-only testing, set **Seed demo data** to `true` on a disposable database. Leave it `false` when connecting real financial data. The current app only makes outbound Plaid requests and uses periodic sync; it does not require an inbound webhook port.

## Important deployment notes

- Plaid credentials are server-side only. Never put them in frontend environment variables.
- Back up PostgreSQL and the `APP_ENCRYPTION_KEY` separately. Losing the key makes stored Plaid access tokens unrecoverable.
- A database backup alone is not sufficient; protect backups as financial data.
- This scaffold uses periodic sync. A public Plaid webhook endpoint with signature verification is intentionally not enabled yet; configure one before relying on webhook-triggered updates.
- Configure Plaid Link products, country codes, and production access for your region before moving beyond Sandbox.
