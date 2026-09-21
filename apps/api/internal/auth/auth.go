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

// hashSlots bounds concurrent bcrypt work so a login storm queues instead of thrashing. It may use every
// core: matching is light (an order takes milliseconds), and a login storm is over in seconds.
var hashSlots = make(chan struct{}, max(2, runtime.NumCPU()))

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

// claims are a login token's contents: who, and which "session version" of that account it belongs to.
// Signing an account out raises its version, which cancels every token issued before.
type claims struct {
	jwt.RegisteredClaims
	Version int `json:"v,omitempty"`
}

func (s *Signer) Issue(accountID string, version int) (string, error) {
	now := s.now()
	c := claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   accountID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
		},
		Version: version,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(s.secret)
}

// Parse returns the account id in a valid, unexpired token.
func (s *Signer) Parse(token string) (string, error) {
	id, _, err := s.Verify(token)
	return id, err
}

// Verify returns the account id and session version in a valid, unexpired token.
func (s *Signer) Verify(token string) (string, int, error) {
	var c claims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return s.secret, nil
	}, jwt.WithTimeFunc(s.now), jwt.WithExpirationRequired(), jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || c.Subject == "" {
		return "", 0, ErrInvalidToken
	}
	return c.Subject, c.Version, nil
}
