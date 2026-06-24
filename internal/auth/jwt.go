package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	UserID int64  `json:"user_id"`
	Email  string `json:"email"`
	jwt.RegisteredClaims
}

type JWTManager struct {
	secret     []byte
	expiration time.Duration
}

// ErrInsecureJWTSecret 表示 JWT secret 为空或为已知默认值，存在 token 伪造风险
var ErrInsecureJWTSecret = errors.New("insecure JWT secret: must be non-empty and not the known default value (configure jwt_secret in server.json or JWT_SECRET env)")

// defaultJWTSecret 已知的不安全默认值，仅用于比对拒绝
const defaultJWTSecret = "genericagent-default-jwt-secret-change-me"

func NewJWTManager(secret string, expirationHours int) (*JWTManager, error) {
	if secret == "" || secret == defaultJWTSecret {
		return nil, ErrInsecureJWTSecret
	}
	if expirationHours <= 0 {
		expirationHours = 72
	}
	return &JWTManager{
		secret:     []byte(secret),
		expiration: time.Duration(expirationHours) * time.Hour,
	}, nil
}

func (m *JWTManager) GenerateToken(userID int64, email string) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		Email:  email,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(m.expiration)),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    "genericagent",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.secret)
}

func (m *JWTManager) ParseToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}
