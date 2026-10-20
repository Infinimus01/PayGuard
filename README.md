# PayGuard

PayGuard is a real-time payment risk API. A fintech, bank, or payment service provider sends a payment intent and receives an explainable decision before money moves: `ALLOW`, `STEP_UP`, `REVIEW`, or `BLOCK`.

The caller keeps the app, the ledger, and the checkout. PayGuard scores the intent, records why, and returns an action the caller enforces. If PayGuard does not answer in time, the caller fails open and continues the payment.

v1 decisions are rules, not a model. Every score stores the feature vector exactly as it was scored. Chargebacks, confirmed scams, and reviewer marks attach a label to that vector later. A model can be trained from those rows offline. It is not on the payment path until it has beaten the rules in shadow. A completed payment is not treated as legitimate, and a failed payment is not treated as fraud.

Full specification: [DESIGN.md](DESIGN.md).

---

## What the API covers

**Money-flow risk.** Dormant accounts that suddenly receive funds from many senders and forward them to new beneficiaries. Fan-in, fan-out, pass-through ratio, beneficiary first-seen age, and a one- to two-hop check against known-bad entities.

**Device binding.** Each payment is signed by a key held in device secure hardware. SIM change or reinstall moves the binding to a cooling state. An invalid signature or a revoked binding is a block.

**Device, integrity, and network signals.** Fingerprint drift, emulator detection, screen-share and accessibility abuse, Play Integrity or App Attest results, datacenter, VPN, and Tor traffic, and session timing. A fingerprint mismatch alone does not block a payment. Attestation failure does.

**Decisioning.** Four actions, not a bare score. A synchronous pre-debit call on the payment path (p95 under 40 ms) and an asynchronous update after the payment settles. Idempotent: the same payment intent returns the same decision.

**Rule operations.** Rules ship as `SHADOW`, then `CANARY`, then `LIVE`. Thresholds change without a deploy. Operators can replay a rule against historical traffic. A per-rule block budget forces a runaway rule back to `SHADOW`.

**Lists.** Exact-match `BAD`, `GREY`, and `TRUSTED` lists. Fuzzy name matches never auto-block.

**A training set, not a live model.** Strong labels only: chargeback, confirmed scam, reviewer fraud, reviewer legit. Weak label: still clean after the dispute window. Everything else stays unlabeled.

---

## Integration

```
Caller (PSP, bank, wallet, checkout)
        |
        |  POST /v1/score
        |  API key + idempotency key + signed device context
        v
PayGuard
        |
        v
ALLOW | STEP_UP | REVIEW | BLOCK
        + reason codes, rule ids, feature values, decision_id

Caller enforces the action (or ALLOW, if the call times out).
Caller POST /v1/outcomes with that decision_id.
```

| Your system | PayGuard |
| --- | --- |
| User accounts, KYC, balances | Risk decision, audit, and the labeled feature row |
| Authorize and capture | `STEP_UP` or `REVIEW` as an instruction |
| Checkout and device keys | Signature and attestation checks |
| Chargebacks and manual review | Labels. Not a training step on the request |

One PostgreSQL database for every tenant, isolated by `tenant_id`. Redis holds short-lived counters only.

---

## Stack

| Layer | Choice |
| --- | --- |
| Language | Go |
| Public API | JSON, `POST /v1/score`, `POST /v1/outcomes` |
| Hot features | Redis |
| System of record | PostgreSQL (UUID keys, `tenant_id` on every row) |
| Observability | OpenTelemetry, Prometheus |
| Deploy | Kubernetes, one region |

---

## Status

The HTTP contract is in the repo: `POST /v1/score`, `POST /v1/outcomes`, tenant authentication, and idempotency. Decisions and outcomes persist in PostgreSQL when `DATABASE_URL` is set (`tenants`, `decisions`, `outcomes`). Rules, Redis, and the mule-account test are next. No model is served.

---

## Principles

- The pre-debit call stays on the payment latency budget: p95 under 40 ms.
- If this API is down, the caller's payments still move.
- Every decision must be explainable. A score alone is not a decision.
- `BLOCK` is the last resort.
- Client-reported device claims are untrusted until a signature or an attestation says otherwise.
- One bad rule must not be able to halt a caller's traffic.
- The model learns from labels that arrive later, in a batch, per tenant. It does not learn inside the score call.

---

## License

MIT
