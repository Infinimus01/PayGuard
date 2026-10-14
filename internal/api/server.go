// Package api is the public JSON contract: POST /v1/score and POST /v1/outcomes.
// Scoring rules are not in this package. With no rules loaded, every new score is ALLOW.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Infinimus01/PayGuard/internal/tenant"
)

const maxBody = 1 << 20

// Server is the v1 HTTP API.
type Server struct {
	keys *tenant.KeyStore
	now  func() time.Time

	mu        sync.Mutex
	decisions map[string]storedDecision // decision_id -> row
	byIdem    map[string]string         // tenant|key -> decision_id
}

type storedDecision struct {
	Tenant     string
	DecisionID string
	IdemKey    string
	Intent     intent
	Action     string
	Degraded   bool
	Outcome    string
	FailReason string
	CreatedAt  time.Time
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

// New returns an API server. keys maps credentials to tenants.
func New(keys *tenant.KeyStore) *Server {
	return &Server{
		keys:      keys,
		now:       time.Now,
		decisions: make(map[string]storedDecision),
		byIdem:    make(map[string]string),
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

	s.mu.Lock()
	defer s.mu.Unlock()

	idemSlot := tenantID + "|" + idem
	if existingID, found := s.byIdem[idemSlot]; found {
		existing := s.decisions[existingID]
		if !sameIntent(existing.Intent, req.Intent) {
			writeError(w, http.StatusConflict, "idempotency_key_reused")
			return
		}
		writeJSON(w, http.StatusOK, toResponse(existing))
		return
	}

	row := storedDecision{
		Tenant:     tenantID,
		DecisionID: newID(),
		IdemKey:    idem,
		Intent:     req.Intent,
		Action:     "ALLOW",
		Degraded:   false,
		CreatedAt:  s.now().UTC(),
	}
	s.decisions[row.DecisionID] = row
	s.byIdem[idemSlot] = row.DecisionID
	writeJSON(w, http.StatusOK, toResponse(row))
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

	s.mu.Lock()
	defer s.mu.Unlock()

	row, found := s.decisions[req.DecisionID]
	if !found || row.Tenant != tenantID {
		writeError(w, http.StatusNotFound, "decision_not_found")
		return
	}
	if row.Outcome != "" && (row.Outcome != req.Status || row.FailReason != req.FailureReason) {
		writeError(w, http.StatusConflict, "outcome_already_reported")
		return
	}
	row.Outcome = req.Status
	row.FailReason = req.FailureReason
	s.decisions[row.DecisionID] = row
	writeJSON(w, http.StatusOK, map[string]string{
		"decision_id": row.DecisionID,
		"status":      row.Outcome,
	})
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

func sameIntent(a, b intent) bool {
	return a.PayerID == b.PayerID &&
		a.Amount == b.Amount &&
		a.Currency == b.Currency &&
		a.BeneficiaryID == b.BeneficiaryID &&
		a.Channel == b.Channel
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

func toResponse(row storedDecision) scoreResponse {
	return scoreResponse{
		Action:     row.Action,
		Score:      0,
		Rules:      []struct{}{},
		Features:   map[string]any{},
		DecisionID: row.DecisionID,
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
