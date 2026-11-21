package usecase

import (
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func TestVerifyPassesOnlyWhenNotStale(t *testing.T) {
	ok := Verdict{Total: 1, Matched: 1}
	if !ok.Passed() {
		t.Fatal("clean verdict must pass")
	}
	stale := Verdict{Total: 1, Matched: 1, Stale: true}
	if stale.Passed() {
		t.Fatal("a stale recording must never pass, even when every case matches")
	}
}

func TestCheckDrift(t *testing.T) {
	fp := domain.Fingerprint{Files: 1, Hash: "aaa"}
	same := domain.Fingerprint{Files: 1, Hash: "aaa"}
	changed := domain.Fingerprint{Files: 1, Hash: "bbb"}

	recWith := domain.Recording{Source: &fp}
	recWithout := domain.Recording{}

	if CheckDrift(recWith, same, true) {
		t.Fatal("unchanged source is not stale")
	}
	if !CheckDrift(recWith, changed, true) {
		t.Fatal("changed source must be stale")
	}
	if !CheckDrift(recWithout, same, true) {
		t.Fatal("a recording with no fingerprint must be treated as stale, not trusted")
	}
	if !CheckDrift(recWith, same, false) {
		t.Fatal("an unknown current source must be treated as stale")
	}
}
