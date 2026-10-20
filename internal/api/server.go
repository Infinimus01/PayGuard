// Package api is the public JSON contract: POST /v1/score and POST /v1/outcomes.
// Scoring rules are not in this package. With no rules loaded, every new score is ALLOW.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Infinimus01/PayGuard/internal/store"
	"github.com/Infinimus01/PayGuard/internal/tenant"
)

const maxBody = 1 << 20

// Server is the v1 HTTP API.
type Server struct {
	keys  *tenant.KeyStore
	store store.Store
	now   func() time.Time
}

type intent struct {
	PayerID       string `json:"payer_id"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	BeneficiaryID string `json:"beneficiary_id"`
	Channel       string `json:"channel"`
}

type scoreRequest struct {
	Intent  intent          `json:"intent"`
	Device  json.RawMessage `json:"device,omitempty"`
	Network json.RawMessage `json:"network,omitempty"`
	Session json.RawMessage `json:"session,omitempty"`
}

type scoreResponse struct {
	Action     string         `json:"action"`
	Score      int            `json:"score"`
	Rules      []struct{}     `json:"rules"`
	Features   map[string]any `json:"features"`
	DecisionID string         `json:"decision_id"`
	Degraded   bool           `json:"degraded"`
}

type outcomeRequest struct {
	DecisionID    string `json:"decision_id"`
	Status        string `json:"status"`
	FailureReason string `json:"failure_reason,omitempty"`
}

// New returns an API server. keys maps credentials to tenants. decisions persist in st.
func New(keys *tenant.KeyStore, st store.Store) *Server {
	return &Server{
		keys:  keys,
		store: st,
		now:   time.Now,
	}
}

// Handler is the public mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/score", s.score)
	mux.HandleFunc("POST /v1/outcomes", s.outcome)
	return mux
}

func (s *Server) score(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	idem := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idem == "" {
		writeError(w, http.StatusBadRequest, "missing_idempotency_key")
		return
	}
	var req scoreRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if err := validateIntent(req.Intent); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	existing, found, err := s.store.FindByIdempotency(r.Context(), tenantID, idem)
	if err != nil {
		log.Printf("score lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if found {
		if !sameIntent(existing.Intent, toStoreIntent(req.Intent)) {
			writeError(w, http.StatusConflict, "idempotency_key_reused")
			return
		}
		writeJSON(w, http.StatusOK, toResponse(existing))
		return
	}

	row := store.Decision{
		ID:             newID(),
		TenantID:       tenantID,
		IdempotencyKey: idem,
		Intent:         toStoreIntent(req.Intent),
		Action:         "ALLOW",
		CreatedAt:      s.now().UTC(),
	}
	saved, created, err := s.store.InsertDecision(r.Context(), row)
	if err != nil {
		log.Printf("score insert: %v", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if !created && !sameIntent(saved.Intent, row.Intent) {
		writeError(w, http.StatusConflict, "idempotency_key_reused")
		return
	}
	writeJSON(w, http.StatusOK, toResponse(saved))
}

func (s *Server) outcome(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	var req outcomeRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	req.DecisionID = strings.TrimSpace(req.DecisionID)
	req.Status = strings.TrimSpace(req.Status)
	if req.DecisionID == "" || !validStatus(req.Status) {
		writeError(w, http.StatusBadRequest, "invalid_outcome")
		return
	}
	if req.Status == "failed" && !validFailure(req.FailureReason) {
		writeError(w, http.StatusBadRequest, "invalid_failure_reason")
		return
	}

	err := s.store.ReportOutcome(r.Context(), tenantID, req.DecisionID, req.Status, req.FailureReason)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "decision_not_found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "outcome_already_reported")
	case err != nil:
		log.Printf("outcome: %v", err)
		writeError(w, http.StatusInternalServerError, "internal")
	default:
		writeJSON(w, http.StatusOK, map[string]string{
			"decision_id": req.DecisionID,
			"status":      req.Status,
		})
	}
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	key, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || strings.TrimSpace(key) == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	tenantID, ok := s.keys.Tenant(strings.TrimSpace(key))
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return "", false
	}
	return tenantID, true
}

func validateIntent(in intent) error {
	switch {
	case strings.TrimSpace(in.PayerID) == "":
		return errors.New("missing_payer_id")
	case strings.TrimSpace(in.BeneficiaryID) == "":
		return errors.New("missing_beneficiary_id")
	case in.Amount <= 0:
		return errors.New("invalid_amount")
	case len(in.Currency) != 3:
		return errors.New("invalid_currency")
	}
	switch in.Channel {
	case "UPI", "CARD", "WALLET", "PAYOUT":
		return nil
	default:
		return errors.New("invalid_channel")
	}
}

func sameIntent(a, b store.Intent) bool {
	return a.PayerID == b.PayerID &&
		a.Amount == b.Amount &&
		a.Currency == b.Currency &&
		a.BeneficiaryID == b.BeneficiaryID &&
		a.Channel == b.Channel
}

func toStoreIntent(in intent) store.Intent {
	return store.Intent{
		PayerID:       in.PayerID,
		Amount:        in.Amount,
		Currency:      in.Currency,
		BeneficiaryID: in.BeneficiaryID,
		Channel:       in.Channel,
	}
}

func validStatus(status string) bool {
	switch status {
	case "completed", "failed", "step_up_passed", "step_up_failed",
		"review_fraud", "review_legit", "chargeback", "scam_confirmed":
		return true
	default:
		return false
	}
}

func validFailure(reason string) bool {
	switch reason {
	case "insufficient_funds", "user_cancelled", "technical":
		return true
	default:
		return false
	}
}

func toResponse(row store.Decision) scoreResponse {
	return scoreResponse{
		Action:     row.Action,
		Score:      row.Score,
		Rules:      []struct{}{},
		Features:   map[string]any{},
		DecisionID: row.ID,
		Degraded:   row.Degraded,
	}
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	enc := hex.EncodeToString(b[:])
	return enc[0:8] + "-" + enc[8:12] + "-" + enc[12:16] + "-" + enc[16:20] + "-" + enc[20:32]
}

func decode(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
