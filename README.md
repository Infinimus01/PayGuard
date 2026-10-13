# PayGuard

**Real-time transaction risk engine for payment systems.**

PayGuard har payment se pehle ek explainable decision deta hai: `ALLOW`, `STEP_UP`, `REVIEW`, ya `BLOCK`. Saath mein woh features aur rules jo decision ke peeche the.

Do problems pe focused hai, generic "5 payments in a minute" demo nahi:

- **Mule / layering** — dormant account, phir tez fan-in aur fan-out
- **Device aur session** — binding, integrity, SIM cooling, proxy/datacenter, screen-share

Machine learning is version mein nahi hai. Pehle features, rules, aur audit solid hone chahiye.

Design, rules, aur pehla demo scene: [DESIGN.md](DESIGN.md).

---

## Kya cover hai

### Paisa ka flow
- Dormant gap, inbound fan-in, outbound fan-out, pass-through ratio
- Beneficiary first-seen age
- 1–2 hop tak `BAD` list se rishta
- User baseline: usual amount, hours, pehle ke payees

### Device binding
- Device key se signed request. Private key secure hardware mein
- SIM / app reinstall pe `COOLING`, "logout all" pe `REVOKED`
- Invalid signature ya revoked binding → `BLOCK`

### Fingerprint, integrity, network
- Fingerprint drift signal hai, identity nahi. Akela block nahi karta
- Play Integrity / App Attest verdict. Client ka "not rooted" kaafi nahi
- Emulator, accessibility / screen-share, datacenter · VPN · TOR
- Session time aur PIN time (script vs remote-assist)

### Decision
- `ALLOW` · `STEP_UP` · `REVIEW` · `BLOCK`
- Pre-debit sync (p95 &lt; 40 ms) aur post-pattern async
- Idempotent: same payment, same decision

### Rules jo production mein chalti hain waise
- `SHADOW` → `CANARY` → `LIVE`
- Har fire ka input/output audit
- Historical replay, false-positive count
- Per-rule block budget. Galat rule poora traffic na roke
- `BAD` / `GREY` / `TRUSTED` lists. Fuzzy match pe auto-block nahi

---

## Architecture

```
App / checkout
      │  signed request + device context
      ▼
Decision service  (gRPC, hot path)
      ├─ binding verify
      ├─ features (Redis)
      ├─ rules (versioned YAML)
      └─ audit (async)
      │
      ▼
ALLOW | STEP_UP | REVIEW | BLOCK

Async ingest → windows, edges, baselines
Review worker → analyst labels
```

Redis: windows, first-seen, edges, block budget. PostgreSQL: bindings, lists, baselines, decisions, labels.

---

## Tech stack

| Layer | Choice |
| --- | --- |
| Language | Go |
| RPC | gRPC + Protocol Buffers |
| Hot features | Redis |
| Durable state | PostgreSQL |
| Observability | OpenTelemetry, Prometheus |
| Deploy | Kubernetes |

---

## Status

Design lock hai, implementation abhi shuru nahi hui. Order [DESIGN.md](DESIGN.md) ke end mein hai. Pehla goal: mule scene ka integration test, rules `SHADOW` se.

---

## Design principles

- Latency pehle. Pre-debit p95 40 ms.
- Har decision audit ho sake. Score akela kaafi nahi.
- Block last resort hai. Step-up aur review pehle.
- Client jo bole use tab maan-o jab attestation ya signature ho.
- Ek rule ko poora system rokne mat do.

---

## License

MIT
