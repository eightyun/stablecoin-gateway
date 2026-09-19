DROP TRIGGER IF EXISTS journal_entries_immutable_guard ON journal_entries;
DROP FUNCTION IF EXISTS protect_journal_entry();

DROP TRIGGER IF EXISTS journal_transactions_immutable_guard ON journal_transactions;
DROP FUNCTION IF EXISTS protect_journal_transaction();

DROP TRIGGER IF EXISTS journal_transactions_balance_guard ON journal_transactions;
DROP FUNCTION IF EXISTS assert_journal_transaction_balanced();

ALTER TABLE journal_transactions
    DROP CONSTRAINT IF EXISTS journal_transactions_status_check,
    DROP COLUMN IF EXISTS reversed_at,
    DROP COLUMN IF EXISTS posted_at,
    DROP COLUMN IF EXISTS status;
