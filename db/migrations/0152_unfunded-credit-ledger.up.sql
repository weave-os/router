BEGIN;

ALTER TABLE router.organization_credit_ledger
    DROP CONSTRAINT organization_credit_ledger_entry_type_check,
    ADD CONSTRAINT organization_credit_ledger_entry_type_check
    CHECK (entry_type IN ('topup','inference','refund','adjustment','byok_fee','unfunded'));

COMMIT;
