package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type Agent struct {
	ID      string
	Name    string
	Enabled bool
	Owner   *string
}

type User struct {
	ID    string
	Email string
	Name  string
	Role  string
}

type JWTClaims struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

func hashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

func checkPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func generateJWT(userID, email, role string, secret string) (string, error) {
	claims := JWTClaims{
		UserID: userID,
		Email:  email,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func validateJWT(tokenStr, secret string) (*JWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &JWTClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*JWTClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

func resolveAgentFromDB(ctx context.Context, pool *pgxpool.Pool, apiKey string) (*Agent, error) {
	hash := sha256.Sum256([]byte(apiKey))
	keyHash := hex.EncodeToString(hash[:])

	var agent Agent
	err := pool.QueryRow(ctx,
		`SELECT id, name, enabled, owner FROM agents WHERE key_hash = $1 AND enabled = true`,
		keyHash,
	).Scan(&agent.ID, &agent.Name, &agent.Enabled, &agent.Owner)
	if err != nil {
		return nil, fmt.Errorf("invalid or disabled API key")
	}
	return &agent, nil
}

func resolveUserFromDB(ctx context.Context, pool *pgxpool.Pool, token, secret string) (*User, error) {
	claims, err := validateJWT(token, secret)
	if err != nil {
		return nil, err
	}

	var user User
	err = pool.QueryRow(ctx,
		`SELECT id, email, name, role FROM users WHERE id = $1`,
		claims.UserID,
	).Scan(&user.ID, &user.Email, &user.Name, &user.Role)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	return &user, nil
}
