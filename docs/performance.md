# Performance Baseline

GROK-GO keeps request coordination in Go and Redis, and avoids synchronous
PostgreSQL writes on the steady successful request path. Request logs use a
bounded asynchronous worker queue, and proxy-specific HTTP transports are
reused through a bounded LRU. Account state is persisted when health, cooldown,
quota, or credentials change.

Run the built-in parallel microbenchmarks with:

```bash
go test -run '^$' -bench . -benchmem -benchtime=2s \
  ./internal/accounts ./internal/gateway
```

The benchmarks cover:

- concurrent account acquire/release without an external coordinator;
- a complete non-streaming Chat Completions handler invocation with an in-memory
  account store and deterministic mock upstream.

Example development baseline on Windows amd64, Go 1.26.5, Ryzen 9 5900X:

| Benchmark | Time | Allocations |
| --- | ---: | ---: |
| Account acquire/release | 1.01 us/op | 824 B/op, 4 allocs/op |
| Chat Completions handler | 17.0 us/op | 14.4 KB/op, 128 allocs/op |

These numbers isolate gateway overhead. They do not predict end-to-end xAI
latency or replace deployment load tests with PostgreSQL, Redis, TLS, proxies,
streaming clients, and realistic response sizes. Compare changes on the same
machine and toolchain.

Prompt-cache identity generation, event normalization, Imagine processing, and
video-job updates remain in memory on the data path. They do not add a
synchronous PostgreSQL lookup to a successful request. Redis coordination is
still part of account acquisition, downstream quota enforcement, distributed
refresh locking, and cache/account affinity in a multi-instance deployment.

Request-log persistence is fail-open for gateway throughput: a saturated
bounded queue drops and counts new log records, then reports the count during
shutdown. Remote media bytes are streamed into a same-directory temporary file
without holding the store mutex; the lock covers only capacity eviction and the
atomic publish step.
