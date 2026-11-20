-- No-op: the deleted cluster pins and retired override keys have no live
-- meaning to restore; affected sessions re-route on their next turn.
BEGIN;
COMMIT;
