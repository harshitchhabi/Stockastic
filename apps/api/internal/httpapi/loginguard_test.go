package httpapi

import "testing"

func TestALockIsPerEmailAndAddressSoOutsidersCannotLockATeamOut(t *testing.T) {
	g := newLoginGuard()
	for i := 0; i < pairFailures; i++ {
		g.failed("victim@x.io", "203.0.113.5") // an attacker somewhere else
	}
	if g.blocked("victim@x.io", "203.0.113.5") == 0 {
		t.Fatal("the attacker's own address was not slowed")
	}
	if g.blocked("victim@x.io", "198.51.100.9") != 0 {
		t.Fatal("the team's own address was locked by guesses from another address")
	}
	if g.blocked("other@x.io", "203.0.113.5") != 0 {
		t.Fatal("another email from the attacker's address was locked")
	}
}

func TestGuessingSpreadOverManyAddressesIsStillBounded(t *testing.T) {
	g := newLoginGuard()
	for i := 0; i < mailFailures; i++ {
		g.failed("victim@x.io", string(rune('a'+i%26))+string(rune('a'+i/26))) // a different address each time
	}
	if g.blocked("victim@x.io", "brand-new-address") == 0 {
		t.Fatal("100 wrong guesses from 100 addresses did not lock the email")
	}
}

func TestAGoodSignInAndTheOrganiserButtonLiftLocks(t *testing.T) {
	g := newLoginGuard()
	for i := 0; i < pairFailures; i++ {
		g.failed("a@x.io", "1.1.1.1")
	}
	if n := g.clear(); n == 0 || g.blocked("a@x.io", "1.1.1.1") != 0 {
		t.Fatalf("clear lifted %d locks and the email is still blocked", n)
	}
	for i := 0; i < pairFailures-1; i++ {
		g.failed("b@x.io", "1.1.1.1")
	}
	g.ok("b@x.io", "1.1.1.1")
	g.failed("b@x.io", "1.1.1.1")
	if g.blocked("b@x.io", "1.1.1.1") != 0 {
		t.Fatal("a good sign-in did not reset the count")
	}
}
