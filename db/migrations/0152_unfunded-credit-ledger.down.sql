BEGIN;

-- Narrowing the CHECK cannot retain unfunded rows. This rollback permanently
-- removes reconciliation records; archive them before rollback when retention
-- is required, matching the destructive narrowing in 0044_managed-byok.down.
DELETE FROM router.organization_credit_ledger
WHERE entry_type = 'unfunded';

ALTER TABLE router.organization_credit_ledger
    DROP CONSTRAINT organization_credit_ledger_entry_type_check,
    ADD CONSTRAINT organization_credit_ledger_entry_type_check
    CHECK (entry_type IN ('topup','inference','refund','adjustment','byok_fee'));

COMMIT;
