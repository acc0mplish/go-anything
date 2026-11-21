package domain

import "testing"

func TestDecideFirstMatchWins(t *testing.T) {
	rules := []Route{
		{Method: "GET", PathPrefix: "/api/users", Target: TargetRewrite},
		{PathPrefix: "/api", Target: TargetOriginal},
	}
	if Decide(rules, "GET", "/api/users/7") != TargetRewrite {
		t.Fatal("specific rule must win")
	}
	if Decide(rules, "POST", "/api/users/7") != TargetOriginal {
		t.Fatal("other method must fall to the broader rule")
	}
}

func TestDecideUnlistedGoesToOriginal(t *testing.T) {
	rules := []Route{{PathPrefix: "/only", Target: TargetRewrite}}
	if Decide(rules, "GET", "/elsewhere") != TargetOriginal {
		t.Fatal("unlisted request must reach the original, never unverified code")
	}
	if Decide(nil, "GET", "/") != TargetOriginal {
		t.Fatal("no rules means the original answers everything")
	}
}

func TestDecideMethodIsCaseInsensitiveAndWildcard(t *testing.T) {
	rules := []Route{{Method: "*", PathPrefix: "/any", Target: TargetRewrite}}
	if Decide(rules, "delete", "/any/x") != TargetRewrite {
		t.Fatal("wildcard method must match any method")
	}
	if Decide([]Route{{Method: "get", PathPrefix: "/g", Target: TargetRewrite}}, "GET", "/g") != TargetRewrite {
		t.Fatal("method match must be case-insensitive")
	}
}

func TestValidRoutes(t *testing.T) {
	if err := ValidRoutes([]Route{{PathPrefix: "/a", Target: TargetRewrite}}); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
	if err := ValidRoutes([]Route{{PathPrefix: "/a", Target: "elsewhere"}}); err == nil {
		t.Fatal("unknown target must be rejected")
	}
	if err := ValidRoutes([]Route{{PathPrefix: "no-slash", Target: TargetRewrite}}); err == nil {
		t.Fatal("prefix without leading slash must be rejected")
	}
}
