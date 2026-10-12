package main

import (
	"encoding/json"
	"net/http"
	"time"
)

// envelope is the response shape shared by every JSON endpoint:
//
//	{"data": ..., "error": null, "meta": {"timestamp": "<RFC3339>"}}
//
// Error is a pointer so that success serialises as null rather than "".
type envelope struct {
	Data  interface{}  `json:"data"`
	Error *string      `json:"error"`
	Meta  envelopeMeta `json:"meta"`
}

type envelopeMeta struct {
	Timestamp string `json:"timestamp"`
}

// apiResponse writes the standard envelope. An empty apiErr means success.
func apiResponse(w http.ResponseWriter, status int, data interface{}, apiErr string) {
	env := envelope{
		Data: data,
		Meta: envelopeMeta{Timestamp: time.Now().UTC().Format(time.RFC3339)},
	}
	if apiErr != "" {
		env.Error = &apiErr
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(env) //nolint:errcheck
}
