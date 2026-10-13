# PayGuard — API and decision design

PayGuard is a risk API for fintech organizations: payment service providers, banks, wallets, and merchant platforms. The caller sends a payment intent. PayGuard returns an action and the evidence behind it. The caller enforces that action in its own payment flow.

PayGuard does not hold customer balances, does not submit transactions to a scheme or to UPI, and does not replace the caller's fraud operations team. It is the decision and audit layer those systems call.

v1 decisions are rules. Rules are versioned, replayable, and bounded so a bad deploy cannot take down a caller's authorization rate. Every score also writes a frozen feature vector. Real labels, which arrive later, attach to that vector. A model is trained from those rows offline. v1 does not serve a model, and it does not learn from a single request.

Stack: Go, Redis, PostgreSQL, Kubernetes. Customers call JSON over HTTPS. gRPC is internal.

---

## API contract (locked before code)

These choices are part of the product. They are not implementation details.

**Fail open by default.** If PayGuard does not answer within the caller's deadline (about 50 ms), the caller proceeds with `ALLOW` and reports the intent afterward. A tenant may opt into fail closed. If Redis is down, PayGuard itself returns `ALLOW` with reason `degraded` and records that the score was blind. A risk API that can halt a customer's authorization rate will not be adopted.

**Public API is versioned JSON.** `POST /v1/score` and `POST /v1/outcomes`. `/v1` is never broken once a tenant is live. gRPC mirrors the same fields inside the cluster.

**Tenant comes from the credential.** An API key maps to one `tenant_id`. A `tenant_id` in the body is ignored.

**Idempotency key is required.** The same key and the same intent return the stored decision and do not move counters. The same key with a different amount, beneficiary, or payer is `409`.

**Identifiers, not people.** VPA, device id, phone hash, the caller's user id, amount, currency. No name, PAN, card number, SMS, or raw SIM. PayGuard is a processor. The caller is the data fiduciary. Default retention for decisions and outcomes is 180 days, then delete. Redis counters live for hours. Baselines roll for 30 days.

**No mobile SDK in v1.** The caller signs in their app. PayGuard verifies the signature and the attestation result. The signed payload is documented byte for byte.

**Rules are rows.** Thresholds for `R1`–`R10` change without a deploy. New rule code still requires a release. There is no tenant-authored rule language in v1.

**One region.** PostgreSQL, UUID primary keys, `tenant_id` on every row. CockroachDB is a later move, not a requirement. The driver stays Postgres-compatible so the move is not a rewrite.

**Meter every score.** Per tenant, per day: scores, allows, step-ups, reviews, blocks, degraded allows. This is the usage record and the only honest number to show a prospect.

**One database for every tenant.** Do not clone a database per customer. Isolation is `tenant_id` plus credentials. A private cluster is a later contract, not the default.

---

## Who calls the API

| Caller | What they send | What they do with the result |
| --- | --- |
| PSP or payment gateway | Intent before authorization | Allow, challenge, queue, or decline |
| Bank or UPI app backend | Debit before the debit is sent | Step-up, hold for review, or stop |
| Wallet or merchant platform | Payout or peer transfer | Same action set, their own UX |

Authentication to PayGuard is the organization's API credential, separate from the end-user device signature. Features, lists, rules, decisions, and training rows never cross organization boundaries unless that tenant has opted into a shared model in writing.

---

## Problem

A large share of payment fraud is technically valid. The user authenticated, the signature checks out, and the balance is sufficient. The payment is still fraud.

Two cases drive the first release. A single velocity counter does not cover both.

**Mule and layering.** An account is quiet for weeks. Within minutes it receives funds from many senders and forwards most of that value to new beneficiaries, often across several hops. That is the pattern networks such as NPCI describe for mule accounts: fan-in, then fan-out. "This user made five payments today" does not describe it.

**Device and session risk.** The request itself is wrong: a new phone, a SIM change, an emulator, an accessibility service used for screen sharing, a datacenter IP running card tests, or a script that completes checkout in a few hundred milliseconds. This is not Ticketmaster-style bot defense. That problem is inventory scraping. Here the question is whether this authenticated payment should move money. Often a person is entering the PIN because someone on a phone call is coaching them.

A third constraint comes from production risk stacks (Razorpay's risk-based authentication, Adyen Protect, Stripe Radar): declining every risky payment destroys authorization rate. The API must be able to say "challenge" or "review" without saying "decline".

---

## API actions

`POST /v1/score` returns one action. The score is supporting data, not a substitute for the action.

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

**Pre-debit, synchronous.** On the caller's payment path. Inputs: payer velocity, device binding, new-beneficiary status, amount against baseline, SIM and integrity signals. Latency budget: p95 under 40 ms for the whole score call. This call may return any of the four actions.

**Post-pattern, asynchronous.** After the caller reports the outcome. PayGuard updates windows and edges: who paid whom, and how fast funds left. That evidence applies to the next debit. The first request is not held open while the graph updates. A pre-debit `BLOCK` is never overturned by a later async pass. Other actions may become stricter on the next call.

Callers must send an outcome. Without outcomes, replay, false-positive rates, and any future model are empty.

---

## Labels, and the model that comes later

Completed is not "good." Failed is not "fraud." A payment can complete and still be a mule transfer. A payment can fail because the user had no balance. Training on that pair would teach the model the wrong thing.

What v1 stores, on every score, is a **training row**:

- `decision_id`, `tenant_id`, time
- the feature vector **as it was at score time**, including missing fields. Later recomputation is forbidden. That is how training and serving drift apart
- the action the rules returned, the rule versions, and whether the call was `degraded`
- `label` — null until a real label exists
- `label_source` and `labeled_at`

A label is attached only by `POST /v1/outcomes` or by a reviewer's decision. Sources, strongest first:

| Source | Label | Strength |
| --- | --- | --- |
| Caller reports a chargeback or a confirmed scam | fraud | strong |
| Reviewer marks `review_fraud` | fraud | strong |
| Reviewer marks `review_legit` | legit | strong |
| Customer dispute upheld by the caller | fraud | strong |
| `completed`, and no fraud label before the tenant's dispute window ends (default 90 days) | legit | weak |
| `failed` because of insufficient funds, a user cancel, or a technical error | unlabeled | not used |
| `step_up_failed` | unlabeled | not used. The user may have been legitimate and confused |

Weak labels are down-weighted. They are not treated as proof.

Training, when it starts, is a **batch job on a schedule**, not an update inside `POST /v1/score`. Online updates from one request are how a fraudster poisons the model: they send traffic, feed a label, and move the boundary. A weekly job reads rows whose label landed since the last run, trains or fine-tunes offline, and writes a versioned artifact. The artifact is not on the payment path until it has been shadowed.

The model does not replace rules. The order is:

1. Rules enforce. The row is stored. No model.
2. A model scores the same frozen features in shadow. We compare it to labels. It cannot `BLOCK` anyone.
3. If its shadow record is better than the rules on recall without a worse false-positive rate, a rule may *read the model score as one more feature*. `R1`, `R2`, and `R9` (bad signature, failed integrity, bad list) still override the model.
4. Each tenant's rows train that tenant's model by default. A shared model exists only for tenants that opt in, because one bank's fraud is not another bank's training set unless they agreed.

Fraud labels are late. Chargebacks arrive days or weeks after the score. The join key is `decision_id`. A model that only learns from the same day's successes will look accurate and be useless.

Class balance will be extreme. A training job that samples "all fraud, a capped number of legit" is required. Accuracy is the wrong metric. The metrics are recall on strong-fraud labels, false-positive rate on weak-legit labels, and the extra step-ups a shadow model would have caused.

v1 builds the row, the label join, and the retention delete. It does not build the trainer.

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
2. Every score request is signed over user, amount, beneficiary, timestamp, and nonce. The private key never leaves the device.
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
- join analyst labels to estimate false positives
- replay a candidate YAML against historical payments and see how many extra `BLOCK` and `STEP_UP` results it would have produced

**Blast radius.** A rule may not return more than N `BLOCK` actions in five minutes, per tenant. Past the cap, the rule drops to `SHADOW` and an alert fires. One bad threshold must not halt a caller's payments.

On the pre-debit path, only features that fit the latency budget are read. A multi-hop walk that is too slow is limited, at pre-debit, to a direct `BAD` hit. Deeper hops are computed asynchronously and apply on the next call.

---

## Initial rule set

These thresholds are defaults, overridable per tenant. All of them start in `SHADOW`.

| Id | Condition | Action |
| --- | --- |
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
5. The caller reports `step_up_passed`. The payment completes. Async ingest updates fan-in and pass-through. The training row stays unlabeled.
6. On the third forward, `R8` matches. If `R8` is `LIVE`, the action is `REVIEW`. If `R8` is `SHADOW`, the enforced action stays whatever the live rules say, and the audit row records that `R8` would have reviewed.
7. A reviewer later sends `review_fraud`. That outcome sets `label=fraud`, `label_source=reviewer` on the frozen feature vector. It does not retrain anything inline.
8. The decision row contains feature values, rule ids, and binding state. A replay with `R8` set to `OFF` shows the third forward no longer reviewing because of that rule.

---

## What is stored

One PostgreSQL database, shared by tenants, isolated by `tenant_id`. UUID primary keys. Redis holds only counters that can be rebuilt.

| Record | Store | Contents |
| --- | --- | --- |
| Tenant | Postgres | UUID, credential hash, fail-open or fail-closed, review TTL fail-safe, dispute window, retention days |
| Binding | Postgres | Tenant, user id, device id, install id, SIM hash, public key, state |
| List entry | Postgres | Tenant, `BAD` / `GREY` / `TRUSTED`, VPA or phone hash or device id |
| Rule config | Postgres | Tenant, rule id, thresholds, version, `SHADOW` / `CANARY` / `LIVE` / `OFF` |
| Decision | Postgres | UUID, tenant, idempotency key, intent ids, action, score, rule hits, degraded flag, time |
| Training row | Postgres | Decision id, feature vector exactly as scored, null label until an outcome attaches one |
| Outcome | Postgres | Decision id, caller status, label source, time |
| Baseline | Postgres | Per user, usual amount and hours, trailing 30 days |
| Usage | Postgres | Per tenant per day: scores, actions, degraded count |
| Counters | Redis | Fan-in, fan-out, velocity, first-seen, block budget. Hours, not the system of record |

---

## Services

```
Caller
    |  POST /v1/score
    |  API key + signed device context + intent + idempotency key
    v
Decision API                 hot path, p95 < 40 ms
    |- tenant from the key, never from the body
    |- verify device binding
    |- read features (Redis). If Redis is down: ALLOW, reason degraded
    |- evaluate rules
    |- return action
    '- append decision + frozen feature vector (async, not on the wait)

POST /v1/outcomes
    |- attach label if the status is a real label
    '- update Redis counters and baselines

Redis:    windows, first-seen, edges, per-rule block budget
Postgres: tenants, bindings, lists, baselines, rules,
          decisions, training rows, outcomes, daily usage

Later, not on the request:
    batch trainer reads labeled rows
    writes a versioned model
    shadow scorer compares it to labels
    rules may read that score only after the shadow record holds
```

Feature computation and rule evaluation are separate packages so replay can run without the decision server.

---

## Request and response

`POST /v1/score`

- Idempotency-Key header
- API key, mapped to a tenant. Body `tenant_id` is ignored
- `intent`: payer id, amount, currency, beneficiary id, channel (`UPI`, `CARD`, `WALLET`, `PAYOUT`)
- `device`: binding signature, device id, instance id, sim-hash match, fingerprint, integrity verdict, emulator, rooted, accessibility, installer
- `network`: IP, ASN class, geo, TLS fingerprint if present
- `session`: session duration, PIN duration

Response:

- `action`
- `score`
- `rules[]` with id, version, action, reason
- `features` that were actually read
- `decision_id`
- `degraded` boolean

`POST /v1/outcomes`

- `decision_id`
- `status`: `completed`, `failed`, `step_up_passed`, `step_up_failed`, `review_fraud`, `review_legit`, `chargeback`, `scam_confirmed`
- `failure_reason` when status is `failed` (`insufficient_funds`, `user_cancelled`, `technical`). Those do not become labels

---

## Data PayGuard does not store

- Raw ICCID, IMEI, MAC address, contacts, or SMS content
- Device private keys
- Full card numbers, names, or government id numbers
- A model weight updated from a single HTTP request

A self-reported "not rooted" flag with no attestation is stored as `UNKNOWN`, never as `PASS`.

---

## Out of scope for v1

- Serving a model, or training inside the score call
- A shared model across tenants who have not opted in
- A general graph database or a graph neural network
- Merchant-website review
- Speaking 3-D Secure or a bank ACS. The API returns `STEP_UP`. The caller runs the challenge
- A mobile SDK, a case-management UI, a custom rule language
- Queue tokens, CAPTCHA, or proof-of-work
- Moving, settling, or reconciling funds
- CockroachDB, a second region, SOC 2 paperwork (the data layout is what makes that paperwork possible later)

---

## Build order

1. `POST /v1/score` and `POST /v1/outcomes`. API key to tenant. Idempotency. Fail-open when Redis is down.
2. Postgres tables: tenant, binding, decision, training row, outcome, daily usage.
3. Redis features: velocity, first-seen, fan-in, fan-out, pass-through.
4. Binding verification and cooling.
5. `R1`–`R10`, default `SHADOW`, block-budget cap.
6. Integration test for the reference scenario, including a late `review_fraud` that labels the frozen row and does not change the original action.
7. Traces, and the per-tenant daily meter.

No model code, and no further product surface, until step 6 is green.
