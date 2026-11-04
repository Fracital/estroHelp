package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

func TestValidTOTPAcceptsAdjacentWindow(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "t", AccountName: "a"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, offset := range []time.Duration{0, -30 * time.Second, 30 * time.Second} {
		code, _ := totp.GenerateCodeCustom(key.Secret(), now.Add(offset), totp.ValidateOpts{Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if !validTOTP(code, key.Secret()) {
			t.Errorf("code at offset %v rejected", offset)
		}
		if !validTOTP(code[:3]+" "+code[3:], key.Secret()) {
			t.Errorf("spaced code at offset %v rejected", offset)
		}
	}
	old, _ := totp.GenerateCodeCustom(key.Secret(), now.Add(-3*time.Minute), totp.ValidateOpts{Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if validTOTP(old, key.Secret()) {
		t.Error("stale code accepted")
	}
	if validTOTP("000000", key.Secret()) && validTOTP("123456", key.Secret()) {
		t.Error("arbitrary codes accepted")
	}
}

func TestBackupCodesShapeAndHashes(t *testing.T) {
	codes, hashes := newBackupCodes()
	if len(codes) != backupCodeCount || len(hashes) != backupCodeCount {
		t.Fatalf("got %d codes, %d hashes", len(codes), len(hashes))
	}
	seen := map[string]bool{}
	for i, c := range codes {
		if len(c) != 11 || c[5] != '-' {
			t.Errorf("code %q has an unexpected shape", c)
		}
		if seen[c] {
			t.Errorf("duplicate code %q", c)
		}
		seen[c] = true
		if HashToken(normalizeBackup(strings.ToUpper(c))) != hashes[i] {
			t.Errorf("hash %d does not match its normalized code", i)
		}
	}
}

func TestMFATokenRoundTripAndTamper(t *testing.T) {
	tok := newMFAToken(42)
	id, nonce, err := parseMFAToken(tok)
	if err != nil || id != 42 || nonce == "" {
		t.Fatalf("round trip failed: id=%d nonce=%q err=%v", id, nonce, err)
	}
	parts := strings.Split(tok, ".")
	parts[0] = "7"
	if _, _, err := parseMFAToken(strings.Join(parts, ".")); err == nil {
		t.Error("tampered user id accepted")
	}
	if _, _, err := parseMFAToken(tok + "x"); err == nil {
		t.Error("tampered signature accepted")
	}
	expired := "42.1.deadbeef"
	if _, _, err := parseMFAToken(expired + "." + mfaSign(expired)); err == nil {
		t.Error("expired token accepted")
	}
}

func TestMFAAttemptCap(t *testing.T) {
	nonce := "cap-test-" + newMFAToken(1)
	for i := 0; i < mfaMaxAttempts; i++ {
		if !mfaTry(nonce) {
			t.Fatalf("attempt %d refused early", i+1)
		}
	}
	if mfaTry(nonce) {
		t.Error("attempt past the cap accepted")
	}
	mfaDone("other")
	if mfaTry("other") {
		t.Error("used nonce accepted again")
	}
}
