// Package auth hashes passwords and issues/verifies the JWT shared by REST and WebSocket.
//
// The token names only the account. Role, admin status and disqualification are looked up live on every
// request, so a promotion or a disqualification takes effect at once rather than when the token expires.
package auth

import (
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidToken = errors.New("invalid_token")

// hashSlots bounds concurrent bcrypt work: hundreds of teams logging in at the same second must not
// starve matching of CPU.
var hashSlots = make(chan struct{}, max(2, runtime.NumCPU()/2))

func HashPassword(pw string) (string, error) {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	b, err := bcrypt.GenerateFromPassword([]byte(pw), 10)
	return string(b), err
}

func CheckPassword(hash, pw string) bool {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

type Signer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewSigner requires a secret of at least 32 bytes: a short secret can be brute-forced offline.
func NewSigner(secret []byte, ttl time.Duration) (*Signer, error) {
	if len(secret) < 32 {
		return nil, errors.New("auth: JWT secret must be at least 32 bytes")
	}
	return &Signer{secret: secret, ttl: ttl, now: time.Now}, nil
}

func (s *Signer) Issue(accountID string) (string, error) {
	now := s.now()
	claims := jwt.RegisteredClaims{
		Subject:   accountID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
}

// Parse returns the account id in a valid, unexpired token.
func (s *Signer) Parse(token string) (string, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return s.secret, nil
	}, jwt.WithTimeFunc(s.now), jwt.WithExpirationRequired(), jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || claims.Subject == "" {
		return "", ErrInvalidToken
	}
	return claims.Subject, nil
}
