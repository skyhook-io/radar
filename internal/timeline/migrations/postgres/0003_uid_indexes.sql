-- Scoped history selects rows by subject UID and walks ownership by owner UID.
CREATE INDEX IF NOT EXISTS radar_timeline_events_uid_idx ON radar_timeline_events (uid);
CREATE INDEX IF NOT EXISTS radar_timeline_events_owner_uid_idx ON radar_timeline_events (owner_uid);
