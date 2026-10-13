# PayGuard — API and decision design

PayGuard is a risk API for fintech organizations: payment service providers, banks, wallets, and merchant platforms. The caller sends a payment intent. PayGuard returns an action and the evidence behind it. The caller enforces that action in its own payment flow.

PayGuard does not hold customer balances, does not submit transactions to a scheme or to UPI, and does not replace the caller's fraud operations team. It is the decision and audit layer those systems call.

This release has no machine-learning model. Rules are versioned, replayable, and bounded so a bad deploy cannot take down a caller's authorization rate.

Stack: Go, gRPC, Redis, PostgreSQL, Kubernetes. An HTTPS gateway exposes the same contract to callers that do not use gRPC.

---

## Who calls the API

| Caller | What they send | What they do with the result |
| --- | --- | --- |
| PSP or payment gateway | Intent before authorization | Allow, challenge, queue, or decline |
| Bank or UPI app backend | Debit before the debit is sent | Step-up, hold for review, or stop |
| Wallet or merchant platform | Payout or peer transfer | Same action set, their own UX |

Authentication to PayGuard is the organization's API credential, separate from the end-user device signature. Tenant isolation is mandatory: features, lists, rules, and audit rows never cross organization boundaries.

---

## Problem

A large share of payment fraud is technically valid. The user authenticated, the signature checks out, and the balance is sufficient. The payment is still fraud.

Two cases drive the first release. A single velocity counter does not cover both.

**Mule and layering.** An account is quiet for weeks. Within minutes it receives funds from many senders and forwards most of that value to new beneficiaries, often across several hops. That is the pattern networks such as NPCI describe for mule accounts: fan-in, then fan-out. "This user made five payments today" does not describe it.

**Device and session risk.** The request itself is wrong: a new phone, a SIM change, an emulator, an accessibility service used for screen sharing, a datacenter IP running card tests, or a script that completes checkout in a few hundred milliseconds. This is not Ticketmaster-style bot defense. That problem is inventory scraping. Here the question is whether this authenticated payment should move money. Often a person is entering the PIN because someone on a phone call is coaching them.

A third constraint comes from production risk stacks (Razorpay's risk-based authentication, Adyen Protect, Stripe Radar): declining every risky payment destroys authorization rate. The API must be able to say "challenge" or "review" without saying "decline".

---

## API actions

`ScorePayment` returns one action. The score is supporting data, not a substitute for the action.

| Action | Meaning for the caller |
| --- | --- |
| `ALLOW` | Continue the payment |
| `STEP_UP` | Require an extra confirmation (re-enter PIN, show the beneficiary name, or apply a short cooldown). Do not kill the payment |
| `REVIEW` | Hold for a person, with a TTL. If the TTL expires with no label, the caller's configured fail-safe applies: allow or block |
| `BLOCK` | Do not send the payment |

Each response includes:

- a numeric score, for ordering and analytics only
- fired rules: id, version, action, reason code
- the feature values those rules evaluated
- the idempotency key. A retry of the same intent returns the same decision and does not double-count features

Two enforcement modes, chosen per organization:

- **enforcing** — the caller is expected to honor the action
- **shadow** — PayGuard logs the action and the caller proceeds. Used for the first days of a new tenant or a new rule

---

## Two evaluation points

**Pre-debit, synchronous.** On the caller's payment path. Inputs: payer velocity, device binding, new-beneficiary status, amount against baseline, SIM and integrity signals. Latency budget: p95 under 40 ms for the whole `ScorePayment` call. This call may return any of the four actions.

**Post-pattern, asynchronous.** After the caller reports the outcome. PayGuard updates windows and edges: who paid whom, and how fast funds left. That evidence applies to the next debit. The first request is not held open while the graph updates. A pre-debit `BLOCK` is never overturned by a later async pass. Other actions may become stricter on the next call.

Callers must send an outcome (`completed`, `failed`, `step_up_passed`, `step_up_failed`, `review_fraud`, `review_legit`). Without outcomes, replay and false-positive rates are empty.

---

## Features

### Money flow

- Account age, and days since the last successful payment (dormant gap)
- Inbound fan-in: distinct payers in 10 minutes and in 1 hour, count and amount
- Outbound fan-out: distinct beneficiaries in 10 minutes and in 1 hour
- Pass-through: share of inbound value in the last 30 minutes that has already left
- Beneficiary first-seen age. First payment to this VPA, above a tenant threshold
- Hop check: beneficiary is on the tenant's `BAD` list, or within one or two hops of an entity that is. Edges live in Redis. There is no general-purpose graph database in this release

### Payer baseline

- Usual amount (median, trailing 30 days)
- Usual hours
- Beneficiaries this payer has already paid

If the payer is new, deviation rules do not fire. Dormant-gap and new-beneficiary rules still do.

### Device binding

Binding is the device identity. The fingerprint is not.

A binding record:

- `user_id` (the caller's user id, scoped to the tenant)
- `device_id` — stable id generated by the app
- `app_instance_id` — this install. A reinstall mints a new one
- `sim_hash` — hash of the SIM identity. Raw ICCID is not stored
- `public_key` — the private key stays in device secure hardware (Android Keystore or iOS Secure Enclave)
- `bound_at`
- `state`: `ACTIVE`, `COOLING`, or `REVOKED`

Flow:

1. First bind happens after the caller's own step-up (OTP or PIN). PayGuard stores the public key.
2. Every `ScorePayment` request is signed over user, amount, beneficiary, timestamp, and nonce. The private key never leaves the device.
3. PayGuard verifies the signature. Failure yields `BLOCK`.
4. A changed SIM hash or a new `app_instance_id` moves the binding to `COOLING` (default 24 hours). During cooling, a new beneficiary above the amount threshold yields `STEP_UP` or `REVIEW`.
5. "Sign out all devices" from the caller sets every binding for that user to `REVOKED`.

### Fingerprint and integrity

The fingerprint describes the environment. It does not prove "this is the same phone" across OS updates, so fingerprint drift alone never returns `BLOCK`.

Signals the caller forwards from its app:

- `fp_hash` — hash of model, OS, app version, screen, timezone, and language. No raw IMEI.
- `fp_drift` — `SAME`, `DRIFT`, or `NEW` against the last successful payment
- `integrity` — server-verified Play Integrity or App Attest result: `PASS`, `FAIL`, or `UNKNOWN`. A client string that says "not rooted" is not a pass
- `emulator`, `rooted`
- `accessibility_enabled` — screen-share and remote-control class signal
- installer source: store or sideload

`integrity = FAIL` or an emulator yields `BLOCK`. Accessibility enabled plus a new beneficiary yields `STEP_UP`, and `REVIEW` when the amount is also high.

### Network and session

- IP, country, region
- ASN class: `RESIDENTIAL`, `DATACENTER`, `VPN`, `TOR`, or `UNKNOWN`
- Distinct users on this IP in the last hour (inside the tenant)
- Whether geo matches the payer's usual region
- TLS fingerprint (JA3 or JA4) when the caller's edge can supply it. A claimed Android app whose handshake looks like a browser automation stack is a flag
- `session_ms` — start of checkout to pay
- `pin_entry_ms`

Datacenter or Tor plus a burst of small attempts is card testing and yields `BLOCK`. A session under 300 ms with a new beneficiary is treated as a script. A very slow PIN entry with long pauses, together with a new beneficiary, is treated as possible remote assistance and yields `STEP_UP`.

On web checkout, not the mobile app, PayGuard also accepts a `webdriver` or headless flag. On a UPI-style mobile app those browser signals are weak. Binding and integrity carry that path.

### Lists

Exact match on VPA, phone hash, and device id.

List class: `BAD`, `GREY`, or `TRUSTED`.

`BAD` at pre-debit yields `BLOCK` or `REVIEW`, per list configuration. A `TRUSTED` beneficiary (for example a known payroll VPA) may suppress the mule fan-out rule. It cannot suppress signature failure, integrity failure, or a `BAD` hit. Fuzzy name similarity is returned on the review record only. It never auto-blocks.

---

## Rule lifecycle

Rules are YAML, and they are pure: features in, action out. A rule does not write to Redis or Postgres.

| State | Effect |
| --- | --- |
| `SHADOW` | Compute the action, do not return it as the enforced action. The audit row records what would have happened |
| `CANARY` | Enforce on a small share of traffic, with a block-rate cap |
| `LIVE` | Enforce for the tenant |
| `OFF` | Not evaluated |

Every evaluation stores inputs, outputs, and the rule version. From that log an operator can:

- count firings
- join analyst labels (`review_fraud`, `review_legit`) to estimate false positives
- replay a candidate YAML against historical payments and see how many extra `BLOCK` and `STEP_UP` results it would have produced

**Blast radius.** A rule may not return more than N `BLOCK` actions in five minutes, per tenant. Past the cap, the rule drops to `SHADOW` and an alert fires. One bad threshold must not halt a caller's payments.

On the pre-debit path, only features that fit the latency budget are read. A multi-hop walk that is too slow is limited, at pre-debit, to a direct `BAD` hit. Deeper hops are computed asynchronously and apply on the next call.

---

## Initial rule set

These thresholds are defaults, overridable per tenant. All of them start in `SHADOW`.

| Id | Condition | Action |
| --- | --- | --- |
| `R1` | Signature invalid, or binding `REVOKED` | BLOCK |
| `R2` | Integrity `FAIL`, or emulator | BLOCK |
| `R3` | Datacenter or Tor IP, and 8 or more small attempts in 10 minutes | BLOCK |
| `R4` | Session under 300 ms, and a new beneficiary | BLOCK |
| `R5` | New device or cooling, new VPA, amount above 3× baseline | STEP_UP |
| `R6` | Accessibility enabled, and a new VPA | STEP_UP |
| `R7` | Dormant more than 30 days, then 5 or more inbound payers in 10 minutes | STEP_UP |
| `R8` | Pass-through above 80%, and 3 or more new outbound VPAs within 15 minutes | REVIEW |
| `R9` | Beneficiary on the `BAD` list | BLOCK |
| `R10` | Beneficiary is one hop from a `BAD` entity, amount above threshold | REVIEW |

The strictest action wins: `BLOCK` over `REVIEW` over `STEP_UP` over `ALLOW`. A `TRUSTED` list entry may skip `R8`. It may not skip `R1`, `R2`, or `R9`.

---

## Reference scenario

This is the first integration test. It is also the scenario used in the API docs.

1. The account has been idle for 40 days.
2. Within 6 minutes, eight distinct payers send between 2,000 and 5,000 (currency as provided by the caller).
3. Ninety percent of that value is forwarded to four new VPAs.
4. The first forward: binding is `ACTIVE`, but the beneficiary is new and the account is dormant. `R7` and `R5` yield `STEP_UP`.
5. The caller reports `step_up_passed`. The payment completes. Async ingest updates fan-in and pass-through.
6. On the third forward, `R8` matches. If `R8` is `LIVE`, the action is `REVIEW`. If `R8` is `SHADOW`, the enforced action stays whatever the live rules say, and the audit row records that `R8` would have reviewed.
7. The decision row contains feature values, rule ids, and binding state. A replay with `R8` set to `OFF` shows the third forward no longer reviewing because of that rule.

---

## Services

```
Caller
    |  ScorePayment (gRPC or HTTPS)
    |  API credential + signed device context + intent
    v
Decision API                 hot path, p95 < 40 ms
    |- authenticate tenant
    |- verify device binding
    |- read features (Redis)
    |- evaluate rules (in-process, versioned YAML)
    |- return action
    '- append audit asynchronously (not on the critical path)

Redis:    windows, first-seen, edges, per-rule block budget
Postgres: tenants, bindings, lists, baselines, rule versions,
          decisions, outcomes, review labels

Ingest (async)
    caller outcome events
    -> update windows, edges, baselines

Review API
    list REVIEW items, accept a label, recompute false-positive counts
```

Feature computation and rule evaluation are separate packages so replay can run without the decision server.

---

## Request and response, conceptual

`ScorePayment` request:

- `tenant_id` (from the credential, not from the body alone)
- `idempotency_key`
- `intent`: payer id, amount, currency, beneficiary id, channel (`UPI`, `CARD`, `WALLET`, `PAYOUT`)
- `device`: binding signature, device id, instance id, sim hash match, fingerprint, integrity verdict, emulator, rooted, accessibility, installer
- `network`: IP, ASN class, geo, TLS fingerprint if present
- `session`: session duration, PIN duration

Response:

- `action`
- `score`
- `rules[]` with id, version, action, reason
- `features` that were actually read
- `decision_id` for the later outcome call

`ReportOutcome` carries `decision_id` and one of `completed`, `failed`, `step_up_passed`, `step_up_failed`, `review_fraud`, `review_legit`.

Field-level protobuf definitions land in the next change. This document is the contract those definitions must follow.

---

## Data PayGuard does not store

- Raw ICCID, IMEI, MAC address, contacts, or SMS content
- Device private keys
- Full card numbers or government id numbers. Only the identifier the caller already uses (token, VPA, or a hash)

A self-reported "not rooted" flag with no attestation is stored as `UNKNOWN`, never as `PASS`.

---

## Out of scope for this release

- A trained model or a training pipeline
- A general graph database or a graph neural network
- Merchant-website review
- Speaking 3-D Secure or a bank ACS. The API returns `STEP_UP`. The caller runs the challenge
- Queue tokens, CAPTCHA, or proof-of-work
- Moving, settling, or reconciling funds

---

## Build order

1. Protobuf for `ScorePayment`, `ReportOutcome`, `DeviceContext`, and `Binding`
2. Redis features: velocity, first-seen, fan-in, fan-out, pass-through
3. Binding verification and cooling
4. YAML rules, default `SHADOW`, block-budget cap
5. Audit log and a replay command
6. Integration test for the reference scenario above
7. Kubernetes manifests, traces, and a per-tenant block-rate metric

No further feature work until step 6 is green.
