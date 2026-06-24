package auth

import (
	"testing"
)

// m1: JWT secret 安全检查
func TestJWTManager_RejectsInsecureSecret(t *testing.T) {
	cases := []struct {
		name   string
		secret string
	}{
		{"empty", ""},
		{"default", "genericagent-default-jwt-secret-change-me"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewJWTManager(tc.secret, 72)
			if err != ErrInsecureJWTSecret {
				t.Errorf("expected ErrInsecureJWTSecret for secret=%q, got %v", tc.secret, err)
			}
		})
	}
}

func TestJWTManager_AcceptsSecureSecret(t *testing.T) {
	mgr, err := NewJWTManager("a-very-secure-and-long-secret-key-1234567890", 72)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	token, err := mgr.GenerateToken(42, "user@example.com")
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}
	if token == "" {
		t.Fatal("token should not be empty")
	}

	claims, err := mgr.ParseToken(token)
	if err != nil {
		t.Fatalf("ParseToken failed: %v", err)
	}
	if claims.UserID != 42 {
		t.Errorf("UserID mismatch: got %d, want 42", claims.UserID)
	}
	if claims.Email != "user@example.com" {
		t.Errorf("Email mismatch: got %s", claims.Email)
	}
}

func TestJWTManager_DefaultExpirationWhenZero(t *testing.T) {
	mgr, err := NewJWTManager("secure-secret-key-for-testing-only-xxx", 0)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	// expirationHours <= 0 应被替换为 72h
	if mgr.expiration != 72*60*60*1e9 { // 72h in nanoseconds
		t.Errorf("expected 72h default expiration, got %v", mgr.expiration)
	}
}

func TestJWTManager_RejectsTamperedToken(t *testing.T) {
	mgr, _ := NewJWTManager("secure-secret-key-for-testing-only-xxx", 72)
	token, _ := mgr.GenerateToken(1, "a@b.com")

	// 篡改 token
	tampered := token[:len(token)-2] + "XX"
	_, err := mgr.ParseToken(tampered)
	if err == nil {
		t.Error("expected error for tampered token")
	}
}
