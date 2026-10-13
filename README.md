# PayGuard

**Real-time transaction risk & anomaly detection engine for payment systems.**

PayGuard evaluates every payment before money moves. It returns an explainable decision — `APPROVE`, `REVIEW`, or `BLOCK` — along with the exact features and rules that drove the outcome. Designed to sit in the critical path of a payment flow with strict latency budgets.

Built for teams that need production-grade risk controls without the complexity of a full ML platform.

---

## Why this exists

Most open-source fraud demos stop at a few hardcoded velocity checks. Real payment systems need more:

- Consistent real-time features under concurrent load
- Behavioral baselines that actually reflect the user
- Lightweight graph signals to catch shared devices and simple collusion patterns
- Fully explainable decisions (required for compliance and dispute handling)
- A feedback loop so chargebacks and manual reviews improve the system over time
- Operational readiness: metrics, tracing, graceful degradation, Kubernetes deployment

PayGuard focuses on those problems.

---

## Core Capabilities

### Real-time Feature Engine
- Sliding-window velocity (count + amount) across 1 min / 5 min / 1 hour / 24 hour windows
- Unique entity counts (merchants, devices, IPs, locations) inside each window
- Per-user behavioral baselines: average amount, typical transaction hours, preferred merchants
- Time-since-last-transaction, new-device and new-IP flags, geographic jump detection
- Lightweight graph signals:
  - Shared device / phone / IP across users
  - Fan-out and fan-in patterns
  - Basic cycle detection (A → B → C → A)

### Configurable Rules Engine
- Versioned, hot-reloadable rules (JSON/YAML)
- Weighted scoring with severity levels (LOW / MEDIUM / HIGH / CRITICAL)
- Rich rule types covering velocity, amount deviation, entity uniqueness, graph signals, and sequence patterns
- Every decision returns the exact list of fired rules plus the feature values that triggered them

### Decision Service
- Final output: decision (`APPROVE` / `REVIEW` / `BLOCK`) + numeric risk score + full explanation
- Two operating modes:
  - **Blocking** — can stop the payment
  - **Non-blocking** — flag only (useful for gradual rollout)
- Idempotent scoring: the same transaction always produces the same decision

### Feedback Loop
- Ingest chargebacks, user-reported scams, and manual review outcomes
- Track false-positive rate per rule
- Enable / disable rules or adjust thresholds based on real outcomes
- Historical replay to back-test rule changes safely

### Operational Features
- gRPC interface for low-latency integration with payment services
- OpenTelemetry tracing and Prometheus metrics (latency, decision distribution, false-positive rate, feature computation time)
- Full audit log of every decision
- Graceful degradation when Redis or the feature store is under pressure
- Kubernetes-ready manifests (Deployments, HPA, readiness/liveness probes, resource limits)

---

## Architecture Overview

```
Payment Service
      │
      │  gRPC  (RiskScoreRequest)
      ▼
┌───────────────────────────────────┐
│           PayGuard                  │
│  ┌─────────────┐  ┌──────────────┐  │
│  │ Feature     │  │ Rules        │  │
│  │ Engine      │→ │ Engine       │  │
│  └─────────────┘  └──────────────┘  │
│         │                │          │
│         └───────┬────────┘          │
│                 ▼                   │
│          Decision Service           │
│                 │                   │
│                 ▼                   │
│          RiskScoreResponse          │
└─────────────────────────────────────┘
      │
      │  async feedback events
      ▼
 Feedback Worker  →  Rule adjustments + metrics
```

Services communicate over gRPC. Real-time features live in Redis. Baselines, rule versions, feedback, and the full audit trail live in PostgreSQL.

---

## Tech Stack

| Layer              | Choice                          |
|--------------------|---------------------------------|
| Language           | Go                              |
| Inter-service      | gRPC + Protocol Buffers         |
| Real-time features | Redis (sliding windows + sets)  |
| Persistent store   | PostgreSQL                      |
| Observability      | OpenTelemetry → Prometheus      |
| Deployment         | Kubernetes                      |

---

## Project Status

This repository is under active development. The current focus is:

1. Feature computation service with correct sliding-window semantics
2. Rules engine with hot reload and full explainability
3. gRPC decision endpoint
4. Feedback ingestion and false-positive tracking
5. Kubernetes manifests and observability

Daily progress is pushed to this repository.

---

## Design Principles

- **Latency first** — target p95 under 40 ms for a complete score
- **Explainability is non-negotiable** — every decision must be auditable
- **Features before models** — a solid feature and rules foundation comes first
- **Fail safe** — when in doubt, the system degrades gracefully instead of blocking legitimate traffic or going silent
- **Operationally honest** — metrics and tracing are part of the core design, not an afterthought

---

## License

MIT

---

Built for real payment systems. Feedback and contributions welcome.
