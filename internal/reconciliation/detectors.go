package reconciliation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	ruleLedgerTransactionUnbalanced   = "ledger.transaction_unbalanced"
	ruleDepositLedgerMismatch         = "business.deposit_ledger_reference_mismatch"
	rulePayoutLedgerMismatch          = "business.payout_ledger_reference_mismatch"
	ruleDepositLedgerSemanticMismatch = "business.deposit_ledger_semantic_mismatch"
	rulePayoutLedgerSemanticMismatch  = "business.payout_ledger_semantic_mismatch"
)

type balanceEvidence struct {
	EntryCount  int64  `json:"entry_count"`
	DebitTotal  string `json:"debit_total"`
	CreditTotal string `json:"credit_total"`
}

type referenceEvidence struct {
	LedgerTransactionID   string `json:"ledger_transaction_id"`
	ExpectedStatus        string `json:"expected_status"`
	ActualStatus          string `json:"actual_status"`
	ExpectedReferenceType string `json:"expected_reference_type"`
	ActualReferenceType   string `json:"actual_reference_type"`
	ExpectedReferenceID   string `json:"expected_reference_id"`
	ActualReferenceID     string `json:"actual_reference_id"`
}

type semanticEvidence struct {
	LedgerTransactionID   string `json:"ledger_transaction_id"`
	ExpectedAmount        string `json:"expected_amount"`
	EntryCount            int64  `json:"entry_count"`
	DebitTotal            string `json:"debit_total"`
	CreditTotal           string `json:"credit_total"`
	MatchingDebitEntries  int64  `json:"matching_debit_entries"`
	MatchingCreditEntries int64  `json:"matching_credit_entries"`
}

func detectUnbalancedTransactions(ctx context.Context, transaction pgx.Tx) ([]finding, int64, error) {
	var checked int64
	if err := transaction.QueryRow(ctx, `
		SELECT COUNT(*) FROM journal_transactions WHERE status = 'posted'
	`).Scan(&checked); err != nil {
		return nil, 0, fmt.Errorf("统计已过账交易: %w", err)
	}
	rows, err := transaction.Query(ctx, `
		SELECT journal.id::TEXT, COALESCE(entry.asset_id, ''), COUNT(entry.transaction_id),
		       COALESCE(SUM(entry.amount) FILTER (WHERE entry.side = 'D'), 0)::TEXT,
		       COALESCE(SUM(entry.amount) FILTER (WHERE entry.side = 'C'), 0)::TEXT
		FROM journal_transactions AS journal
		LEFT JOIN journal_entries AS entry ON entry.transaction_id = journal.id
		WHERE journal.status = 'posted'
		GROUP BY journal.id, entry.asset_id
		HAVING COUNT(entry.transaction_id) < 2
		    OR COALESCE(SUM(entry.amount) FILTER (WHERE entry.side = 'D'), 0)
		       IS DISTINCT FROM COALESCE(SUM(entry.amount) FILTER (WHERE entry.side = 'C'), 0)
		ORDER BY journal.id, entry.asset_id
	`)
	if err != nil {
		return nil, 0, fmt.Errorf("检测账务借贷平衡: %w", err)
	}
	defer rows.Close()
	findings := make([]finding, 0)
	for rows.Next() {
		var transactionID, assetID string
		var evidence balanceEvidence
		if err := rows.Scan(&transactionID, &assetID, &evidence.EntryCount, &evidence.DebitTotal, &evidence.CreditTotal); err != nil {
			return nil, 0, fmt.Errorf("读取账务借贷差异: %w", err)
		}
		encoded, err := json.Marshal(evidence)
		if err != nil {
			return nil, 0, fmt.Errorf("编码账务借贷差异: %w", err)
		}
		findings = append(findings, finding{
			RuleCode: ruleLedgerTransactionUnbalanced, Severity: SeverityCritical,
			ResourceType: "journal_transaction", ResourceID: transactionID,
			AssetID: assetID, Evidence: encoded,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("遍历账务借贷差异: %w", err)
	}
	return findings, checked, nil
}

func detectDepositLedgerMismatches(ctx context.Context, transaction pgx.Tx) ([]finding, int64, error) {
	var checked int64
	if err := transaction.QueryRow(ctx, `
		SELECT COUNT(*) FROM deposit_intents WHERE ledger_transaction_id IS NOT NULL
	`).Scan(&checked); err != nil {
		return nil, 0, fmt.Errorf("统计已入账充值: %w", err)
	}
	rows, err := transaction.Query(ctx, `
		SELECT intent.id::TEXT, intent.asset_id, intent.ledger_transaction_id::TEXT,
		       COALESCE(journal.status, 'missing'), COALESCE(journal.reference_type, ''),
		       COALESCE(journal.reference_id, '')
		FROM deposit_intents AS intent
		LEFT JOIN journal_transactions AS journal ON journal.id = intent.ledger_transaction_id
		WHERE intent.ledger_transaction_id IS NOT NULL
		  AND (
		      journal.id IS NULL
		      OR journal.status IS DISTINCT FROM 'posted'
		      OR journal.reference_type IS DISTINCT FROM 'deposit'
		      OR journal.reference_id IS DISTINCT FROM intent.id::TEXT
		  )
		ORDER BY intent.id
	`)
	if err != nil {
		return nil, 0, fmt.Errorf("检测充值账务引用: %w", err)
	}
	defer rows.Close()
	findings := make([]finding, 0)
	for rows.Next() {
		var intentID, assetID string
		evidence := referenceEvidence{ExpectedStatus: "posted", ExpectedReferenceType: "deposit"}
		if err := rows.Scan(
			&intentID, &assetID, &evidence.LedgerTransactionID,
			&evidence.ActualStatus, &evidence.ActualReferenceType, &evidence.ActualReferenceID,
		); err != nil {
			return nil, 0, fmt.Errorf("读取充值账务引用差异: %w", err)
		}
		evidence.ExpectedReferenceID = intentID
		encoded, err := json.Marshal(evidence)
		if err != nil {
			return nil, 0, fmt.Errorf("编码充值账务引用差异: %w", err)
		}
		findings = append(findings, finding{
			RuleCode: ruleDepositLedgerMismatch, Severity: SeverityCritical,
			ResourceType: "deposit_intent", ResourceID: intentID,
			AssetID: assetID, Evidence: encoded,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("遍历充值账务引用差异: %w", err)
	}
	return findings, checked, nil
}

func detectDepositLedgerSemanticMismatches(ctx context.Context, transaction pgx.Tx) ([]finding, int64, error) {
	var checked int64
	if err := transaction.QueryRow(ctx, `
		SELECT COUNT(*) FROM deposit_intents WHERE ledger_transaction_id IS NOT NULL
	`).Scan(&checked); err != nil {
		return nil, 0, fmt.Errorf("统计充值账务语义检查项: %w", err)
	}
	rows, err := transaction.Query(ctx, `
		SELECT intent.id::TEXT, intent.asset_id, intent.ledger_transaction_id::TEXT,
		       intent.credited_amount::TEXT, COUNT(entry.transaction_id),
		       COALESCE(SUM(entry.amount) FILTER (WHERE entry.side = 'D'), 0)::TEXT,
		       COALESCE(SUM(entry.amount) FILTER (WHERE entry.side = 'C'), 0)::TEXT,
		       COUNT(*) FILTER (
		           WHERE entry.side = 'D' AND entry.amount = intent.credited_amount
		             AND entry.asset_id = intent.asset_id
		             AND account.owner_type = 'platform' AND account.owner_id = 'gateway'
		             AND account.code = 'custody' AND account.normal_side = 'D'
		       ),
		       COUNT(*) FILTER (
		           WHERE entry.side = 'C' AND entry.amount = intent.credited_amount
		             AND entry.asset_id = intent.asset_id
		             AND account.owner_type = 'merchant'
		             AND account.owner_id = intent.merchant_id::TEXT
		             AND account.code = 'available' AND account.normal_side = 'C'
		       )
		FROM deposit_intents AS intent
		LEFT JOIN journal_entries AS entry ON entry.transaction_id = intent.ledger_transaction_id
		LEFT JOIN ledger_accounts AS account ON account.id = entry.account_id
		WHERE intent.ledger_transaction_id IS NOT NULL
		GROUP BY intent.id, intent.asset_id, intent.merchant_id,
		         intent.ledger_transaction_id, intent.credited_amount
		HAVING COUNT(entry.transaction_id) <> 2
		    OR COALESCE(SUM(entry.amount) FILTER (WHERE entry.side = 'D'), 0)
		       IS DISTINCT FROM intent.credited_amount
		    OR COALESCE(SUM(entry.amount) FILTER (WHERE entry.side = 'C'), 0)
		       IS DISTINCT FROM intent.credited_amount
		    OR COUNT(*) FILTER (
		           WHERE entry.side = 'D' AND entry.amount = intent.credited_amount
		             AND entry.asset_id = intent.asset_id
		             AND account.owner_type = 'platform' AND account.owner_id = 'gateway'
		             AND account.code = 'custody' AND account.normal_side = 'D'
		       ) <> 1
		    OR COUNT(*) FILTER (
		           WHERE entry.side = 'C' AND entry.amount = intent.credited_amount
		             AND entry.asset_id = intent.asset_id
		             AND account.owner_type = 'merchant'
		             AND account.owner_id = intent.merchant_id::TEXT
		             AND account.code = 'available' AND account.normal_side = 'C'
		       ) <> 1
		ORDER BY intent.id
	`)
	if err != nil {
		return nil, 0, fmt.Errorf("检测充值账务语义: %w", err)
	}
	defer rows.Close()
	findings := make([]finding, 0)
	for rows.Next() {
		var intentID, assetID string
		var evidence semanticEvidence
		if err := rows.Scan(
			&intentID, &assetID, &evidence.LedgerTransactionID, &evidence.ExpectedAmount,
			&evidence.EntryCount, &evidence.DebitTotal, &evidence.CreditTotal,
			&evidence.MatchingDebitEntries, &evidence.MatchingCreditEntries,
		); err != nil {
			return nil, 0, fmt.Errorf("读取充值账务语义差异: %w", err)
		}
		encoded, err := json.Marshal(evidence)
		if err != nil {
			return nil, 0, fmt.Errorf("编码充值账务语义差异: %w", err)
		}
		findings = append(findings, finding{
			RuleCode: ruleDepositLedgerSemanticMismatch, Severity: SeverityCritical,
			ResourceType: "deposit_intent", ResourceID: intentID,
			AssetID: assetID, Evidence: encoded,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("遍历充值账务语义差异: %w", err)
	}
	return findings, checked, nil
}

func detectPayoutLedgerMismatches(ctx context.Context, transaction pgx.Tx) ([]finding, int64, error) {
	const payoutLinks = `
		WITH payout_links AS (
			SELECT payout.id, payout.asset_id, link.kind, link.transaction_id,
			       link.expected_reference_type
			FROM payouts AS payout
			CROSS JOIN LATERAL (
				VALUES
					('freeze', payout.freeze_transaction_id, 'payout_freeze'::TEXT),
					('release', payout.unfreeze_transaction_id,
						CASE payout.status
							WHEN 'rejected' THEN 'payout_unfreeze'
							WHEN 'failed' THEN 'payout_failure_release'
						END),
					('settlement', payout.settlement_transaction_id,
						CASE WHEN payout.status = 'succeeded' THEN 'payout_settlement' END)
			) AS link(kind, transaction_id, expected_reference_type)
			WHERE link.transaction_id IS NOT NULL
			  AND link.expected_reference_type IS NOT NULL
		)
`
	var checked int64
	if err := transaction.QueryRow(ctx, payoutLinks+`
		SELECT COUNT(*) FROM payout_links
	`).Scan(&checked); err != nil {
		return nil, 0, fmt.Errorf("统计出款账务引用: %w", err)
	}
	rows, err := transaction.Query(ctx, payoutLinks+`
		SELECT link.id::TEXT, link.asset_id, link.kind, link.transaction_id::TEXT,
		       link.expected_reference_type, COALESCE(journal.status, 'missing'),
		       COALESCE(journal.reference_type, ''), COALESCE(journal.reference_id, '')
		FROM payout_links AS link
		LEFT JOIN journal_transactions AS journal ON journal.id = link.transaction_id
		WHERE journal.id IS NULL
		   OR journal.status IS DISTINCT FROM 'posted'
		   OR journal.reference_type IS DISTINCT FROM link.expected_reference_type
		   OR journal.reference_id IS DISTINCT FROM link.id::TEXT
		ORDER BY link.id, link.kind
	`)
	if err != nil {
		return nil, 0, fmt.Errorf("检测出款账务引用: %w", err)
	}
	defer rows.Close()
	findings := make([]finding, 0)
	for rows.Next() {
		var payoutID, assetID, linkKind string
		evidence := referenceEvidence{ExpectedStatus: "posted"}
		if err := rows.Scan(
			&payoutID, &assetID, &linkKind, &evidence.LedgerTransactionID,
			&evidence.ExpectedReferenceType, &evidence.ActualStatus,
			&evidence.ActualReferenceType, &evidence.ActualReferenceID,
		); err != nil {
			return nil, 0, fmt.Errorf("读取出款账务引用差异: %w", err)
		}
		evidence.ExpectedReferenceID = payoutID
		encoded, err := json.Marshal(evidence)
		if err != nil {
			return nil, 0, fmt.Errorf("编码出款账务引用差异: %w", err)
		}
		findings = append(findings, finding{
			RuleCode: rulePayoutLedgerMismatch, Severity: SeverityCritical,
			ResourceType: "payout_ledger_link", ResourceID: payoutID + ":" + linkKind,
			AssetID: assetID, Evidence: encoded,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("遍历出款账务引用差异: %w", err)
	}
	return findings, checked, nil
}

func detectPayoutLedgerSemanticMismatches(ctx context.Context, transaction pgx.Tx) ([]finding, int64, error) {
	const payoutSemanticLinks = `
		WITH payout_links AS (
			SELECT payout.id, payout.asset_id, payout.amount, link.*
			FROM payouts AS payout
			CROSS JOIN LATERAL (
				VALUES
					(
						'freeze', payout.freeze_transaction_id,
						'merchant'::TEXT, payout.merchant_id::TEXT, 'available'::TEXT, 'C'::TEXT,
						'merchant'::TEXT, payout.merchant_id::TEXT, 'frozen'::TEXT, 'C'::TEXT
					),
					(
						'release', payout.unfreeze_transaction_id,
						'merchant'::TEXT, payout.merchant_id::TEXT, 'frozen'::TEXT, 'C'::TEXT,
						'merchant'::TEXT, payout.merchant_id::TEXT, 'available'::TEXT, 'C'::TEXT
					),
					(
						'settlement', payout.settlement_transaction_id,
						'merchant'::TEXT, payout.merchant_id::TEXT, 'frozen'::TEXT, 'C'::TEXT,
						'platform'::TEXT, 'gateway'::TEXT, 'custody'::TEXT, 'D'::TEXT
					)
			) AS link(
				kind, transaction_id,
				debit_owner_type, debit_owner_id, debit_code, debit_normal_side,
				credit_owner_type, credit_owner_id, credit_code, credit_normal_side
			)
			WHERE link.transaction_id IS NOT NULL
		)
`
	var checked int64
	if err := transaction.QueryRow(ctx, payoutSemanticLinks+`
		SELECT COUNT(*) FROM payout_links
	`).Scan(&checked); err != nil {
		return nil, 0, fmt.Errorf("统计出款账务语义检查项: %w", err)
	}
	rows, err := transaction.Query(ctx, payoutSemanticLinks+`
		SELECT link.id::TEXT, link.asset_id, link.kind, link.transaction_id::TEXT,
		       link.amount::TEXT, COUNT(entry.transaction_id),
		       COALESCE(SUM(entry.amount) FILTER (WHERE entry.side = 'D'), 0)::TEXT,
		       COALESCE(SUM(entry.amount) FILTER (WHERE entry.side = 'C'), 0)::TEXT,
		       COUNT(*) FILTER (
		           WHERE entry.side = 'D' AND entry.amount = link.amount
		             AND entry.asset_id = link.asset_id
		             AND account.owner_type = link.debit_owner_type
		             AND account.owner_id = link.debit_owner_id
		             AND account.code = link.debit_code
		             AND account.normal_side = link.debit_normal_side
		       ),
		       COUNT(*) FILTER (
		           WHERE entry.side = 'C' AND entry.amount = link.amount
		             AND entry.asset_id = link.asset_id
		             AND account.owner_type = link.credit_owner_type
		             AND account.owner_id = link.credit_owner_id
		             AND account.code = link.credit_code
		             AND account.normal_side = link.credit_normal_side
		       )
		FROM payout_links AS link
		LEFT JOIN journal_entries AS entry ON entry.transaction_id = link.transaction_id
		LEFT JOIN ledger_accounts AS account ON account.id = entry.account_id
		GROUP BY link.id, link.asset_id, link.kind, link.transaction_id, link.amount,
		         link.debit_owner_type, link.debit_owner_id, link.debit_code, link.debit_normal_side,
		         link.credit_owner_type, link.credit_owner_id, link.credit_code, link.credit_normal_side
		HAVING COUNT(entry.transaction_id) <> 2
		    OR COALESCE(SUM(entry.amount) FILTER (WHERE entry.side = 'D'), 0)
		       IS DISTINCT FROM link.amount
		    OR COALESCE(SUM(entry.amount) FILTER (WHERE entry.side = 'C'), 0)
		       IS DISTINCT FROM link.amount
		    OR COUNT(*) FILTER (
		           WHERE entry.side = 'D' AND entry.amount = link.amount
		             AND entry.asset_id = link.asset_id
		             AND account.owner_type = link.debit_owner_type
		             AND account.owner_id = link.debit_owner_id
		             AND account.code = link.debit_code
		             AND account.normal_side = link.debit_normal_side
		       ) <> 1
		    OR COUNT(*) FILTER (
		           WHERE entry.side = 'C' AND entry.amount = link.amount
		             AND entry.asset_id = link.asset_id
		             AND account.owner_type = link.credit_owner_type
		             AND account.owner_id = link.credit_owner_id
		             AND account.code = link.credit_code
		             AND account.normal_side = link.credit_normal_side
		       ) <> 1
		ORDER BY link.id, link.kind
	`)
	if err != nil {
		return nil, 0, fmt.Errorf("检测出款账务语义: %w", err)
	}
	defer rows.Close()
	findings := make([]finding, 0)
	for rows.Next() {
		var payoutID, assetID, linkKind string
		var evidence semanticEvidence
		if err := rows.Scan(
			&payoutID, &assetID, &linkKind, &evidence.LedgerTransactionID,
			&evidence.ExpectedAmount, &evidence.EntryCount,
			&evidence.DebitTotal, &evidence.CreditTotal,
			&evidence.MatchingDebitEntries, &evidence.MatchingCreditEntries,
		); err != nil {
			return nil, 0, fmt.Errorf("读取出款账务语义差异: %w", err)
		}
		encoded, err := json.Marshal(evidence)
		if err != nil {
			return nil, 0, fmt.Errorf("编码出款账务语义差异: %w", err)
		}
		findings = append(findings, finding{
			RuleCode: rulePayoutLedgerSemanticMismatch, Severity: SeverityCritical,
			ResourceType: "payout_ledger_link", ResourceID: payoutID + ":" + linkKind,
			AssetID: assetID, Evidence: encoded,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("遍历出款账务语义差异: %w", err)
	}
	return findings, checked, nil
}
