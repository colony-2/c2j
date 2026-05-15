package redact

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

const Redacted = "[REDACTED]"

var sensitiveKeyFragments = []string{
	"api_key",
	"apikey",
	"auth",
	"bearer",
	"credential",
	"passwd",
	"password",
	"private_key",
	"secret",
	"token",
}

var secretValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)-----BEGIN (?:RSA |OPENSSH |EC |DSA )?PRIVATE KEY-----`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|github_pat)_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{20,}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
}

var allowValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^[a-f0-9]{40}$`),
	regexp.MustCompile(`^[a-f0-9]{64}$`),
	regexp.MustCompile(`^[0-9a-fA-F-]{32,36}$`),
}

func Value(path string, value interface{}) interface{} {
	return redactValue(path, value)
}

func redactValue(path string, value interface{}) interface{} {
	if pathLooksSensitive(path) {
		return Redacted
	}

	switch typed := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			out[key] = redactValue(joinPath(path, key), item)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = redactValue(path, item)
		}
		return out
	case string:
		if stringLooksSecret(typed) {
			return Redacted
		}
		return typed
	default:
		return typed
	}
}

func pathLooksSensitive(path string) bool {
	lower := strings.ToLower(path)
	parts := strings.FieldsFunc(lower, func(r rune) bool {
		return r == '.' || r == '/' || r == '[' || r == ']' || r == '-' || unicode.IsSpace(r)
	})
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		for _, fragment := range sensitiveKeyFragments {
			if fragment == "auth" {
				if part == "auth" || strings.HasSuffix(part, "_auth") || strings.HasPrefix(part, "auth_") {
					return true
				}
				continue
			}
			if part == fragment || strings.Contains(part, fragment) {
				return true
			}
		}
	}
	return false
}

func stringLooksSecret(value string) bool {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) < 16 {
		return false
	}
	for _, allow := range allowValuePatterns {
		if allow.MatchString(trimmed) {
			return false
		}
	}
	for _, pattern := range secretValuePatterns {
		if pattern.MatchString(trimmed) {
			return true
		}
	}
	if likelyHighEntropyToken(trimmed) {
		return true
	}
	return false
}

func likelyHighEntropyToken(value string) bool {
	if len(value) < 24 || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	letters, digits, symbols := 0, 0, 0
	for _, r := range value {
		switch {
		case unicode.IsLetter(r):
			letters++
		case unicode.IsDigit(r):
			digits++
		case strings.ContainsRune("_-+/=.", r):
			symbols++
		default:
			return false
		}
	}
	if letters == 0 || digits == 0 || symbols > len(value)/3 {
		return false
	}
	return shannonEntropy(value) >= 4.2
}

func shannonEntropy(value string) float64 {
	counts := map[rune]float64{}
	for _, r := range value {
		counts[r]++
	}
	length := float64(len([]rune(value)))
	if length == 0 {
		return 0
	}
	entropy := 0.0
	for _, count := range counts {
		p := count / length
		entropy -= p * math.Log2(p)
	}
	return entropy
}

func joinPath(base string, key string) string {
	key = strings.TrimSpace(key)
	if base == "" {
		return key
	}
	if key == "" {
		return base
	}
	return base + "." + key
}
