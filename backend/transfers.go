package main

import "context"

func (a *app) matchInterAccountTransfers(ctx context.Context) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE transactions t
		SET category_id = transaction_bank_category_mapping(t.category_detailed, t.amount)
		WHERE t.category_id IN ('transfer', 'transfer_expense')
		  AND t.category_rule_exempt = FALSE
		  AND COALESCE(transaction_bank_category_mapping(t.category_detailed, t.amount), '') NOT IN ('transfer', 'transfer_expense')
		  AND NOT EXISTS (
			  SELECT 1
			  FROM transaction_category_rules r
			  WHERE r.match_key = t.category_match_key
			    AND r.category_type = CASE WHEN t.amount < 0 THEN 'income' ELSE 'expense' END
		  )`)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		WITH eligible AS (
			SELECT t.transaction_id, t.account_id, t.name, t.merchant_name, t.amount,
			       t.transaction_date, t.iso_currency_code, t.category_primary, t.category_detailed
			FROM transactions t
			WHERE t.pending = FALSE
			  AND t.category_rule_exempt = FALSE
			  AND NOT EXISTS (
				  SELECT 1
				  FROM transaction_category_rules r
				  WHERE r.match_key = t.category_match_key
				    AND r.category_type = CASE WHEN t.amount < 0 THEN 'income' ELSE 'expense' END
			  )
		),
		candidates AS (
			SELECT debit.transaction_id AS debit_id,
			       credit.transaction_id AS credit_id,
			       ABS(debit.transaction_date - credit.transaction_date) AS days_apart
			FROM eligible debit
			JOIN eligible credit
			  ON debit.amount > 0
			 AND credit.amount < 0
			 AND debit.account_id <> credit.account_id
			 AND debit.iso_currency_code IS NOT NULL
			 AND debit.iso_currency_code = credit.iso_currency_code
			 AND ROUND(debit.amount, 2) = ROUND(ABS(credit.amount), 2)
			 AND ABS(debit.transaction_date - credit.transaction_date) <= 3
			WHERE LOWER(CONCAT_WS(' ',
				debit.name, debit.merchant_name, debit.category_primary, debit.category_detailed,
				credit.name, credit.merchant_name, credit.category_primary, credit.category_detailed
			)) LIKE '%transfer%'
			   OR LOWER(CONCAT_WS(' ',
				debit.name, debit.merchant_name, debit.category_primary, debit.category_detailed,
				credit.name, credit.merchant_name, credit.category_primary, credit.category_detailed
			)) LIKE '%xfer%'
		),
		ranked AS (
			SELECT debit_id, credit_id,
			       ROW_NUMBER() OVER (PARTITION BY debit_id ORDER BY days_apart, credit_id) AS debit_rank,
			       ROW_NUMBER() OVER (PARTITION BY credit_id ORDER BY days_apart, debit_id) AS credit_rank
			FROM candidates
		),
		matches AS (
			SELECT debit_id AS transaction_id FROM ranked WHERE debit_rank = 1 AND credit_rank = 1
			UNION ALL
			SELECT credit_id AS transaction_id FROM ranked WHERE debit_rank = 1 AND credit_rank = 1
		)
		UPDATE transactions t
		SET category_id = CASE WHEN t.amount < 0 THEN 'transfer' ELSE 'transfer_expense' END
		FROM matches m
		WHERE t.transaction_id = m.transaction_id`)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}
