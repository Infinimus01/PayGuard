# PayGuard — Design

Yeh document batata hai PayGuard **kya** karta hai, **kyun**, aur pehla version **kaise** banega. Marketing page nahi hai. Jo yahan nahi likha, voh pehle release ka hissa nahi hai.

Stack: Go, gRPC, Redis, PostgreSQL, Kubernetes. Machine learning is release mein nahi hai.

---

## Problem

Payment fraud ka bada hissa aisi transactions hai jo **technically valid** hoti hain. User ne PIN daala, signature sahi hai, balance hai. Phir bhi paisa fraud hai.

Do alag cases hain. Dono ko ek hi "velocity > 5" rule se nahi pakda ja sakta.

**Mule / layering.** Account hafte bhar se soya hua hai. Phir kuch minute mein kai logon se paise aate hain, aur turant naye UPI IDs pe nikal jaate hain. NPCI isi pattern ko mule account maanti hai: fan-in, phir fan-out, kai hops. Sirf "is user ne aaj 5 payment ki" isse nahi dikhta.

**Device aur session.** Doosra fraud request ke source ka hai. Naya phone, SIM change, emulator, screen-share (AnyDesk / accessibility), datacenter IP se card testing, ya script jo form 200 ms mein bhar de. Ticketmaster jaisa bot detection yahan copy nahi karna. Wahan inventory scraping hai. Yahan paisa move ho raha hai, aur aksar insaan khud PIN daal raha hai kyunki koi call pe guide kar raha hai.

Teesra constraint: **har risky payment ko BLOCK karna business kharab karta hai.** Razorpay jaise stacks isliye risk-based step-up use karte hain. Low risk seedha jaaye. High risk pe extra confirm. Sirf clear fraud pe block.

---

## Decision

Har score ka output char mein se ek action hai. Sirf number nahi.

| Action | Matlab |
| --- | --- |
| `ALLOW` | Payment aage badhe |
| `STEP_UP` | Extra confirm: PIN dobara, cooldown, ya beneficiary naam dikha ke haan | Payment kill nahi |
| `REVIEW` | Insaan dekhe. Payment hold, TTL ke saath. TTL khatam aur koi decision nahi to fail-safe config se ALLOW ya BLOCK |
| `BLOCK` | Payment ruke |

Har decision ke saath:

- numeric score (ordering aur analytics ke liye, action ka substitute nahi)
- fired rules: id, version, action, reason code
- un features ki values jinpe rule chala
- idempotency key. Same payment dobara aaye to same decision, naya side effect nahi

Do modes:

- **blocking** — action payment ko rok sakta hai
- **non-blocking** — sirf log. Rollout ke waqt pehle yahi

---

## Do decision points

**Pre-debit (sync, payment ke raste mein).** Payer velocity, device binding, naya beneficiary, amount vs baseline, SIM/integrity. Yahan latency budget tight hai: poora score p95 40 ms ke andar. Yahan se ALLOW, STEP_UP, REVIEW, BLOCK.

**Post-pattern (async, seconds).** Is payment ke baad graph update: kisne bheja, kahan gaya, kitni der mein. Mule score agle debit pe kaam aayega. Pehli transaction ko late block karke user ka request mat latkao. Jo pehle ho chuka, use REVIEW queue ya agla hop pakdo.

Pre-debit BLOCK ko post-pattern ALLOW override nahi karega. Baaki actions post-pattern evidence se agle request pe sakht ho sakte hain.

---

## Features

### Paisa ka flow (mule)

- Account age, aur last successful payment ke baad kitne din khali (dormant gap)
- Inbound fan-in: alag payers, last 10 min / 1 hour, count + amount
- Outbound fan-out: alag beneficiaries, last 10 min / 1 hour
- Pass-through: last 30 min mein aaya hua amount ka kitna hissa nikal gaya
- Beneficiary first-seen age. Pehli baar is VPA ko paisa, amount threshold se upar
- Hop: beneficiary pehle se `BAD` list pe hai, ya uske 1–2 hop pe hai (seedha Redis edges, full graph database nahi)

### User baseline

- Usual amount (median, last 30 din)
- Usual hours
- Pehle jise bheja hai, woh set

Baseline missing ho (naya user) to rule "deviation" skip kare, dormant/new-beneficiary phir bhi chale.

### Device binding

Binding identity hai. Fingerprint identity nahi hai.

Ek binding:

- `user_id`
- `device_id` — app ne banaya, stable id
- `app_instance_id` — is install ka. Reinstall pe naya
- `sim_hash` — SIM ka hash. Raw ICCID store nahi
- `public_key` — private key device ke secure hardware mein (Android Keystore / iOS Secure Enclave)
- `bound_at`, `state`: `ACTIVE` | `COOLING` | `REVOKED`

Flow:

1. Pehli baar user OTP/PIN se bind karta hai. Server public key rakhta hai.
2. Har payment app request sign karti hai: user, amount, beneficiary, timestamp, nonce. Private key device se bahar nahi jaati.
3. Server signature verify karta hai. Fail = `BLOCK`.
4. SIM hash badla, ya naya `app_instance_id` = purani binding `COOLING` (default 24h). Cooling mein naya beneficiary + high amount = `STEP_UP` ya `REVIEW`.
5. "Sab devices hatao" = saari bindings `REVOKED`.

### Fingerprint aur integrity

Fingerprint batata hai environment kaisa hai, "yeh wahi phone hai" prove nahi karta. OS update pe badal jata hai. Isliye fingerprint drift akela `BLOCK` nahi karega.

App se aane wale signals:

- `fp_hash` — model, OS, app version, screen, timezone, language ka hash. Raw IMEI nahi.
- `fp_drift` — `SAME` | `DRIFT` | `NEW` pichhli successful payment ke against
- `integrity` — Play Integrity ya App Attest ka server-side verdict: `PASS` | `FAIL` | `UNKNOWN`. Client ka "root: false" string kaafi nahi.
- `emulator`, `rooted`
- `accessibility_enabled` — screen-share / remote control class ka signal
- installer: Play Store ya sideload

`integrity = FAIL` ya emulator pe payment = `BLOCK`. Accessibility on + naya beneficiary = `STEP_UP`, high amount pe `REVIEW`.

### Network aur session

- IP, country, region
- ASN class: `RESIDENTIAL` | `DATACENTER` | `VPN` | `TOR` | `UNKNOWN`
- Is IP pe kitne alag user last 1 hour
- Geo vs user ka usual region
- TLS fingerprint (JA3/JA4) jab edge pe mil sake. Claimed client "Android app" ho aur handshake browser-bot jaisa ho to flag
- `session_ms`: checkout start se pay tak
- `pin_entry_ms`

Datacenter/TOR + chhote repeated attempts = card testing = `BLOCK`. Session 200 ms mein complete = script = `BLOCK` ya `STEP_UP`, rule severity se. PIN bahut slow aur beech mein lamba pause, naya beneficiary ke saath = remote-assist suspect = `STEP_UP`.

Web checkout (app nahi) pe extra: `webdriver` / headless flag. Mobile UPI pe yeh weak hai, wahan binding + integrity primary hain.

### Lists

Exact match: VPA, phone hash, device id.

List type: `BAD` | `GREY` | `TRUSTED`.

`BAD` pe pre-debit `BLOCK` ya `REVIEW` (list ke config se). `TRUSTED` payroll VPA pe mule fan-out rule soft ho sakta hai, band nahi. Fuzzy name match sirf review UI ke liye. Fuzzy pe auto-block nahi, warna false positive.

---

## Rules ka lifecycle

Rule YAML mein, pure: input features → action. Bahar kisi store ko rule khud update nahi karta.

State:

| State | Asar |
| --- | --- |
| `SHADOW` | Action compute karo, payment mat chhedo. Log mein "hota to yeh hota" |
| `CANARY` | Chhote hissa traffic pe live action. Block rate cap |
| `LIVE` | Sab pe |
| `OFF` | Band |

Har execution ka input, output, rule version Postgres mein. Baad mein:

- is rule ne kitni baar fire kiya
- analyst ne kitni baar fraud / legit bola (false positive)
- replay: kal ki payments pe naya YAML chalao, extra BLOCK/STEP_UP kitne

**Blast radius.** Ek rule 5 minute mein configured N se zyada `BLOCK` na kare. Cap ke baad rule apne aap `SHADOW` pe gir jaaye aur alert. Ek galat threshold se poora traffic na ruke.

Pre-debit pe sirf wahi features jo budget mein aa jayein. Graph hop mahanga ho to pre-debit mein sirf direct `BAD` list, hop depth async.

---

## Default rules (pehla set)

Yeh numbers shuruaat hain, config se badlenge. Pehle sab `SHADOW`.

| Id | Condition | Action |
| --- | --- | --- |
| `R1` | Signature invalid ya binding `REVOKED` | BLOCK |
| `R2` | Integrity FAIL ya emulator | BLOCK |
| `R3` | Datacenter/TOR IP aur 10 min mein 8+ chhote attempts | BLOCK |
| `R4` | Session < 300 ms aur naya beneficiary | BLOCK |
| `R5` | Naya device ya cooling, naya VPA, amount > baseline × 3 | STEP_UP |
| `R6` | Accessibility on aur naya VPA | STEP_UP |
| `R7` | Dormant > 30 din, phir 10 min mein inbound payers ≥ 5 | STEP_UP |
| `R8` | Pass-through > 80% aur outbound new VPA ≥ 3, 15 min | REVIEW |
| `R9` | Beneficiary `BAD` list pe | BLOCK |
| `R10` | Beneficiary ke 1 hop pe `BAD`, amount > threshold | REVIEW |

Sabse sakht action jeetega: BLOCK > REVIEW > STEP_UP > ALLOW. TRUSTED list `R8` ko SKIP kar sakti hai, `R1`/`R2`/`R9` ko nahi.

---

## Pehla end-to-end scene

Yahi demo hai. Isi pe tests.

1. Account 40 din se khali.
2. 6 minute mein 8 alag payers se ₹2,000–₹5,000 aata hai.
3. 90% amount 4 naye VPAs pe forward hota hai.
4. Pehla forward: device ACTIVE, lekin beneficiary naya aur dormant. `R7` + `R5` → `STEP_UP`.
5. User confirm karta hai. Paisa jaata hai. Async pe fan-in / pass-through update.
6. Teesra forward: `R8` ab match. State `LIVE` hai to `REVIEW`. `SHADOW` hai to payment jaati hai, log mein REVIEW likha hota.
7. Decision row mein features, rule ids, binding state. Replay mein `R8` OFF ho to teeno ALLOW.

---

## Services

```
Client (app / checkout)
    │  gRPC ya gateway REST
    │  signed request + DeviceContext
    ▼
Decision service          ← hot path, p95 < 40 ms
    ├─ Binding verify
    ├─ Feature read (Redis)
    ├─ Rules (in-process, versioned YAML)
    └─ Audit append (async, payment response ka wait nahi)
    │
    ├─ Redis: windows, first-seen, edges, block budget
    └─ Postgres: bindings, lists, baselines, rule versions,
                 decisions, review labels

Ingest (async)
    payment completed / failed
    → windows, graph edges, baselines update

Review worker
    REVIEW queue, analyst label, FP count
```

Feature compute aur rule execute alag packages, taaki replay decision service ke bina chal sake.

---

## Data jo hum nahi rakhte

- Raw ICCID, IMEI, MAC, contacts, SMS
- Private key
- Poora PAN / card number. Sirf jo identifier payment service pehle se bheje (token, VPA, hash)

Client ka self-reported "not rooted" bina attestation ke `UNKNOWN` hai, `PASS` nahi.

---

## Is release se bahar

- ML model, training pipeline
- Full graph database, GNN
- Merchant website review
- 3DS / bank ACS ka protocol. Hum `STEP_UP` bolte hain, bank ka screen nahi chalate
- Ticketmaster-style queue, captcha, proof-of-work

---

## Build order

1. Proto: `ScorePayment`, `DeviceContext`, `Binding`, decision enum
2. Redis features: velocity, first-seen, fan-in, fan-out, pass-through
3. Binding verify + cooling
4. YAML rules, SHADOW default, blast-radius cap
5. Audit log + replay command
6. Upar wala mule scene, integration test
7. Kubernetes manifests, traces, block-rate metric

Jab tak scene 6 green na ho, naya feature add nahi karna.
