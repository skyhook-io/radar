The NATS fixtures contain five retained messages. Consumer `idle` has five
pending messages in `nats-backlog.json` and none in `nats-recovered.json`.

Unselected configuration and large unsigned counters are intentional: normalized
output must exclude them. Top-level `total` counts accounts; matching consumer
counts alone does not establish complete account and stream coverage.
