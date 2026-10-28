package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	httpSwagger "github.com/swaggo/http-swagger"
	"golang.org/x/time/rate"

	_ "bank-webhook-listener/docs"
)

type BankNotification struct {
	EventID       string  `json:"event_id"`
	AccountNumber string  `json:"account_number"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	SenderName    string  `json:"sender_name"`
	Reference     string  `json:"reference"`
	Status        string  `json:"status"`
}

type TransactionResponse struct {
	ID            int     `json:"id"`
	EventID       string  `json:"event_id"`
	AccountNumber string  `json:"account_number"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	SenderName    string  `json:"sender_name"`
	Reference     string  `json:"reference"`
	Status        string  `json:"status"`
	CreatedAt     string  `json:"created_at"`
}

var db *sql.DB
var webhookSecret = "KONGPAY_SUPER_SECRET_KEY_2026"

// --- PROMETHEUS METRICS ---
var (
	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kongpay_http_requests_total",
			Help: "Total HTTP requests processed by KongPay, labeled by method and endpoint.",
		},
		[]string{"method", "handler"},
	)

	transactionsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "kongpay_transactions_success_total",
			Help: "Total successful incoming bank transactions stored in database.",
		},
	)
)

func initMetrics() {
	prometheus.MustRegister(httpRequestsTotal)
	prometheus.MustRegister(transactionsTotal)
}

// --------------------------

type client struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

var (
	mu      sync.Mutex
	clients = make(map[string]*client)
)

func backgroundIPCleaner() {
	for {
		time.Sleep(time.Minute)
		mu.Lock()
		for ip, client := range clients {
			if time.Since(client.lastSeen) > 3*time.Minute {
				delete(clients, ip)
			}
		}
		mu.Unlock()
	}
}

func getVisitor(ip string) *rate.Limiter {
	mu.Lock()
	defer mu.Unlock()

	v, exists := clients[ip]
	if !exists {
		limiter := rate.NewLimiter(5, 10)
		clients[ip] = &client{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}

	v.lastSeen = time.Now()
	return v.limiter
}

func rateLimitMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}

		limiter := getVisitor(ip)
		if !limiter.Allow() {
			logrus.WithFields(logrus.Fields{"client_ip": ip}).Warn("Rate limit exceeded")
			http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
			return
		}

		next(w, r)
	}
}

func initDB() {
	var err error
	connStr := os.Getenv("DATABASE_URL")
	if connStr == "" {
		connStr = "postgres://postgres:password@localhost:5433/kongpay_db?sslmode=disable"
	}

	db, err = sql.Open("postgres", connStr)
	if err != nil {
		logrus.Fatalf("Gagal membuka koneksi database: %v", err)
	}

	err = db.Ping()
	if err != nil {
		logrus.Fatalf("Gagal terhubung ke database PostgreSQL KongPay: %v", err)
	}

	logrus.Info("Berhasil terhubung ke database PostgreSQL KongPay!")
}

func verifyHMAC(body []byte, incomingSignature string, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expectedMAC := mac.Sum(nil)
	expectedSignature := hex.EncodeToString(expectedMAC)

	return hmac.Equal([]byte(incomingSignature), []byte(expectedSignature))
}

// @Title KongPay API
// @Version 1.0
// @Description Backend API dengan mTLS, Rate Limiting, Logrus & Prometheus Metrics.
// @Host localhost:8443
// @BasePath /api/v1
func webhookHandler(w http.ResponseWriter, r *http.Request) {
	httpRequestsTotal.WithLabelValues(r.Method, "webhook").Inc()
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Gagal membaca body", http.StatusBadRequest)
		return
	}

	incomingSig := r.Header.Get("X-Hub-Signature")
	if incomingSig == "" || !verifyHMAC(bodyBytes, incomingSig, webhookSecret) {
		logrus.WithFields(logrus.Fields{"client_ip": ip}).Error("Invalid signature or unauthorized")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var notif BankNotification
	if err := json.Unmarshal(bodyBytes, &notif); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	query := `
		INSERT INTO bank_transactions (event_id, account_number, amount, currency, sender_name, reference, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (event_id) DO NOTHING
	`
	res, err := db.Exec(query, notif.EventID, notif.AccountNumber, notif.Amount, notif.Currency, notif.SenderName, notif.Reference, notif.Status)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	inserted, _ := res.RowsAffected()
	if inserted == 0 {
		// Idempotent replay: this event_id was already stored.
		// Do NOT increment the success counter.
		logrus.WithField("event_id", notif.EventID).
			Info("Duplicate event_id, transaction already stored (idempotent no-op)")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "success",
			"message": "duplicate event_id, already stored (idempotent)",
		})
		return
	}

	// First-time insert succeeded.
	transactionsTotal.Inc()

	logrus.WithFields(logrus.Fields{
		"event_id": notif.EventID,
		"amount":   notif.Amount,
		"currency": notif.Currency,
	}).Info("Transaksi berhasil dicatat dan metrik Prometheus diperbarui")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": "metrics recorded, mTLS verified and saved",
	})
	return

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
}

func getTransactionsHandler(w http.ResponseWriter, r *http.Request) {
	httpRequestsTotal.WithLabelValues(r.Method, "transactions").Inc()

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rows, err := db.Query(`
		SELECT id, event_id, account_number, amount, currency, sender_name, reference, status, created_at 
		FROM bank_transactions ORDER BY created_at DESC LIMIT 50
	`)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var transactions []TransactionResponse
	for rows.Next() {
		var tx TransactionResponse
		var createdAt string
		rows.Scan(&tx.ID, &tx.EventID, &tx.AccountNumber, &tx.Amount, &tx.Currency, &tx.SenderName, &tx.Reference, &tx.Status, &createdAt)
		tx.CreatedAt = createdAt
		transactions = append(transactions, tx)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "data": transactions})
}

func main() {
	logrus.SetFormatter(&logrus.JSONFormatter{TimestampFormat: "2006-01-02 15:04:05"})
	logrus.SetLevel(logrus.InfoLevel)

	initMetrics()
	initDB()
	defer db.Close()

	go backgroundIPCleaner()

	// 1. Jalankan server terpisah khusus untuk Prometheus Scraper Metrics di port 9090
	go func() {
		muxMetrics := http.NewServeMux()
		muxMetrics.Handle("/metrics", promhttp.Handler())
		logrus.Info("Prometheus metrics server berjalan di http://localhost:9090/metrics")
		if err := http.ListenAndServe(":9090", muxMetrics); err != nil {
			logrus.Errorf("Metrics server error: %v", err)
		}
	}()

	// 2. Server utama mTLS untuk webhook & API klien di port 8443
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/bank-webhook", rateLimitMiddleware(webhookHandler))
	mux.HandleFunc("/api/v1/transactions", getTransactionsHandler)
	mux.Handle("/swagger/", httpSwagger.WrapHandler)

	caCert, err := os.ReadFile("ca.crt")
	if err != nil {
		logrus.Fatalf("Gagal membaca CA certificate: %v", err)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	tlsConfig := &tls.Config{
		ClientCAs:  caCertPool,
		ClientAuth: tls.RequireAndVerifyClientCert,
		MinVersion: tls.VersionTLS12,
	}

	server := &http.Server{
		Addr:      ":8443",
		Handler:   mux,
		TLSConfig: tlsConfig,
	}

	logrus.Info("Server KongPay mTLS + Prometheus berjalan aman di port 8443...")
	logrus.Fatal(server.ListenAndServeTLS("server.crt", "server.key"))
}
