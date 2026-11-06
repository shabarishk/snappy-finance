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
    excluded_from_calculations BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS excluded_from_calculations BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS statement_parsers (
    parser_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE
);

INSERT INTO statement_parsers (parser_id, display_name)
VALUES
    ('bilt', 'Bilt card statement'),
    ('chase', 'Chase credit card statement')
ON CONFLICT (parser_id) DO UPDATE SET display_name = EXCLUDED.display_name, enabled = TRUE;

ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS statement_parser_id TEXT
    REFERENCES statement_parsers(parser_id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS finance_settings (
    setting_id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (setting_id = 1),
    expendable_cash_account_id TEXT REFERENCES accounts(account_id) ON DELETE SET NULL,
    spend_month_start_day SMALLINT NOT NULL DEFAULT 4 CHECK (spend_month_start_day BETWEEN 1 AND 31),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE finance_settings
    ADD COLUMN IF NOT EXISTS spend_month_start_day SMALLINT NOT NULL DEFAULT 4
    CHECK (spend_month_start_day BETWEEN 1 AND 31);

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

ALTER TABLE scheduled_projection_expenses
    ADD COLUMN IF NOT EXISTS merchant_name TEXT NOT NULL DEFAULT '';
ALTER TABLE scheduled_projection_expenses
    ADD COLUMN IF NOT EXISTS merchant_names TEXT[] NOT NULL DEFAULT '{}';
UPDATE scheduled_projection_expenses
SET merchant_names = ARRAY[merchant_name]
WHERE cardinality(merchant_names) = 0 AND btrim(merchant_name) <> '';
ALTER TABLE scheduled_projection_expenses
    ADD COLUMN IF NOT EXISTS account_id TEXT REFERENCES accounts(account_id) ON DELETE SET NULL;
ALTER TABLE scheduled_projection_expenses
    ADD COLUMN IF NOT EXISTS tracking_mode TEXT NOT NULL DEFAULT 'fixed';
ALTER TABLE scheduled_projection_expenses
    ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE scheduled_projection_expenses
    DROP CONSTRAINT IF EXISTS scheduled_projection_expenses_tracking_mode_check;
ALTER TABLE scheduled_projection_expenses
    ADD CONSTRAINT scheduled_projection_expenses_tracking_mode_check
    CHECK (tracking_mode IN ('fixed', 'variable'));

CREATE TABLE IF NOT EXISTS subscription_amount_history (
    history_id BIGSERIAL PRIMARY KEY,
    expense_id BIGINT NOT NULL REFERENCES scheduled_projection_expenses(expense_id) ON DELETE CASCADE,
    amount NUMERIC(18, 4) NOT NULL,
    effective_date DATE NOT NULL,
    change_type TEXT NOT NULL CHECK (change_type IN ('initial', 'admin_edit', 'confirmed_increase')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (expense_id, amount, effective_date, change_type)
);

INSERT INTO subscription_amount_history (expense_id, amount, effective_date, change_type)
SELECT expense_id, amount, first_due_date, 'initial'
FROM scheduled_projection_expenses
ON CONFLICT DO NOTHING;

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
FROM finance_settings settings
WHERE settings.setting_id = 1
  AND projected_expendable_income.frozen_at IS NULL
  AND (
      (date_trunc('month', projected_expendable_income.period_start) + INTERVAL '1 month')::date
      + LEAST(
          settings.spend_month_start_day,
          EXTRACT(DAY FROM (
              date_trunc('month', projected_expendable_income.period_start)
              + INTERVAL '2 months' - INTERVAL '1 day'
          ))::int
      ) - 2
  ) < CURRENT_DATE;

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
    ('investments', 'Investments', 'expense'),
    ('schooling', 'Schooling', 'expense'),
    ('gaming', 'Gaming', 'expense'),
    ('other', 'Other', 'expense'),
    ('income', 'Income', 'income'),
    ('transfer', 'Transfer', 'income'),
    ('other_income', 'Other', 'income'),
    ('expendable_income', 'Expendable income', 'income')
ON CONFLICT (category_id) DO NOTHING;

UPDATE transaction_categories
SET label = 'Subscriptions/Streaming'
WHERE category_id = 'streaming' AND label IS DISTINCT FROM 'Subscriptions/Streaming';

CREATE TABLE IF NOT EXISTS app_schema_migrations (
    migration_id TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM app_schema_migrations
        WHERE migration_id = 'exclude-inter-account-transfer-categories'
    ) THEN
        UPDATE transaction_categories
        SET excluded_from_totals = TRUE
        WHERE category_id IN ('transfer', 'transfer_expense');
        INSERT INTO app_schema_migrations (migration_id)
        VALUES ('exclude-inter-account-transfer-categories');
    END IF;
END $$;

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

DROP FUNCTION IF EXISTS transaction_bank_category_mapping(TEXT);

CREATE OR REPLACE FUNCTION transaction_bank_category_mapping(p_category_detailed TEXT, p_amount NUMERIC)
RETURNS TEXT
LANGUAGE SQL
IMMUTABLE
AS $$
    SELECT CASE
        WHEN p_category_detailed IN (
            'FOOD_AND_DRINK_OTHER_FOOD_AND_DRINK',
            'FOOD_AND_DRINK_FAST_FOOD',
            'FOOD_AND_DRINK_RESTAURANT',
            'FOOD_AND_DRINK_COFFEE'
        ) THEN 'restaurants'
        WHEN p_category_detailed IN (
            'TRANSFER_IN_ACCOUNT_TRANSFER',
            'TRANSFER_IN_SAVINGS',
            'TRANSFER_OUT_ACCOUNT_TRANSFER',
            'TRANSFER_OUT_SAVINGS'
        ) THEN CASE WHEN p_amount < 0 THEN 'transfer' ELSE 'transfer_expense' END
    END;
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

CREATE TABLE IF NOT EXISTS imported_statements (
    statement_id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL,
    account_name TEXT NOT NULL,
    parser_id TEXT NOT NULL REFERENCES statement_parsers(parser_id),
    period_start DATE NOT NULL,
    period_end DATE NOT NULL,
    source_sha256 TEXT NOT NULL,
    spreadsheet BYTEA NOT NULL,
    review_status TEXT NOT NULL DEFAULT 'approved',
    reviewed_at TIMESTAMPTZ,
    reviewed_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    statement_balance NUMERIC(18, 4),
    payment_due_date DATE,
    imported_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (period_start <= period_end),
    CHECK (review_status IN ('pending_review', 'approved', 'rejected')),
    UNIQUE (account_id, source_sha256)
);

ALTER TABLE imported_statements ADD COLUMN IF NOT EXISTS review_status TEXT NOT NULL DEFAULT 'approved';
ALTER TABLE imported_statements ADD COLUMN IF NOT EXISTS reviewed_at TIMESTAMPTZ;
ALTER TABLE imported_statements ADD COLUMN IF NOT EXISTS reviewed_by TEXT REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE imported_statements ADD COLUMN IF NOT EXISTS statement_balance NUMERIC(18, 4);
ALTER TABLE imported_statements ADD COLUMN IF NOT EXISTS payment_due_date DATE;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'imported_statements_statement_balance_check'
          AND conrelid = 'imported_statements'::regclass
    ) THEN
        ALTER TABLE imported_statements
            ADD CONSTRAINT imported_statements_statement_balance_check
            CHECK (statement_balance IS NULL OR statement_balance >= 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'imported_statements_review_status_check'
          AND conrelid = 'imported_statements'::regclass
    ) THEN
        ALTER TABLE imported_statements
            ADD CONSTRAINT imported_statements_review_status_check
            CHECK (review_status IN ('pending_review', 'approved', 'rejected'));
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS imported_statements_approved_balance_idx
    ON imported_statements (account_id, payment_due_date, reviewed_at DESC)
    WHERE review_status = 'approved'
      AND statement_balance IS NOT NULL
      AND payment_due_date IS NOT NULL;

CREATE TABLE IF NOT EXISTS statement_import_transactions (
    statement_transaction_id BIGSERIAL PRIMARY KEY,
    statement_id TEXT NOT NULL REFERENCES imported_statements(statement_id) ON DELETE CASCADE,
    row_number INTEGER NOT NULL,
    transaction_date DATE NOT NULL,
    merchant TEXT NOT NULL,
    amount NUMERIC(18, 2) NOT NULL,
    match_status TEXT NOT NULL CHECK (match_status IN ('matched', 'statement_only', 'ambiguous')),
    matched_plaid_transaction_id TEXT,
    matched_plaid_date DATE,
    matched_plaid_merchant TEXT,
    matched_plaid_amount NUMERIC(18, 2),
    category_id TEXT REFERENCES transaction_categories(category_id),
    category_rule_exempt BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (statement_id, row_number)
);

ALTER TABLE statement_import_transactions
    ADD COLUMN IF NOT EXISTS category_id TEXT REFERENCES transaction_categories(category_id);
ALTER TABLE statement_import_transactions
    ADD COLUMN IF NOT EXISTS category_rule_exempt BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE statement_import_transactions
    ALTER COLUMN category_rule_exempt SET DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS statement_import_transactions_category_idx
    ON statement_import_transactions (category_id);

CREATE TABLE IF NOT EXISTS statement_reconciliation_issues (
    discrepancy_id BIGSERIAL PRIMARY KEY,
    statement_id TEXT NOT NULL REFERENCES imported_statements(statement_id) ON DELETE CASCADE,
    plaid_transaction_id TEXT NOT NULL,
    transaction_date DATE NOT NULL,
    merchant TEXT NOT NULL,
    amount NUMERIC(18, 2) NOT NULL,
    issue_type TEXT NOT NULL CHECK (issue_type IN ('plaid_only', 'ambiguous_plaid')),
    UNIQUE (statement_id, plaid_transaction_id)
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
  AND c.category_id NOT IN ('transfer', 'transfer_expense')
ORDER BY c.category_type, t.category_match_key, t.transaction_date DESC, t.updated_at DESC
ON CONFLICT (category_type, match_key) DO NOTHING;

UPDATE transactions t
SET category_id = r.category_id
FROM transaction_category_rules r
WHERE t.category_match_key = r.match_key
  AND r.category_type = CASE WHEN t.amount < 0 THEN 'income' ELSE 'expense' END
  AND t.category_rule_exempt = FALSE
  AND t.category_id IS DISTINCT FROM r.category_id;

UPDATE transactions t
SET category_id = transaction_bank_category_mapping(t.category_detailed, t.amount)
WHERE t.category_rule_exempt = FALSE
  AND transaction_bank_category_mapping(t.category_detailed, t.amount) IS NOT NULL
  AND NOT EXISTS (
      SELECT 1
      FROM transaction_category_rules r
      WHERE r.match_key = t.category_match_key
        AND r.category_type = CASE WHEN t.amount < 0 THEN 'income' ELSE 'expense' END
  );

CREATE INDEX IF NOT EXISTS transactions_date_idx ON transactions (transaction_date DESC);
CREATE INDEX IF NOT EXISTS transactions_account_date_idx ON transactions (account_id, transaction_date DESC);
CREATE INDEX IF NOT EXISTS transactions_category_idx ON transactions (category_id);
CREATE INDEX IF NOT EXISTS transactions_category_match_key_idx ON transactions (category_match_key);
CREATE INDEX IF NOT EXISTS transactions_transfer_amount_date_idx ON transactions (amount, transaction_date);

CREATE TABLE IF NOT EXISTS subscription_payment_history (
    history_id BIGSERIAL PRIMARY KEY,
    expense_id BIGINT NOT NULL REFERENCES scheduled_projection_expenses(expense_id) ON DELETE CASCADE,
    transaction_id TEXT NOT NULL,
    due_date DATE NOT NULL,
    payment_date DATE NOT NULL,
    amount NUMERIC(18, 4) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (expense_id, transaction_id)
);

CREATE TABLE IF NOT EXISTS subscription_increase_reviews (
    review_id BIGSERIAL PRIMARY KEY,
    expense_id BIGINT NOT NULL REFERENCES scheduled_projection_expenses(expense_id) ON DELETE CASCADE,
    transaction_id TEXT NOT NULL,
    previous_amount NUMERIC(18, 4) NOT NULL,
    observed_amount NUMERIC(18, 4) NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'confirmed', 'dismissed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ,
    UNIQUE (expense_id, transaction_id)
);

ALTER TABLE subscription_payment_history
    DROP CONSTRAINT IF EXISTS subscription_payment_history_transaction_id_fkey;
ALTER TABLE subscription_increase_reviews
    DROP CONSTRAINT IF EXISTS subscription_increase_reviews_transaction_id_fkey;

CREATE OR REPLACE VIEW application_transactions AS
WITH approved_statement_aliases AS (
    SELECT DISTINCT ON (
        imported_statement.account_id,
        REGEXP_REPLACE(LOWER(statement_row.merchant), '[^a-z0-9]', '', 'g')
    )
        imported_statement.account_id,
        REGEXP_REPLACE(LOWER(statement_row.merchant), '[^a-z0-9]', '', 'g') AS merchant_key,
        BTRIM(statement_row.matched_plaid_merchant) AS merchant_alias
    FROM statement_import_transactions statement_row
    JOIN imported_statements imported_statement ON imported_statement.statement_id=statement_row.statement_id
    WHERE imported_statement.review_status='approved'
      AND statement_row.match_status='matched'
      AND NULLIF(BTRIM(statement_row.matched_plaid_merchant), '') IS NOT NULL
    ORDER BY imported_statement.account_id,
             REGEXP_REPLACE(LOWER(statement_row.merchant), '[^a-z0-9]', '', 'g'),
             imported_statement.reviewed_at DESC NULLS LAST,
             statement_row.statement_transaction_id DESC
)
SELECT plaid_txn.transaction_id, plaid_txn.account_id, plaid_txn.name,
       COALESCE(NULLIF(plaid_txn.merchant_name, ''), aliases.merchant_alias) AS merchant_name,
       plaid_txn.amount, plaid_txn.transaction_date, plaid_txn.pending,
       plaid_txn.category_primary, plaid_txn.category_detailed, plaid_txn.iso_currency_code,
       plaid_txn.hidden_from_users, plaid_txn.category_id,
       plaid_txn.category_match_key <> '' AS has_category_match,
       FALSE AS is_statement_import,
       plaid_txn.category_match_key
FROM transactions plaid_txn
LEFT JOIN approved_statement_aliases aliases
  ON aliases.account_id=plaid_txn.account_id
 AND aliases.merchant_key=REGEXP_REPLACE(
     LOWER(COALESCE(NULLIF(plaid_txn.merchant_name, ''), plaid_txn.name)),
     '[^a-z0-9]', '', 'g'
 )

UNION ALL

SELECT 'statement-import-' || statement_row.statement_transaction_id::text,
       imported_statement.account_id, statement_row.merchant, aliases.merchant_alias,
       -statement_row.amount, statement_row.transaction_date, FALSE,
       NULL::text, NULL::text, account.iso_currency_code, FALSE,
       statement_row.category_id,
       transaction_category_match_key(
           COALESCE(NULLIF(aliases.merchant_alias, ''), statement_row.merchant),
           statement_row.merchant,
           imported_statement.account_id,
           (-statement_row.amount) < 0
       ) <> '',
       TRUE,
       transaction_category_match_key(
           COALESCE(NULLIF(aliases.merchant_alias, ''), statement_row.merchant),
           statement_row.merchant,
           imported_statement.account_id,
           (-statement_row.amount) < 0
       )
FROM statement_import_transactions statement_row
JOIN imported_statements imported_statement ON imported_statement.statement_id=statement_row.statement_id
LEFT JOIN accounts account ON account.account_id=imported_statement.account_id
LEFT JOIN approved_statement_aliases aliases
  ON aliases.account_id=imported_statement.account_id
 AND aliases.merchant_key=REGEXP_REPLACE(LOWER(statement_row.merchant), '[^a-z0-9]', '', 'g')
WHERE imported_statement.review_status='approved'
  AND statement_row.match_status='statement_only';
