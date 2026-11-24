package billing

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestManagedModelsProfileJSON(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		valid       bool
	}{
		{"legacy", "", true},
		{"explicit empty map", `,"managed_models":{}`, true},
		{"ordered and disabled", `,"managed_models":{"chat":["deepseek-flash","fixture/fallback:free"],"ocr":[]}`, true},
		{"unknown operation", `,"managed_models":{"embedding":[]}`, false},
		{"null operation", `,"managed_models":{"chat":null}`, false},
		{"null model", `,"managed_models":{"chat":[null]}`, false},
		{"duplicate model", `,"managed_models":{"chat":["a","a"]}`, false},
		{"wrong type", `,"managed_models":{"chat":"a"}`, false},
		{"blank model", `,"managed_models":{"chat":[" "]}`, false},
		{"proxy punctuation and max length", `,"managed_models":{"chat":["._:/-` + strings.Repeat("a", 123) + `"]}`, true},
		{"over proxy limit", `,"managed_models":{"chat":["` + strings.Repeat("a", 129) + `"]}`, false},
		{"at sign", `,"managed_models":{"chat":["vendor/model@version"]}`, false},
		{"plus sign", `,"managed_models":{"chat":["vendor/model+version"]}`, false},
		{"wrong product", `,"managed_models":{"chat":[]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			product := "superwagie"
			if tc.name == "wrong product" {
				product = "agentwiki"
			}
			raw := `[{"product_id":"` + product + `","version":1,"subscription":{"enabled":true},"included_credits":{"enabled":true},"addons":{"enabled":true},"usage":{"enabled":true,"require_subscription":true,"funding_modes":["credits"],"default_funding_mode":"credits"` + tc.field + `}}]`
			out, err := ParseBillingProfiles([]byte(raw))
			if (err == nil) != tc.valid {
				t.Fatalf("valid = %v, got %v", tc.valid, err)
			}
			if !tc.valid {
				return
			}
			var expected []struct{ Usage map[string]json.RawMessage }
			if err := json.Unmarshal([]byte(raw), &expected); err != nil {
				t.Fatal(err)
			}
			result, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			var actual []struct{ Usage map[string]json.RawMessage }
			if err := json.Unmarshal(result, &actual); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual[0].Usage["managed_models"], expected[0].Usage["managed_models"]) {
				t.Fatalf("policy lost its order or presence: %s", result)
			}
		})
	}
}

func TestManagedModelWirePolicy(t *testing.T) {
	policy := ManagedModels{
		"chat": {"text"}, "vision": {"vision"}, "speech": {}, "transcription": {},
		"image-generation": {}, "image-edit": {}, "video": {}, "knowledge-embedding": {},
		"knowledge-rerank": {}, "ocr": {},
	}
	for _, tc := range []struct {
		wire, operation, model string
		allowed                bool
	}{
		{"chat.completions.create", "chat", "text", true},
		{"chat.completions.create", "vision", "vision", true},
		{"chat.completions.create", "chat", "vision", false},
		{"responses.create", "vision", "text", false},
		{"claude.messages.create", "", "text", false},
		{"gemini.generate", "speech", "text", false},
		{"bailian.audio.speech.create", "", "text", false},
		{"dashscope.audio.transcriptions.create", "", "text", false},
		{"gemini.images.generate", "", "text", false},
		{"images.edits.create", "", "text", false},
		{"videos.create", "", "text", false},
		{"embeddings.create", "", "text", false},
		{"multimodal.embeddings.create", "", "text", false},
		{"rerank.create", "", "text", false},
		{"ocr.jobs.create", "", "text", false},
		{"ocr.jobs.retrieve", "", "text", false},
		{"ocr.results.retrieve", "", "text", false},
		{"unknown.operation", "chat", "text", false},
	} {
		t.Run(strings.Join([]string{tc.wire, tc.operation, tc.model}, "/"), func(t *testing.T) {
			if got := policy.allows(tc.model, tc.wire, tc.operation); got != tc.allowed {
				t.Fatalf("allowed = %v, want %v", got, tc.allowed)
			}
		})
	}
	if !(ManagedModels{"speech": {}}).allows("text", "chat.completions.create", "") || !ManagedModels(nil).allows("text", "future.operation", "") {
		t.Fatal("an omitted operation or policy changed legacy admission")
	}
}

func TestManagedModelAdminEditSurvivesStartupProfileReplay(t *testing.T) {
	s := profileService(t)
	ctx := t.Context()
	staged, err := BillingPreset("superwagie", "hybrid")
	if err != nil {
		t.Fatal(err)
	}
	staged.Version = 1
	if err := s.ApplyConfiguredProfiles(ctx, []BillingProfile{staged}); err != nil {
		t.Fatal(err)
	}

	edit := staged
	edit.Usage.ManagedModels = ManagedModels{"chat": {"deepseek-flash"}, "vision": {}}
	saved, err := s.SaveProfile(ctx, "operator", "superwagie", edit)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyConfiguredProfiles(ctx, nil); err != nil {
		t.Fatalf("restart without a staged entry: %v", err)
	}
	if err := s.ApplyConfiguredProfiles(ctx, []BillingProfile{staged}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale file should conflict: %v", err)
	}
	staged.Version = saved.Version
	if err := s.ApplyConfiguredProfiles(ctx, []BillingProfile{staged}); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-version file with different policy should conflict: %v", err)
	}
	if err := s.ApplyConfiguredProfiles(ctx, []BillingProfile{saved}); err != nil {
		t.Fatalf("matching snapshot should replay: %v", err)
	}
	current, err := s.Profile(ctx, "superwagie")
	if err != nil || !reflect.DeepEqual(current, saved) {
		t.Fatalf("restart altered the saved profile: %+v, %v", current, err)
	}
}
