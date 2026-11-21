package domain

import "testing"

func TestEffectsAreOptionalAndValidate(t *testing.T) {
	rec := AppRecording{Subject: "svc", Cases: []AppCase{
		{Name: "order", Kind: "http", Method: "POST", Path: "/orders", Status: 201,
			Effects: []Effect{{Method: "POST", Path: "/hook", Body: `{"id":1}`}}},
	}}
	if err := rec.Validate(); err != nil {
		t.Fatalf("a recording with effects must validate: %v", err)
	}
}
