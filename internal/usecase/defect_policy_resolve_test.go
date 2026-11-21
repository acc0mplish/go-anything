package usecase

import "testing"

func policyPtr(p DefectPolicy) *DefectPolicy { return &p }

// The four sources, strongest first. Each row sets up one situation and names the policy and source that
// must win. The "other unit" row is the one that catches an entry applying to a unit it was not written for.
func TestResolveDefectPolicyPrecedence(t *testing.T) {
	fileDefault := PolicySettings{Default: policyPtr(PolicyExpected)}
	cases := []struct {
		name       string
		unit       string
		flag       *DefectPolicy
		settings   PolicySettings
		wantPolicy DefectPolicy
		wantSource PolicySource
	}{
		{
			name:       "flag beats unit entry and file default",
			unit:       "orders",
			flag:       policyPtr(PolicyDefect),
			settings:   PolicySettings{Default: policyPtr(PolicyExpected), Units: map[string]DefectPolicy{"orders": PolicyExpected}},
			wantPolicy: PolicyDefect,
			wantSource: SourceFlag,
		},
		{
			name:       "unit entry beats file default",
			unit:       "orders",
			settings:   PolicySettings{Default: policyPtr(PolicyDefect), Units: map[string]DefectPolicy{"orders": PolicyExpected}},
			wantPolicy: PolicyExpected,
			wantSource: SourceUnit,
		},
		{
			name:       "file default applies when the unit has no entry",
			unit:       "orders",
			settings:   fileDefault,
			wantPolicy: PolicyExpected,
			wantSource: SourceFile,
		},
		{
			name:       "built-in default when nothing is set",
			unit:       "orders",
			settings:   PolicySettings{},
			wantPolicy: PolicyDefect,
			wantSource: SourceDefault,
		},
		{
			name:       "entry for another unit is ignored",
			unit:       "orders",
			settings:   PolicySettings{Units: map[string]DefectPolicy{"users": PolicyExpected}},
			wantPolicy: PolicyDefect,
			wantSource: SourceDefault,
		},
		{
			name:       "entry for another unit does not override the file default",
			unit:       "orders",
			settings:   PolicySettings{Default: policyPtr(PolicyDefect), Units: map[string]DefectPolicy{"users": PolicyExpected}},
			wantPolicy: PolicyDefect,
			wantSource: SourceFile,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotPolicy, gotSource := ResolveDefectPolicy(c.unit, c.flag, c.settings)
			if gotPolicy != c.wantPolicy || gotSource != c.wantSource {
				t.Fatalf("got (%q, %q), want (%q, %q)", gotPolicy, gotSource, c.wantPolicy, c.wantSource)
			}
		})
	}
}
