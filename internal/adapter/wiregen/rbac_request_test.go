package wiregen

import (
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func TestRBACRequestKeepsCaseHeadersAndSetsPrincipalToken(t *testing.T) {
	c := domain.AppCase{Name: "get", RequestHeaders: map[string]string{"X-Request-Kind": "read"}}
	got := RBACRequest(c, domain.Principal{Name: "bob", Role: "team_user", TokenEnv: "GA_BOB"})
	if got.RequestHeaders["X-Request-Kind"] != "read" {
		t.Fatalf("the case's own header must survive: %v", got.RequestHeaders)
	}
	if got.RequestHeaders["Authorization"] != "Bearer ${GA_BOB}" {
		t.Fatalf("the principal's token must be the credential: %v", got.RequestHeaders)
	}
}

func TestRBACRequestAnonymousCarriesNoCredential(t *testing.T) {
	c := domain.AppCase{Name: "get", RequestHeaders: map[string]string{"authorization": "Bearer leaked", "X-Keep": "1"}}
	got := RBACRequest(c, domain.Principal{Name: "anon", Role: domain.AnonymousRole})
	if _, ok := got.RequestHeaders["authorization"]; ok {
		t.Fatalf("an anonymous principal must not send a credential from the case: %v", got.RequestHeaders)
	}
	if _, ok := got.RequestHeaders["Authorization"]; ok {
		t.Fatalf("an anonymous principal must not get an Authorization header: %v", got.RequestHeaders)
	}
	if got.RequestHeaders["X-Keep"] != "1" {
		t.Fatalf("other headers must survive for anonymous requests: %v", got.RequestHeaders)
	}
}
