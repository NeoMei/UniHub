package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRecoveryRouteDoesNotRecurseIntoItself(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	recovery := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	request := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound)
	})
	h := recoveryRoute(next, recovery, request)

	for _, tc := range []struct {
		name string
		path string
		want int
	}{
		{name: "recovery", path: "/auth/recovery", want: http.StatusAccepted},
		{name: "recovery request", path: "/auth/recovery/request", want: http.StatusFound},
		{name: "normal", path: "/v1/me", want: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			res := httptest.NewRecorder()
			h.ServeHTTP(res, req)
			if res.Code != tc.want {
				t.Fatalf("status = %d, want %d", res.Code, tc.want)
			}
		})
	}
}
