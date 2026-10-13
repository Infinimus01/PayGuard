# PayGuard

PayGuard is a real-time payment risk API. A fintech, bank, or payment service provider sends a payment intent and receives an explainable decision before money moves: `ALLOW`, `STEP_UP`, `REVIEW`, or `BLOCK`.

The API is built for organizations that already run payments and need a risk layer they can call, rather than a demo ruleset embedded in one app. Callers keep their own user store, ledger, and checkout. PayGuard scores the intent, records why, and returns an action the caller enforces.

This version does not use machine learning. Decisions come from versioned rules over real-time features: money-flow patterns, device binding, and network and session signals. Every decision is auditable.

Full specification: [DESIGN.md](DESIGN.md).

---

## What the API covers

**Money-flow risk.** Dormant accounts that suddenly receive funds from many senders and forward them to new beneficiaries. Fan-in, fan-out, pass-through ratio, beneficiary first-seen age, and a one- to two-hop check against known-bad entities.

**Device binding.** Each payment is signed by a key held in device secure hardware. SIM change or reinstall moves the binding to a cooling state. An invalid signature or a revoked binding is a block.

**Device, integrity, and network signals.** Fingerprint drift, emulator detection, screen-share and accessibility abuse, Play Integrity or App Attest results, datacenter, VPN, and Tor traffic, and session timing. A fingerprint mismatch alone does not block a payment. Attestation failure does.

**Decisioning.** Four actions, not a bare score. A synchronous pre-debit call on the payment path (p95 under 40 ms) and an asynchronous update after the payment settles. Idempotent: the same payment intent returns the same decision.

**Rule operations.** Rules ship as `SHADOW`, then `CANARY`, then `LIVE`. Each firing stores inputs and outputs. Operators can replay a rule against historical traffic, measure false positives from analyst labels, and cap how many blocks one rule may issue in a short window.

**Lists.** Exact-match `BAD`, `GREY`, and `TRUSTED` lists for VPAs, phone hashes, and device identifiers. Fuzzy name matches are exposed to reviewers only. They never auto-block.

---

## Integration

```
Caller (PSP, bank, wallet, checkout)
        |
        |  ScorePayment  (gRPC or HTTPS gateway)
        |  signed device context + payment intent
        v
PayGuard Decision API
        |
        v
ALLOW | STEP_UP | REVIEW | BLOCK
        + reason codes, rule ids, feature values

Caller enforces the action.
Caller reports the outcome (completed, failed, reviewed).
```

The caller enforces the action. PayGuard does not move money, does not render a bank authentication screen, and does not replace the caller's ledger.

| Your system | PayGuard |
| --- | --- |
| User accounts, KYC, balances | Risk decision and audit |
| Authorize and capture | `STEP_UP` or `REVIEW` as an instruction |
| Checkout UI | Device context and binding signatures, verified server-side |
| Chargebacks and manual review outcomes | Labels that feed false-positive rates and replay |

---

## Stack

| Layer | Choice |
| --- | --- |
| Language | Go |
| API | gRPC, with an HTTPS gateway for callers who do not run gRPC |
| Hot features | Redis |
| Durable state | PostgreSQL |
| Observability | OpenTelemetry, Prometheus |
| Deploy | Kubernetes |

---

## Status

The API contract and decision model are specified in [DESIGN.md](DESIGN.md). Implementation has not started. The first milestone is an integration test of the mule-account scenario, with rules in `SHADOW`.

---

## Principles

- The pre-debit call stays on the payment latency budget: p95 under 40 ms.
- Every decision must be explainable to a reviewer and to the calling organization.
- `BLOCK` is the last resort. `STEP_UP` and `REVIEW` exist so callers can protect conversion.
- Client-reported device claims are untrusted until a signature or an attestation says otherwise.
- One bad rule must not be able to halt a caller's traffic. Block budgets force a rule back to `SHADOW`.

---

## License

MIT
