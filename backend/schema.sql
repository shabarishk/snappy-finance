CREATE TABLE IF NOT EXISTS plaid_items (
    item_id TEXT PRIMARY KEY,
    access_token_encrypted BYTEA NOT NULL,
    cursor TEXT NOT NULL DEFAULT '',
    demo_data BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_synced_at TIMESTAMPTZ
);

ALTER TABLE plaid_items ADD COLUMN IF NOT EXISTS demo_data BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS accounts (
    account_id TEXT PRIMARY KEY,
    item_id TEXT NOT NULL REFERENCES plaid_items(item_id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    official_name TEXT,
    mask TEXT,
    type TEXT NOT NULL DEFAULT '',
    subtype TEXT NOT NULL DEFAULT '',
    current_balance NUMERIC(18, 4),
    iso_currency_code TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS transactions (
    transaction_id TEXT PRIMARY KEY,
    item_id TEXT NOT NULL REFERENCES plaid_items(item_id) ON DELETE CASCADE,
    account_id TEXT NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    merchant_name TEXT,
    amount NUMERIC(18, 4) NOT NULL,
    transaction_date DATE NOT NULL,
    pending BOOLEAN NOT NULL DEFAULT FALSE,
    category_primary TEXT,
    category_detailed TEXT,
    iso_currency_code TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS transactions_date_idx ON transactions (transaction_date DESC);
CREATE INDEX IF NOT EXISTS transactions_account_date_idx ON transactions (account_id, transaction_date DESC);
