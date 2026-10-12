package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type decodedEnvelope struct {
	Data  json.RawMessage        `json:"data"`
	Error *string                `json:"error"`
	Meta  map[string]interface{} `json:"meta"`
}

func decodeEnvelope(t *testing.T, body []byte) decodedEnvelope {
	t.Helper()
	var env decodedEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("response is not a JSON envelope: %v\nbody: %s", err, body)
	}
	return env
}

func assertTimestamp(t *testing.T, env decodedEnvelope) {
	t.Helper()
	ts, ok := env.Meta["timestamp"].(string)
	if !ok {
		t.Fatalf("meta.timestamp missing or not a string: %#v", env.Meta)
	}
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Fatalf("meta.timestamp %q is not RFC3339: %v", ts, err)
	}
}

func TestAPIResponse_Success(t *testing.T) {
	rec := httptest.NewRecorder()
	apiResponse(rec, http.StatusOK, map[string]int{"indexed": 3}, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	if env.Error != nil {
		t.Fatalf("error = %q, want null", *env.Error)
	}
	if string(env.Data) != `{"indexed":3}` {
		t.Fatalf("data = %s", env.Data)
	}
	assertTimestamp(t, env)
}

func TestAPIResponse_ErrorHasNullDataAndMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	apiResponse(rec, http.StatusBadRequest, nil, "path required")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	env := decodeEnvelope(t, rec.Body.Bytes())
	if env.Error == nil || *env.Error != "path required" {
		t.Fatalf("error = %v, want \"path required\"", env.Error)
	}
	if string(env.Data) != "null" {
		t.Fatalf("data = %s, want null", env.Data)
	}
	assertTimestamp(t, env)
}

func TestAPIResponse_ErrorKeyIsLiteralNullOnSuccess(t *testing.T) {
	rec := httptest.NewRecorder()
	apiResponse(rec, http.StatusOK, []int{}, "")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["error"]) != "null" {
		t.Fatalf(`"error" = %s, want null (not "")`, raw["error"])
	}
	if string(raw["data"]) != "[]" {
		t.Fatalf(`"data" = %s, want []`, raw["data"])
	}
}
