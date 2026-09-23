-- The full owner reference and how it was determined. NULL means unknown: an
-- owner reference without those fields, and evidence that wasn't recorded.
ALTER TABLE radar_timeline_events
    ADD COLUMN IF NOT EXISTS owner_api_version text,
    ADD COLUMN IF NOT EXISTS owner_uid         text,
    ADD COLUMN IF NOT EXISTS owner_evidence    text;
