BEGIN;

DROP INDEX router.subscriber_credit_ledger_action_id_uidx;

ALTER TABLE router.subscriber_credit_ledger
    DROP COLUMN capacity_source,
    DROP COLUMN action_id,
    DROP COLUMN authorization_action_id;

DROP TABLE router.subscriber_credit_reservations;

COMMIT;