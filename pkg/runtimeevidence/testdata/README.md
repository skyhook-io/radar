NATS 2.11.8 `/jsz?streams=true&consumers=true&config=false&limit=20` responses captured from a disposable single-node fixture on `kind-radar-e2e`, 2026-09-21. The backlog fixture has five synthetic retained messages pending on consumer `idle`; the recovered fixture has the same retained messages after all five were acknowledged.

These are test-cluster responses, not customer data. They retain unselected source fields to test allowlisted output against the actual endpoint shape, including large unsigned counters in unrelated fields. `config=false` removes stream/consumer config, but the source still includes server config; that config must not enter the normalized result.

Source contract: https://github.com/nats-io/nats-server/blob/v2.11.8/server/monitor.go (`JSInfo`, `AccountDetail`, `Jsz`). The `total` field counts accounts; `limit` paginates accounts. Matching consumer counts alone does not establish complete detail coverage.
