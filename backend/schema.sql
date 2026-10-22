CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('admin', 'user')),
    dashboard_preferences JSONB NOT NULL DEFAULT '{"hidden_cards":[]}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    disabled_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS user_sessions (
    token_hash BYTEA PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS user_sessions_user_id_idx ON user_sessions (user_id);

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

CREATE TABLE IF NOT EXISTS finance_settings (
    setting_id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (setting_id = 1),
    expendable_cash_account_id TEXT REFERENCES accounts(account_id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO finance_settings (setting_id)
VALUES (1)
ON CONFLICT (setting_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS projected_expendable_income (
    period_start DATE PRIMARY KEY,
    amount NUMERIC(18, 4) NOT NULL CHECK (amount >= 0),
    frozen_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS income_projection_settings (
    setting_id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (setting_id = 1),
    biweekly_pay_amount NUMERIC(18, 4) CHECK (biweekly_pay_amount >= 0),
    biweekly_payday_anchor DATE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO income_projection_settings (setting_id)
VALUES (1)
ON CONFLICT (setting_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS projection_wife_pay_rates (
    workday_count SMALLINT PRIMARY KEY CHECK (workday_count BETWEEN 0 AND 12),
    net_pay NUMERIC(18, 4) NOT NULL CHECK (net_pay >= 0)
);

CREATE TABLE IF NOT EXISTS projection_federal_holidays (
    holiday_id TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS projection_days_off (
    day_off DATE PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS scheduled_projection_expenses (
    expense_id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL CHECK (LENGTH(BTRIM(name)) BETWEEN 1 AND 100),
    amount NUMERIC(18, 4) NOT NULL CHECK (amount >= 0),
    frequency TEXT NOT NULL CHECK (frequency IN ('one_time', 'weekly', 'monthly', 'bimonthly', 'annual')),
    first_due_date DATE NOT NULL
);

ALTER TABLE scheduled_projection_expenses
    DROP CONSTRAINT IF EXISTS scheduled_projection_expenses_frequency_check;
ALTER TABLE scheduled_projection_expenses
    ADD CONSTRAINT scheduled_projection_expenses_frequency_check
    CHECK (frequency IN ('one_time', 'weekly', 'monthly', 'bimonthly', 'annual'));

CREATE TABLE IF NOT EXISTS credit_card_statements (
    account_id TEXT NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    due_date DATE NOT NULL,
    statement_date DATE,
    statement_balance NUMERIC(18, 4) NOT NULL CHECK (statement_balance >= 0),
    source TEXT NOT NULL CHECK (source IN ('plaid', 'manual')),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id, due_date, source)
);

CREATE INDEX IF NOT EXISTS credit_card_statements_due_date_idx ON credit_card_statements (due_date);

CREATE TABLE IF NOT EXISTS credit_card_cycles (
    account_id TEXT PRIMARY KEY REFERENCES accounts(account_id) ON DELETE CASCADE,
    statement_close_day SMALLINT NOT NULL CHECK (statement_close_day BETWEEN 1 AND 31),
    statement_due_day SMALLINT NOT NULL CHECK (statement_due_day BETWEEN 1 AND 31),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

UPDATE projected_expendable_income
SET frozen_at = NOW()
WHERE frozen_at IS NULL
  AND period_start + INTERVAL '1 month' - INTERVAL '1 day' < CURRENT_DATE;

CREATE TABLE IF NOT EXISTS transaction_categories (
    category_id TEXT PRIMARY KEY,
    label TEXT NOT NULL,
    category_type TEXT NOT NULL DEFAULT 'expense',
    excluded_from_totals BOOLEAN NOT NULL DEFAULT FALSE
);

ALTER TABLE transaction_categories ADD COLUMN IF NOT EXISTS category_type TEXT NOT NULL DEFAULT 'expense';

INSERT INTO transaction_categories (category_id, label, category_type) VALUES
    ('credit_card_payment', 'Credit card payment', 'expense'),
    ('mortgage_payment', 'Mortgage payment', 'expense'),
    ('utility_payment', 'Utility payment', 'expense'),
    ('transfer_expense', 'Transfer', 'expense'),
    ('grocery', 'Grocery', 'expense'),
    ('restaurants', 'Restaurants', 'expense'),
    ('food_delivery', 'Food delivery', 'expense'),
    ('recreation_activities', 'Recreation & Activities', 'expense'),
    ('shopping', 'Shopping', 'expense'),
    ('streaming', 'Streaming', 'expense'),
    ('home_maintenance', 'Home maintenance', 'expense'),
    ('transportation_mobility', 'Transportation/Mobility', 'expense'),
    ('other', 'Other', 'expense'),
    ('income', 'Income', 'income'),
    ('transfer', 'Transfer', 'income'),
    ('other_income', 'Other', 'income'),
    ('expendable_income', 'Expendable income', 'income')
ON CONFLICT (category_id) DO NOTHING;

UPDATE transaction_categories
SET label = 'Subscriptions/Streaming'
WHERE category_id = 'streaming' AND label IS DISTINCT FROM 'Subscriptions/Streaming';

DROP FUNCTION IF EXISTS transaction_category_match_key(TEXT, TEXT);

CREATE OR REPLACE FUNCTION transaction_category_match_key(
    p_merchant_name TEXT,
    p_transaction_name TEXT,
    p_account_id TEXT,
    p_is_income BOOLEAN
)
RETURNS TEXT
LANGUAGE SQL
IMMUTABLE
AS $$
    WITH source AS (
        SELECT CASE
            WHEN p_is_income THEN COALESCE(p_transaction_name, '')
            ELSE COALESCE(NULLIF(btrim(p_merchant_name), ''), p_transaction_name, '')
        END AS raw_name
    ), normalized AS (
        SELECT array_to_string(
            (regexp_split_to_array(
                btrim(regexp_replace(lower(raw_name), '[^[:alnum:]]+', ' ', 'g')),
                ' '
            ))[1:3],
            ' '
        ) AS name_key
        FROM source
    )
    SELECT CASE
        WHEN p_is_income THEN COALESCE(p_account_id, '') || '|' || name_key
        ELSE name_key
    END
    FROM normalized;
$$;

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
    category_id TEXT REFERENCES transaction_categories(category_id),
    category_match_key TEXT NOT NULL DEFAULT '',
    category_rule_exempt BOOLEAN NOT NULL DEFAULT FALSE,
    hidden_from_users BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE transactions ADD COLUMN IF NOT EXISTS category_id TEXT REFERENCES transaction_categories(category_id);
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS category_match_key TEXT NOT NULL DEFAULT '';
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS category_rule_exempt BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS hidden_from_users BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE transactions t
SET category_id = 'other_income'
FROM transaction_categories c
WHERE t.category_id = c.category_id
  AND c.category_type = 'expense'
  AND t.amount < 0;

CREATE TABLE IF NOT EXISTS transaction_category_rules (
    category_type TEXT NOT NULL,
    match_key TEXT NOT NULL,
    category_id TEXT NOT NULL REFERENCES transaction_categories(category_id),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (category_type, match_key)
);

INSERT INTO transaction_category_rules (category_type, match_key, category_id)
SELECT DISTINCT ON (transaction_category_match_key(t.merchant_name, t.name, t.account_id, TRUE))
    'income',
    transaction_category_match_key(t.merchant_name, t.name, t.account_id, TRUE),
    r.category_id
FROM transaction_category_rules r
JOIN transactions t ON t.category_match_key = r.match_key AND t.amount < 0
WHERE r.category_type = 'income'
  AND r.match_key NOT LIKE '%|%'
ORDER BY transaction_category_match_key(t.merchant_name, t.name, t.account_id, TRUE),
         t.transaction_date DESC, t.updated_at DESC
ON CONFLICT (category_type, match_key) DO NOTHING;

UPDATE transactions
SET category_match_key = transaction_category_match_key(merchant_name, name, account_id, amount < 0)
WHERE category_match_key IS DISTINCT FROM transaction_category_match_key(merchant_name, name, account_id, amount < 0);

DELETE FROM transaction_category_rules
WHERE category_type = 'income'
  AND match_key NOT LIKE '%|%';

INSERT INTO transaction_category_rules (category_type, match_key, category_id)
SELECT DISTINCT ON (c.category_type, t.category_match_key)
    c.category_type, t.category_match_key, t.category_id
FROM transactions t
JOIN transaction_categories c ON c.category_id = t.category_id
WHERE t.category_match_key <> ''
  AND c.category_type = CASE WHEN t.amount < 0 THEN 'income' ELSE 'expense' END
ORDER BY c.category_type, t.category_match_key, t.transaction_date DESC, t.updated_at DESC
ON CONFLICT (category_type, match_key) DO NOTHING;

UPDATE transactions t
SET category_id = r.category_id
FROM transaction_category_rules r
WHERE t.category_match_key = r.match_key
  AND r.category_type = CASE WHEN t.amount < 0 THEN 'income' ELSE 'expense' END
  AND t.category_rule_exempt = FALSE
  AND t.category_id IS DISTINCT FROM r.category_id;

CREATE INDEX IF NOT EXISTS transactions_date_idx ON transactions (transaction_date DESC);
CREATE INDEX IF NOT EXISTS transactions_account_date_idx ON transactions (account_id, transaction_date DESC);
CREATE INDEX IF NOT EXISTS transactions_category_idx ON transactions (category_id);
CREATE INDEX IF NOT EXISTS transactions_category_match_key_idx ON transactions (category_match_key);
