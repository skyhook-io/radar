The NATS fixtures contain five retained messages. Consumer `idle` has five
pending messages in `nats-backlog.json` and none in `nats-recovered.json`.

Unselected configuration and large unsigned counters are intentional: normalized
output must exclude them. Top-level `total` counts accounts; matching consumer
counts alone does not establish complete account and stream coverage.

`nats-delivered.json` and `nats-pull-waiting.json` were captured from official NATS 2.11.8 on 2026-11-03 using the same bounded `/jsz?streams=true&consumers=true&config=false&limit=20` request. Five synthetic messages were delivered without acknowledgements (ack floor0 / ack pending5), then acknowledged; the latter capture has one real waiting pull request. Configuration was not requested.

## RabbitMQ resource measurements

`rabbitmq-4.3.4-metrics.prom` is an unmodified excerpt from the default `/metrics` endpoint of an official RabbitMQ 4.3.4 Pod with the Prometheus plugin enabled, captured on 2026-11-03. It keeps the six metric families used by Radar and their original HELP/TYPE lines; unrelated families are omitted. Image digest: `sha256:4b336f82e93749f1ebf8d6283b4d5a98bf1efac8412ec56015a6ab5aae0f57a2`. No credentials, names or user traffic are present.

Metric mapping: [RabbitMQ core collector](https://github.com/rabbitmq/rabbitmq-server/blob/v4.3.4/deps/rabbitmq_prometheus/src/collectors/prometheus_rabbitmq_core_metrics_collector.erl). The upstream metric named `process_resident_memory_bytes` represents RabbitMQ's `mem_used`; its calculation follows RabbitMQ's configured memory strategy. Measurements are periodically refreshed statistics, while the [alarm collector](https://github.com/rabbitmq/rabbitmq-server/blob/v4.3.4/deps/rabbitmq_prometheus/src/collectors/prometheus_rabbitmq_alarm_metrics_collector.erl) reads current local alarms.
