<p align="center">
  <img src="https://raw.githubusercontent.com/kongali1720/KongPay-Bank-Webhook-Listener/main/logo-baru.jpg" width="100%" alt="KongPay Bank Webhook Listener">
</p>

<h1 align="center">🔐 KongPay Bank Webhook Listener</h1>

<p align="center">
  <b>Secure mTLS + HMAC webhook ingestion service for bank transaction notifications</b><br>
  Built with Go • PostgreSQL • Prometheus • Grafana • Docker
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.26-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go">
  <img src="https://img.shields.io/badge/PostgreSQL-15-336791?style=flat-square&logo=postgresql&logoColor=white" alt="PostgreSQL">
  <img src="https://img.shields.io/badge/Prometheus-Metrics-E6522C?style=flat-square&logo=prometheus&logoColor=white" alt="Prometheus">
  <img src="https://img.shields.io/badge/Grafana-Dashboard-F46800?style=flat-square&logo=grafana&logoColor=white" alt="Grafana">
  <img src="https://img.shields.io/badge/Docker-Compose-2496ED?style=flat-square&logo=docker&logoColor=white" alt="Docker">
  <img src="https://img.shields.io/badge/License-MIT-green?style=flat-square" alt="License">
</p>

---

## 📖 Overview

**KongPay Bank Webhook Listener** is a production-grade Go service that securely receives bank transaction notifications via **mutual TLS (mTLS)** and **HMAC-SHA256 signature verification**, stores them idempotently in PostgreSQL, and exposes Prometheus metrics for real-time observability.

Designed as a **standalone component** of the KongPay ecosystem, this service handles the critical "last mile" of payment infrastructure: **trustworthy ingestion of external bank events**.

### 🎯 Why This Exists

Bank webhooks are the **most attack-prone surface** in any payment system:

- Anyone can POST to a public endpoint
- Replays can double-count transactions
- Unverified payloads can corrupt financial records

This service addresses each:

| Threat | Mitigation |
|---|---|
| **Unauthorized POST** | mTLS — client certificate required |
| **Tampered payload** | HMAC-SHA256 signature verified before DB write |
| **Duplicate events** | `ON CONFLICT DO NOTHING` + `RowsAffected()` check |
| **Silent failures** | Prometheus counters + structured logging |
| **Rogue clients** | CA-signed cert only; unsigned requests rejected at TLS layer |

---

## ✨ Features

### 🔒 Security
- ✅ **mTLS authentication** — client certificate required for every request
- ✅ **HMAC-SHA256 signature verification** — payload integrity guaranteed
- ✅ **Constant-time signature comparison** — prevents timing attacks
- ✅ **Structured logging** with request context (`event_id`, `client_ip`)
- ✅ **Self-signed CA** for development; SANs-based certs (no legacy CN)

### 💾 Data Integrity
- ✅ **Idempotent inserts** via PostgreSQL `ON CONFLICT (event_id) DO NOTHING`
- ✅ **`RowsAffected()` check** — success metric only increments on real inserts
- ✅ **Replay-safe** — duplicate `event_id` returns `200` with no-op response
- ✅ **Unique constraint** on `event_id` at schema level

### 📊 Observability
- ✅ **Prometheus metrics** on `/metrics`:
  - `kongpay_http_requests_total{handler, method}`
  - `kongpay_transactions_success_total`
- ✅ **Grafana dashboard** — auto-provisioned (see `grafana/`)
- ✅ **Prometheus scrape config** — ready to connect to any Prometheus server

### 🧪 Testing
- ✅ **7 unit + integration tests** (all passing)
- ✅ **mTLS integration test** against live server
- ✅ **Idempotency test** — verifies no duplicate rows
- ✅ **Signature verification** (valid/invalid/missing)
- ✅ **DB-backed tests** with auto-cleanup

### 🛠 Automation
- ✅ **Makefile** — 25+ targets (`make up`, `make test`, `make test-webhook`, ...)
- ✅ **`scripts/test-webhook.sh`** — end-to-end signed POST + verification
- ✅ **Docker Compose** — full stack in one command
- ✅ **Migration auto-apply** via `docker-entrypoint-initdb.d`

---

## 🏗 Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    Bank / Payment Gateway               │
│              (Client with valid mTLS cert)              │
└──────────────────────────┬──────────────────────────────┘
                           │ HTTPS + client cert
                           │ X-Hub-Signature: <HMAC>
                           ▼
┌─────────────────────────────────────────────────────────┐
│              KongPay Bank Webhook Listener              │
│                    (Go 1.26, :8443)                     │
│                                                         │
│  1. TLS handshake      → verify client cert (mTLS)      │
│  2. Read body          → compute HMAC                   │
│  3. Compare signature  → constant-time                  │
│  4. Parse JSON         → validate schema                │
│  5. INSERT ... ON CONFLICT DO NOTHING                   │
│  6. RowsAffected()     → increment metric only if > 0   │
│  7. Respond JSON       → single write                   │
└──────────────────────────┬──────────────────────────────┘
                           │
              ┌────────────┼────────────┐
              ▼            ▼            ▼
        ┌──────────┐  ┌─────────┐  ┌──────────┐
        │PostgreSQL│  │Prometheus│ │ Grafana  │
        │  :5432   │  │  :9090   │ │  :3000   │
        └──────────┘  └─────────┘  └──────────┘
```

### Request Flow

```mermaid
sequenceDiagram
    participant Bank as Bank / Client
    participant LB as mTLS Listener (:8443)
    participant H as webhookHandler
    participant DB as PostgreSQL
    participant P as Prometheus (:9090)

    Bank->>LB: POST /api/v1/bank-webhook<br/>(client cert + X-Hub-Signature)
    LB->>H: TLS verified → handler
    H->>H: Verify HMAC-SHA256
    alt Signature invalid
        H-->>Bank: 401 Unauthorized
    else Signature valid
        H->>DB: INSERT ... ON CONFLICT DO NOTHING
        DB-->>H: rowsAffected
        alt rowsAffected == 0 (duplicate)
            H-->>Bank: 200 {message: "duplicate event_id"}
        else rowsAffected == 1 (new)
            H->>P: transactionsTotal.Inc()
            H-->>Bank: 200 {message: "metrics recorded..."}
        end
    end
```

---

## 🗂 Project Structure

```
bank-webhook-listener/
├── main.go                    # Entry point + handler + metrics
├── main_test.go               # 7 tests (unit + integration)
├── go.mod, go.sum             # Dependencies
├── Dockerfile                 # Multi-stage Go build
├── docker-compose.yml         # Full stack: backend + postgres + prometheus
├── prometheus.yml             # Scrape config
├── Makefile                   # 25+ automation targets
├── migrations/
│   └── 001_init.sql           # Schema (auto-applied on first boot)
├── scripts/
│   ├── test-webhook.sh        # End-to-end signed POST
│   └── gen-makefile.py        # Makefile generator (tab-safe)
├── grafana/
│   ├── provisioning/
│   │   ├── datasources/       # Prometheus datasource
│   │   └── dashboards/        # Dashboard provider
│   └── dashboards/
│       └── kongpay.json       # Pre-built dashboard
├── docs/                      # Additional documentation
├── ca.crt, server.crt, client.crt   # Dev certificates (public only)
└── README.md
```

> ⚠️ **Private keys (`*.key`) are git-ignored.** Generate your own for production.

---

## 🚀 Quick Start

### Prerequisites

| Tool | Version |
|---|---|
| Go | 1.26+ |
| Docker | Latest |
| Docker Compose | v2+ |
| OpenSSL | Latest |
| `jq` (optional) | Latest |

### 1. Start the Stack

```bash
git clone <repo-url> bank-webhook-listener
cd bank-webhook-listener

# Full stack up (backend + postgres + prometheus + grafana)
make up
```

Or with plain Docker:

```bash
docker compose up -d --build
```

### 2. Verify Services

```bash
docker compose ps
```

Expected:
```
NAME                 STATUS
kongpay_backend      Up (ports 8443, 9090)
kongpay_postgres     Up (healthy, port 5432)
kongpay_prometheus   Up (port 9091→9090)
```

### 3. Send a Signed Webhook

```bash
# Using the provided script
make test-webhook

# Or manually
PAYLOAD='{"event_id":"EVT-DEMO-001","account_number":"999888777","amount":2500000.00,"currency":"USD","sender_name":"DEMO BANK","reference":"INV-DEMO","status":"COMPLETED"}'
SIG=$(printf '%s' "$PAYLOAD" | openssl dgst -sha256 -hmac "KONGPAY_SUPER_SECRET_KEY_2026" | awk '{print $2}')

curl -X POST https://localhost:8443/api/v1/bank-webhook \
  --cacert ca.crt --cert client.crt --key client.key \
  -H "Content-Type: application/json" \
  -H "X-Hub-Signature: $SIG" \
  -d "$PAYLOAD"
```

Expected response:
```json
{"status": "success", "message": "metrics recorded, mTLS verified and saved"}
```

### 4. Query Transactions

```bash
make history
# → JSON array of transactions (newest first)

make db
# → raw PostgreSQL table
```

---

## 📡 API Reference

### `POST /api/v1/bank-webhook`

Receives a signed bank transaction notification.

**Headers:**

| Header | Required | Description |
|---|---|---|
| `Content-Type` | ✅ | `application/json` |
| `X-Hub-Signature` | ✅ | HMAC-SHA256 of raw body, hex-encoded |
| TLS client certificate | ✅ | Signed by trusted CA |

**Body:**
```json
{
  "event_id": "EVT-DOCKER-MTLS-001",
  "account_number": "999888777",
  "amount": 2500000.00,
  "currency": "USD",
  "sender_name": "DOCKER ENTERPRISE BANK",
  "reference": "INV-DOCKER-SECURE",
  "status": "COMPLETED"
}
```

**Responses:**

| Status | Body | Meaning |
|---|---|---|
| `200` | `{"status":"success","message":"metrics recorded..."}` | First-time insert |
| `200` | `{"status":"success","message":"duplicate event_id..."}` | Idempotent replay (no DB write) |
| `400` | `Invalid JSON` | Malformed body |
| `401` | `Unauthorized` | Bad/missing signature |
| `500` | `Database error` | Insert failure |

### `GET /api/v1/transactions`

Returns last 50 transactions, newest first.

```bash
curl -s https://localhost:8443/api/v1/transactions \
  --cacert ca.crt --cert client.crt --key client.key | jq .
```

Response:
```json
{
  "status": "success",
  "data": [
    {
      "id": 1,
      "event_id": "EVT-DOCKER-MTLS-001",
      "account_number": "999888777",
      "amount": 2500000,
      "currency": "USD",
      "sender_name": "DOCKER ENTERPRISE BANK",
      "reference": "INV-DOCKER-SECURE",
      "status": "COMPLETED",
      "created_at": "2026-10-28T03:38:47.647074Z"
    }
  ]
}
```

### `GET :9090/metrics`

Prometheus metrics endpoint.

```
kongpay_http_requests_total{handler="webhook",method="POST"} 7
kongpay_transactions_success_total 8
```

---

## 🧪 Testing

### Run the full suite

```bash
export TEST_DATABASE_URL="postgres://postgres:password@localhost:5432/kongpay_db?sslmode=disable"
go test . -v
```

**Expected:**
```
--- PASS: TestVerifyHMAC
--- PASS: TestWebhookHandler_Security (2 subtests)
--- PASS: TestSign_Deterministic
--- PASS: TestWebhookHandler_Success
--- PASS: TestWebhookHandler_DuplicateEventID
--- PASS: TestGetTransactionsHandler
--- PASS: TestWebhookMTLS_Live
PASS
ok  bank-webhook-listener
```

### Test categories

| Test | What it proves |
|---|---|
| `TestVerifyHMAC` | HMAC helper correct |
| `TestWebhookHandler_Security` | Missing/bad signature → 401 |
| `TestSign_Deterministic` | Signature stable across runs |
| `TestWebhookHandler_Success` | Valid request → 200 **and** row in DB |
| `TestWebhookHandler_DuplicateEventID` | Replay → 200, no duplicate row |
| `TestGetTransactionsHandler` | GET returns `{"status":"success","data":[...]}` |
| `TestWebhookMTLS_Live` | Real mTLS cert accepted |

---

## 📊 Observability

### Prometheus

Scraped by the included Prometheus server:

```
http://localhost:9091/targets
```

Should show `kongpay-backend` as **UP**.

Query metrics:

```bash
curl -s 'http://localhost:9091/api/v1/query?query=kongpay_transactions_success_total' | jq .
```

### Grafana

```bash
make grafana
# Opens http://localhost:3000
```

Login: `admin` / `admin` (change on first login)

**Dashboard → KongPay → KongPay Webhook** shows:

- Total successful transactions
- Webhook POST rate
- Success rate %
- Backend UP status
- Error count
- Go heap, GC pause

![KongPay Webhook Dashboard](https://raw.githubusercontent.com/kongali1720/KongPay-Bank-Webhook-Listener/main/grafana-admin-Al%21g3.png)

---

## 🛠 Automation (Makefile)

```bash
make help             # list all targets
```

| Target | Action |
|---|---|
| `make up` | Start full stack |
| `make down` | Stop stack (keep data) |
| `make reset` | Destroy volumes + rebuild |
| `make rebuild` | Rebuild backend image |
| `make test` | Unit + DB tests |
| `make test-live` | Live mTLS test |
| `make test-webhook` | Send signed webhook |
| `make history` | GET transactions via API |
| `make db` | Dump PostgreSQL table |
| `make psql` | Interactive psql |
| `make metrics` | Print Prometheus counters |
| `make prom-targets` | Check scrape health |
| `make grafana` | Open dashboard |

---

## 🔐 Security Notes

### Dev Certificates (included)

The `ca.crt`, `server.crt`, `client.crt` in this repo are **development-only**. They exist for `make test-webhook` to work out of the box.

**For production:**

1. Generate your own CA (`ca.key`, `ca.crt`)
2. Issue server cert with SANs (`DNS:yourdomain.com, IP:127.0.0.1`)
3. Issue client certs per integrating party
4. Never commit `.key` files
5. Rotate HMAC secret regularly

### HMAC Secret

Set via constant `KONGPAY_SUPER_SECRET_KEY_2026` in `main.go`. For production, load from environment variable:

```go
webhookSecret = os.Getenv("WEBHOOK_SECRET")
```

### TLS Configuration

Server enforces `RequireAndVerifyClientCert` — unsigned clients are rejected at handshake, before any HTTP processing.

---

## 🗄 Database Schema

```sql
CREATE TABLE bank_transactions (
    id              BIGSERIAL PRIMARY KEY,
    event_id        TEXT UNIQUE NOT NULL,
    account_number  TEXT NOT NULL,
    amount          NUMERIC(18,2) NOT NULL,
    currency        TEXT NOT NULL,
    sender_name     TEXT,
    reference       TEXT,
    status          TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_bank_transactions_created_at
    ON bank_transactions(created_at DESC);
```

Auto-applied on first boot via `migrations/001_init.sql` (mounted to `/docker-entrypoint-initdb.d/`).

---

## 🐛 Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `lookup postgres: no such host` | Backend can't reach DB | Check `postgres` container is `Up` |
| `bind: address already in use` | Port conflict | `sudo lsof -i :8443` then `kill <PID>` |
| `Database error` on POST | Table missing | Check `\dt` in psql |
| `x509: certificate relies on legacy CN` | Cert without SANs | Regenerate with `subjectAltName` |
| `TLS handshake failed` | Client cert not trusted | Verify `ca.crt` matches server's CA |
| `401 Unauthorized` | HMAC mismatch | Compute signature from raw body, hex-encoded |

---

## 🗺 Roadmap

- [x] mTLS + HMAC webhook ingestion
- [x] Idempotent inserts
- [x] Prometheus metrics
- [x] Grafana dashboard
- [x] Automated test suite
- [ ] Environment-based secret loading
- [ ] Rate limiting per client certificate
- [ ] Webhook retry queue (dead-letter)
- [ ] Multi-currency formatting per ISO 4217
- [ ] Webhook signature rotation (key versioning)
- [ ] OpenAPI/Swagger spec

---

## 🤝 Contributing

1. Fork the repo
2. Create a feature branch (`git checkout -b feat/my-feature`)
3. Run tests (`make test`)
4. Commit with [conventional commits](https://www.conventionalcommits.org/)
5. Push and open a PR

**Before opening a PR:**

```bash
gofmt -w .
go vet .
make test
```

---

## 📄 License

MIT License — see [LICENSE](LICENSE) for details.

---

## ☕ Support the Project

If this project has helped your research, learning, or security operations, consider supporting its continued development.

<div align="center">

<a href="https://www.paypal.com/paypalme/bungtempong99">
  <img src="https://img.shields.io/badge/☕-Support_Development-FFDD00?style=for-the-badge&logo=buymeacoffee&logoColor=000000" alt="Support Development">
</a>

</div>

---

<p align="center">
  <b>Built with Go by KONGALI1720</b><br>
  <i>Secure by design. Observable by default. Tested thoroughly.</i><br><br>
  🇮🇩 Made with ❤️ in Indonesia
</p>
