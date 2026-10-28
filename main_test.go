package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

const testSecret = "KONGPAY_SUPER_SECRET_KEY_2026"

// ── helpers ──────────────────────────────────────────────────

func signTestPayload(payload []byte) string {
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	testDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := testDB.Ping(); err != nil {
		t.Fatalf("ping db: %v", err)
	}
	t.Cleanup(func() { testDB.Close() })
	return testDB
}

func ensureSchema(t *testing.T, testDB *sql.DB) {
	t.Helper()
	_, err := testDB.Exec(`
		CREATE TABLE IF NOT EXISTS bank_transactions (
			id              BIGSERIAL PRIMARY KEY,
			event_id        TEXT UNIQUE NOT NULL,
			account_number  TEXT NOT NULL,
			amount          NUMERIC(18,2) NOT NULL,
			currency        TEXT NOT NULL,
			sender_name     TEXT,
			reference       TEXT,
			status          TEXT,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
}

// withDB swaps the global `db` for the duration of a test.
func withDB(t *testing.T, testDB *sql.DB) {
	t.Helper()
	orig := db
	db = testDB
	t.Cleanup(func() { db = orig })
}

// ── original tests (kept intact) ─────────────────────────────

func TestVerifyHMAC(t *testing.T) {
	payload := []byte(`{"event_id":"TEST-001","amount":100}`)
	secret := testSecret

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	validSig := hex.EncodeToString(mac.Sum(nil))

	if !verifyHMAC(payload, validSig, secret) {
		t.Errorf("Seharusnya valid, tetapi ditolak oleh verifyHMAC")
	}
	if verifyHMAC(payload, "salah_signature_123", secret) {
		t.Errorf("Seharusnya tidak valid, tetapi diterima oleh verifyHMAC")
	}
}

func TestWebhookHandler_Security(t *testing.T) {
	webhookSecret = testSecret

	payload := []byte(`{
		"event_id": "EVT-TEST-UNIT-01",
		"account_number": "1234567890",
		"amount": 1000.00,
		"currency": "USD",
		"sender_name": "TEST SENDER",
		"reference": "REF-001",
		"status": "COMPLETED"
	}`)

	t.Run("Kasus 1: Request tanpa Header Signature (Harus 401)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/bank-webhook", bytes.NewBuffer(payload))
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		webhookHandler(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Expected status 401 Unauthorized, got %d", rec.Code)
		}
	})

	t.Run("Kasus 2: Request dengan Signature Salah (Harus 401)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/bank-webhook", bytes.NewBuffer(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Hub-Signature", "invalid_signature_hash")

		rec := httptest.NewRecorder()
		webhookHandler(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Expected status 401 Unauthorized, got %d", rec.Code)
		}
	})
}

// ── new tests ────────────────────────────────────────────────

func TestSign_Deterministic(t *testing.T) {
	payload := []byte(`{"event_id":"EVT-1"}`)
	s1 := signTestPayload(payload)
	s2 := signTestPayload(payload)
	if s1 != s2 {
		t.Fatalf("signature not deterministic")
	}
	if len(s1) != 64 {
		t.Fatalf("signature length = %d, want 64", len(s1))
	}
	if signTestPayload([]byte(`{"event_id":"EVT-2"}`)) == s1 {
		t.Fatal("different payload should produce different signature")
	}
}

func TestWebhookHandler_Success(t *testing.T) {
	testDB := openTestDB(t)
	ensureSchema(t, testDB)
	withDB(t, testDB)
	webhookSecret = testSecret

	eventID := fmt.Sprintf("EVT-OK-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = testDB.Exec(`DELETE FROM bank_transactions WHERE event_id = $1`, eventID)
	})

	payload := []byte(fmt.Sprintf(
		`{"event_id":"%s","account_number":"42","amount":123.45,`+
			`"currency":"IDR","sender_name":"Tester","reference":"R-1","status":"COMPLETED"}`,
		eventID))
	sig := signTestPayload(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/bank-webhook", bytes.NewBuffer(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature", sig)

	rec := httptest.NewRecorder()
	webhookHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode json: %v body=%s", err, rec.Body.String())
	}
	if resp["status"] != "success" {
		t.Fatalf("response status = %v", resp["status"])
	}

	var count int
	if err := testDB.QueryRow(
		`SELECT COUNT(*) FROM bank_transactions WHERE event_id = $1`, eventID,
	).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want 1", count)
	}
}

func TestWebhookHandler_DuplicateEventID(t *testing.T) {
	testDB := openTestDB(t)
	ensureSchema(t, testDB)
	withDB(t, testDB)
	webhookSecret = testSecret

	eventID := fmt.Sprintf("EVT-DUP-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = testDB.Exec(`DELETE FROM bank_transactions WHERE event_id = $1`, eventID)
	})

	payload := []byte(fmt.Sprintf(
		`{"event_id":"%s","account_number":"1","amount":1.0,`+
			`"currency":"USD","sender_name":"X","reference":"R","status":"COMPLETED"}`,
		eventID))
	sig := signTestPayload(payload)

	// First call → 200, row inserted once.
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/bank-webhook", bytes.NewBuffer(payload))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Hub-Signature", sig)
	webhookHandler(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first call status = %d body=%s", rec1.Code, rec1.Body.String())
	}

	var count int
	if err := testDB.QueryRow(
		`SELECT COUNT(*) FROM bank_transactions WHERE event_id = $1`, eventID,
	).Scan(&count); err != nil {
		t.Fatalf("count after first call: %v", err)
	}
	if count != 1 {
		t.Fatalf("after first call row count = %d, want 1", count)
	}

	// Second call (replay) → 200 (idempotent), still exactly one row.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/bank-webhook", bytes.NewBuffer(payload))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Hub-Signature", sig)
	webhookHandler(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("replay should return 200 (idempotent), got %d body=%s",
			rec2.Code, rec2.Body.String())
	}
	if err := testDB.QueryRow(
		`SELECT COUNT(*) FROM bank_transactions WHERE event_id = $1`, eventID,
	).Scan(&count); err != nil {
		t.Fatalf("count after replay: %v", err)
	}
	if count != 1 {
		t.Fatalf("after replay row count = %d, want 1 (no duplicate row)", count)
	}
}

func TestGetTransactionsHandler(t *testing.T) {
	testDB := openTestDB(t)
	ensureSchema(t, testDB)
	withDB(t, testDB)

	eventID := fmt.Sprintf("EVT-LIST-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = testDB.Exec(`DELETE FROM bank_transactions WHERE event_id = $1`, eventID)
	})

	// insert one row directly
	if _, err := testDB.Exec(
		`INSERT INTO bank_transactions (event_id, account_number, amount, currency, sender_name, reference, status)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		eventID, "42", 99.99, "IDR", "Tester", "R-LIST", "COMPLETED",
	); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/transactions", nil)
	rec := httptest.NewRecorder()
	getTransactionsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Status string `json:"status"`
		Data   []struct {
			EventID string `json:"event_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if body.Status != "success" {
		t.Fatalf("status field = %q", body.Status)
	}
	found := false
	for _, row := range body.Data {
		if row.EventID == eventID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("event %s not in response data", eventID)
	}
}

// Optional integration test against a real mTLS server (skipped in -short and
// when the certs or the running server are absent).
func TestWebhookMTLS_Live(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live mTLS test in -short mode")
	}
	cert, err := tls.LoadX509KeyPair("client.crt", "client.key")
	if err != nil {
		t.Skipf("client cert missing: %v", err)
	}
	caPEM, err := os.ReadFile("ca.crt")
	if err != nil {
		t.Skipf("ca.crt missing: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to load CA")
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
				RootCAs:      pool,
			},
		},
		Timeout: 5 * time.Second,
	}

	payload := []byte(fmt.Sprintf(
		`{"event_id":"EVT-MTLS-%d","account_number":"1","amount":1,`+
			`"currency":"USD","sender_name":"X","reference":"R","status":"COMPLETED"}`,
		time.Now().UnixNano()))
	sig := signTestPayload(payload)

	req, _ := http.NewRequest(http.MethodPost,
		"https://localhost:8443/api/v1/bank-webhook",
		strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature", sig)

	resp, err := client.Do(req)
	if err != nil {
		t.Skipf("live server not reachable: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
