package identityapi

import (
	"reflect"
	"strings"
	"testing"
)

func TestRegistrationPasswordPolicy(t *testing.T) {
	tests := []struct {
		name, password string
		options        []string
		valid          bool
	}{
		{"eight lowercase", "abcdefgh", nil, true},
		{"username and email allowed", "alicealice", nil, true},
		{"seven characters", "abcdefg", nil, false},
		{"unicode eight characters", "你好世界天地玄黄", nil, true},
		{"72 bytes", strings.Repeat("界", 24), nil, true},
		{"over 72 bytes", strings.Repeat("界", 25), nil, false},
		{"control character", "abcdefg\n", nil, false},
		{"organization Aa123", "abcdefgh", []string{"Aa123"}, false},
		{"organization special", "Abcdefg1", []string{"SpecialChar"}, false},
		{"organization no repeat", "Abcdeff1", []string{"NoRepeat"}, false},
		{"unknown policy", "Abcdefg1", []string{"Unknown"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validPassword(tt.password, tt.options); got != tt.valid {
				t.Fatalf("valid=%v, want %v", got, tt.valid)
			}
		})
	}
}

func TestRegistrationPasswordIssuesResponse(t *testing.T) {
	s, _, _ := setup(t)
	body := registration()
	body["password"] = "short"
	w := request(t, s, "/v1/identity/registrations", body, "password-issues")
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"password_issues":["too_short"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}

func TestPasswordIssueCodes(t *testing.T) {
	tests := []struct {
		password string
		options  []string
		want     []string
	}{
		{"abc", nil, []string{"too_short"}},
		{strings.Repeat("界", 25), nil, []string{"too_many_bytes"}},
		{"abcdefg\n", nil, []string{"control_character"}},
		{"abcdefgh", []string{"Aa123", "SpecialChar"}, []string{"letter_case_and_digit_required", "special_character_required"}},
		{"aabcde12", []string{"NoRepeat"}, []string{"repeated_character"}},
		{"abcdefgh", []string{"unknown"}, []string{"unsupported_policy"}},
	}
	for _, tt := range tests {
		if got := passwordIssues(tt.password, tt.options); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("issues=%v, want %v", got, tt.want)
		}
	}
}

func TestRegistrationAllowsUsernameAndEmailInPassword(t *testing.T) {
	s, issuer, _ := setup(t)
	issuer.orgPolicy = []string{"AtLeast8"}
	issuer.expectedPassword = "alicealice"
	body := registration()
	body["password"] = "alicealice"
	w := request(t, s, "/v1/identity/registrations", body, "username-password")
	if w.Code != 201 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}
