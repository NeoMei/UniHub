package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"unihub/internal/accounts"
	"unihub/internal/billing"
	"unihub/internal/modelaccess"
	"unihub/internal/wallet"
)

// A closed authority pool never connects or mutates state. Valid new-purpose
// requests must report unavailable authority, while invalid shapes fail first.
func TestKnowledgeDispatchUnavailableAuthorityAndScopeGuards(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://unused:unused@127.0.0.1:1/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	store := accounts.New(pool)
	const service = "test-only-router-service-key-over-32-bytes"
	h := New(verifier{}, store, wallet.New(pool, store, noLedger{}), Options{Billing: billing.New(pool, store, noLedger{}), ModelAccess: modelaccess.New(pool, store, nil), ServiceToken: service})
	for _, tc := range []struct {
		name, token, model, instance, wire string
		want                               int
	}{
		{"embedding authority unavailable", "uh_model_" + strings.Repeat("a", 64), "unihub-text-embedding-v4", "default", "embeddings.create", 503},
		{"rerank authority unavailable", "uh_model_" + strings.Repeat("a", 64), "qwen3-rerank", "default", "rerank.create", 503},
		{"api key", "uh_key_" + strings.Repeat("a", 64), "unihub-text-embedding-v4", "default", "embeddings.create", 403},
		{"empty instance", "uh_model_" + strings.Repeat("a", 64), "unihub-text-embedding-v4", "", "embeddings.create", 403},
		{"wrong model", "uh_model_" + strings.Repeat("a", 64), "other", "default", "embeddings.create", 403},
		{"crossed wire", "uh_model_" + strings.Repeat("a", 64), "qwen3-rerank", "default", "embeddings.create", 403},
		{"metering operation is not wire", "uh_model_" + strings.Repeat("a", 64), "qwen3-rerank", "default", "rerank", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"purpose": "knowledge_dispatch", "token": tc.token, "model": tc.model, "instance_id": tc.instance, "wire_operation": tc.wire})
			w := call(h, "POST", "/internal/v1/model-credentials/verify", service, string(body), "")
			if w.Code != tc.want {
				t.Fatalf("status %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
