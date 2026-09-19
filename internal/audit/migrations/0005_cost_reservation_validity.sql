-- A price snapshot alone does not mean the request had an estimate: image
-- requests historically stored a zero reservation even with a nonzero tariff.
ALTER TABLE audit_costs ADD COLUMN reservation_valid BOOLEAN NOT NULL DEFAULT FALSE;

-- Preserve existing text reservations, including genuinely free tariffs.
-- Zero reservations without a provably free price remain unknown even after
-- their request logs have expired, so later enabling a budget fails closed.
UPDATE audit_costs SET reservation_valid = TRUE
WHERE amount_pico IS NOT NULL
   OR (price_id IS NOT NULL AND (
       reserved_pico > 0
       OR price_snapshot->'rates' @> '{"off_hit":0,"off_miss":0,"off_output":0,"peak_hit":0,"peak_miss":0,"peak_output":0}'::jsonb
   ));
