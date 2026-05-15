package redact

import "testing"

func TestValueRedactsSensitiveKeysAndLikelySecrets(t *testing.T) {
	in := map[string]interface{}{
		"api_token": "plain-value",
		"generic":   "ghp_abcdefghijklmnopqrstuvwxyz123456",
		"commit":    "0123456789abcdef0123456789abcdef01234567",
		"nested": map[string]interface{}{
			"password": "short",
		},
	}

	out, ok := Value("", in).(map[string]interface{})
	if !ok {
		t.Fatalf("Value returned %#v, want map", out)
	}
	if out["api_token"] != Redacted {
		t.Fatalf("api_token = %#v, want redacted", out["api_token"])
	}
	if out["generic"] != Redacted {
		t.Fatalf("generic = %#v, want redacted", out["generic"])
	}
	if out["commit"] != in["commit"] {
		t.Fatalf("commit = %#v, want allowlisted hash", out["commit"])
	}
	nested := out["nested"].(map[string]interface{})
	if nested["password"] != Redacted {
		t.Fatalf("nested password = %#v, want redacted", nested["password"])
	}
}
