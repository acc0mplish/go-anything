package domain

import "testing"

func TestGoVersionAtLeast(t *testing.T) {
	cases := []struct {
		installed, required string
		want                bool
	}{
		{"go1.27.4", "1.27.4", true},
		{"go1.27.5", "1.27.4", true},
		{"go1.28.0", "1.27.4", true},
		{"go1.27.3", "1.27.4", false},
		{"go1.26.9", "1.27.4", false},
		{"go1.10", "1.9", true},
		{"go1.9", "1.10", false},
		{"go1.27", "1.27.0", true},
		{"garbage", "1.27.4", false},
		{"", "1.27.4", false},
	}
	for _, c := range cases {
		if got := GoVersionAtLeast(c.installed, c.required); got != c.want {
			t.Fatalf("GoVersionAtLeast(%q,%q)=%v want %v", c.installed, c.required, got, c.want)
		}
	}
}

func TestDoctorHealthyIgnoresWarnings(t *testing.T) {
	r := DoctorReport{Checks: []Check{{Name: "a", Level: LevelOK}, {Name: "b", Level: LevelWarn}}}
	if !r.Healthy() {
		t.Fatal("warnings alone must not make the setup unhealthy")
	}
}

func TestDoctorUnhealthyOnAnyFailure(t *testing.T) {
	r := DoctorReport{Checks: []Check{{Name: "a", Level: LevelOK}, {Name: "b", Level: LevelFail}}}
	if r.Healthy() {
		t.Fatal("a failed check must make the setup unhealthy")
	}
}

func TestDoctorCounts(t *testing.T) {
	r := DoctorReport{Checks: []Check{{Level: LevelOK}, {Level: LevelOK}, {Level: LevelWarn}, {Level: LevelFail}}}
	c := r.Counts()
	if c[LevelOK] != 2 || c[LevelWarn] != 1 || c[LevelFail] != 1 {
		t.Fatalf("counts wrong: %+v", c)
	}
}
