package auth_test

import (
	"strings"
	"testing"
	"time"

	"stockastic/api/internal/auth"
)

var secret = []byte(strings.Repeat("s", 40))

func TestTokenRoundTrip(t *testing.T) {
	s, _ := auth.NewSigner(secret, time.Hour)
	tok, err := s.Issue("acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if id, err := s.Parse(tok); err != nil || id != "acct-1" {
		t.Fatalf("Parse = %q, %v", id, err)
	}
}

func TestTokensThatMustBeRejected(t *testing.T) {
	s, _ := auth.NewSigner(secret, time.Hour)
	tok, _ := s.Issue("acct-1")
	other, _ := auth.NewSigner([]byte(strings.Repeat("x", 40)), time.Hour)
	expired, _ := auth.NewSigner(secret, -time.Minute)
	old, _ := expired.Issue("acct-1")

	tampered := tok[:len(tok)-2] + "aa"
	if tok[len(tok)-2:] == "aa" {
		tampered = tok[:len(tok)-2] + "bb"
	}
	for name, bad := range map[string]string{
		"empty":            "",
		"garbage":          "not.a.token",
		"signed elsewhere": mustIssue(t, other),
		"expired":          old,
		"tampered":         tampered,
		"alg none":         "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiJhZG1pbiJ9.",
	} {
		if _, err := s.Parse(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func mustIssue(t *testing.T, s *auth.Signer) string {
	tok, err := s.Issue("acct-1")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestShortSecretsAreRefused(t *testing.T) {
	if _, err := auth.NewSigner([]byte("short"), time.Hour); err == nil {
		t.Fatal("a short secret was accepted")
	}
}

func TestPasswordHashing(t *testing.T) {
	h, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if h == "correct horse" || !auth.CheckPassword(h, "correct horse") || auth.CheckPassword(h, "wrong") {
		t.Fatal("password hashing is broken")
	}
	if auth.CheckPassword("", "anything") {
		t.Fatal("an empty hash accepted a password")
	}
}
