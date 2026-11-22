BEGIN;

CREATE TABLE IF NOT EXISTS router.organization_credit_ledger_unfunded_archive
    (LIKE router.organization_credit_ledger INCLUDING ALL);

INSERT INTO router.organization_credit_ledger_unfunded_archive
SELECT ledger.*
FROM router.organization_credit_ledger AS ledger
WHERE ledger.entry_type = 'unfunded'
  AND NOT EXISTS (
      SELECT 1
      FROM router.organization_credit_ledger_unfunded_archive AS archived
      WHERE archived.id = ledger.id
  );

DELETE FROM router.organization_credit_ledger
WHERE entry_type = 'unfunded';

ALTER TABLE router.organization_credit_ledger
    DROP CONSTRAINT organization_credit_ledger_entry_type_check,
    ADD CONSTRAINT organization_credit_ledger_entry_type_check
    CHECK (entry_type IN ('topup','inference','refund','adjustment','byok_fee'));

COMMIT;
