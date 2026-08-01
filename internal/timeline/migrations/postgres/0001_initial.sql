CREATE SEQUENCE radar_timeline_event_seq;

CREATE TABLE radar_timeline_events (
    id                  text PRIMARY KEY,
    timestamp           timestamptz NOT NULL,
    source              text NOT NULL,
    kind                text NOT NULL,
    api_version         text,
    namespace           text,
    name                text NOT NULL,
    uid                 text,
    event_type          text NOT NULL,
    reason              text,
    message             text,
    diff_json           jsonb,
    health_state        text,
    owner_kind          text,
    owner_name          text,
    labels_json         jsonb,
    count               bigint NOT NULL DEFAULT 0,
    correlation_id      text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    cluster_context     text NOT NULL DEFAULT '',
    resource_created_at timestamptz,
    seq                 bigint NOT NULL DEFAULT nextval('radar_timeline_event_seq')
);

ALTER SEQUENCE radar_timeline_event_seq
    OWNED BY radar_timeline_events.seq;

CREATE INDEX radar_timeline_events_timestamp_idx
    ON radar_timeline_events (timestamp DESC);
CREATE INDEX radar_timeline_events_kind_idx
    ON radar_timeline_events (kind);
CREATE INDEX radar_timeline_events_namespace_idx
    ON radar_timeline_events (namespace);
CREATE INDEX radar_timeline_events_name_idx
    ON radar_timeline_events (name);
CREATE INDEX radar_timeline_events_source_idx
    ON radar_timeline_events (source);
CREATE INDEX radar_timeline_events_owner_idx
    ON radar_timeline_events (owner_kind, owner_name, namespace);
CREATE INDEX radar_timeline_events_kind_namespace_name_idx
    ON radar_timeline_events (kind, namespace, name);
CREATE INDEX radar_timeline_events_cluster_timestamp_idx
    ON radar_timeline_events (cluster_context, timestamp DESC);
CREATE INDEX radar_timeline_events_seq_idx
    ON radar_timeline_events (seq);

CREATE TABLE radar_timeline_seen_resources (
    resource_key bytea PRIMARY KEY,
    seen_at      timestamptz NOT NULL DEFAULT now()
);
