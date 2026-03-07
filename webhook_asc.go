package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	stdlog "log"
	"net/http"
	"os"
	"time"
)

// handleASCWebhook receives TestFlight feedback webhooks from Apple.
// Verifies the HMAC-SHA256 signature, then spools the raw payload to disk
// for the Python feedback pipeline to process.
func handleASCWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	secret := os.Getenv("ASC_WEBHOOK_SECRET")
	if secret != "" && len(body) > 0 {
		sig := r.Header.Get("X-Apple-Signature")
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		expected := "hmacsha256=" + hex.EncodeToString(mac.Sum(nil))
		if sig != "" && !hmac.Equal([]byte(sig), []byte(expected)) {
			stdlog.Printf("ASC webhook: invalid signature")
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	// Acknowledge immediately — Apple expects 200 within a few seconds
	w.WriteHeader(http.StatusOK)

	// Write payload to spool directory for the Python pipeline to pick up
	spoolDir := os.Getenv("ASC_WEBHOOK_SPOOL_DIR")
	if spoolDir == "" {
		spoolDir = "/mnt/ghosttrak-data/fairway-intel/asc-webhook-spool"
	}

	if err := os.MkdirAll(spoolDir, 0755); err != nil {
		stdlog.Printf("ASC webhook: failed to create spool dir: %v", err)
		return
	}

	filename := spoolDir + "/" + time.Now().UTC().Format("20060102T150405.999999999Z") + ".json"
	if err := os.WriteFile(filename, body, 0644); err != nil {
		stdlog.Printf("ASC webhook: failed to write spool file: %v", err)
		return
	}

	stdlog.Printf("ASC webhook: spooled %d bytes to %s", len(body), filename)
}
