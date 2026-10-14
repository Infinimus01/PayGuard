package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Infinimus01/PayGuard/internal/tenant"
)

func testServer() *Server {
	return New(tenant.Parse("test-key:tenant_a,other-key:tenant_b"))
}

func scoreBody() string {
	return `{
		"intent": {
			"payer_id": "user_1",
			"amount": 500000,
			"currency": "INR",
			"beneficiary_id": "vpa_1@bank",
			"channel": "UPI"
		}
	}`
}

func do(s *Server, method, path, key, idem, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestScoreRequiresAuthAndIdempotency(t *testing.T) {
	s := testServer()
	if rec := do(s, http.MethodPost, "/v1/score", "", "k1", scoreBody()); rec.Code != http.StatusUnauthorized {
		t.Fatalf("auth: got %d", rec.Code)
	}
	if rec := do(s, http.MethodPost, "/v1/score", "nope", "k1", scoreBody()); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad key: got %d", rec.Code)
	}
	if rec := do(s, http.MethodPost, "/v1/score", "test-key", "", scoreBody()); rec.Code != http.StatusBadRequest {
		t.Fatalf("idem: got %d", rec.Code)
	}
}

func TestScoreAllowAndReplay(t *testing.T) {
	s := testServer()
	first := do(s, http.MethodPost, "/v1/score", "test-key", "pay_1", scoreBody())
	if first.Code != http.StatusOK {
		t.Fatalf("score: %d %s", first.Code, first.Body.String())
	}
	var a scoreResponse
	if err := json.Unmarshal(first.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Action != "ALLOW" || a.Degraded || a.DecisionID == "" {
		t.Fatalf("response: %+v", a)
	}

	second := do(s, http.MethodPost, "/v1/score", "test-key", "pay_1", scoreBody())
	var b scoreResponse
	_ = json.Unmarshal(second.Body.Bytes(), &b)
	if b.DecisionID != a.DecisionID {
		t.Fatalf("idempotency did not replay: %s vs %s", a.DecisionID, b.DecisionID)
	}

	changed := `{
		"intent": {
			"payer_id": "user_1",
			"amount": 1,
			"currency": "INR",
			"beneficiary_id": "vpa_1@bank",
			"channel": "UPI"
		}
	}`
	conflict := do(s, http.MethodPost, "/v1/score", "test-key", "pay_1", changed)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict: got %d", conflict.Code)
	}
}

func TestOutcomeIsTenantScoped(t *testing.T) {
	s := testServer()
	rec := do(s, http.MethodPost, "/v1/score", "test-key", "pay_9", scoreBody())
	var scored scoreResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &scored)

	other := do(s, http.MethodPost, "/v1/outcomes", "other-key", "",
		`{"decision_id":"`+scored.DecisionID+`","status":"completed"}`)
	if other.Code != http.StatusNotFound {
		t.Fatalf("cross tenant: got %d", other.Code)
	}

	ok := do(s, http.MethodPost, "/v1/outcomes", "test-key", "",
		`{"decision_id":"`+scored.DecisionID+`","status":"completed"}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("outcome: %d %s", ok.Code, ok.Body.String())
	}
	again := do(s, http.MethodPost, "/v1/outcomes", "test-key", "",
		`{"decision_id":"`+scored.DecisionID+`","status":"completed"}`)
	if again.Code != http.StatusOK {
		t.Fatalf("replay outcome: %d", again.Code)
	}
	clash := do(s, http.MethodPost, "/v1/outcomes", "test-key", "",
		`{"decision_id":"`+scored.DecisionID+`","status":"chargeback"}`)
	if clash.Code != http.StatusConflict {
		t.Fatalf("outcome clash: %d", clash.Code)
	}
}

func TestFailedOutcomeNeedsReason(t *testing.T) {
	s := testServer()
	rec := do(s, http.MethodPost, "/v1/score", "test-key", "pay_f", scoreBody())
	var scored scoreResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &scored)
	bad := do(s, http.MethodPost, "/v1/outcomes", "test-key", "",
		`{"decision_id":"`+scored.DecisionID+`","status":"failed"}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("missing reason: %d", bad.Code)
	}
	good := do(s, http.MethodPost, "/v1/outcomes", "test-key", "",
		`{"decision_id":"`+scored.DecisionID+`","status":"failed","failure_reason":"insufficient_funds"}`)
	if good.Code != http.StatusOK {
		t.Fatalf("failed outcome: %d %s", good.Code, good.Body.String())
	}
}
