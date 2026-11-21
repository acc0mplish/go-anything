package usecase

import (
	"fmt"
	"strconv"

	"github.com/go-anything/go-anything/internal/domain"
)

// EffectSource collects the outbound calls made since the last collection. The capture
// receiver implements it.
type EffectSource interface {
	Drain() []domain.Effect
}

// withEffects runs one case and attaches the calls the program made while answering it.
// Calls made before the case (stray traffic from earlier) are discarded first, so each case's
// effects are its own and nothing else.
func withEffects(c domain.AppCase, runner AppRunner, src EffectSource, before func() error) (domain.AppCase, error) {
	if before != nil {
		// Setup such as a database reset runs first; the effects it causes are drained away.
		if err := before(); err != nil {
			return domain.AppCase{}, fmt.Errorf("before case: %w", err)
		}
	}
	if src != nil {
		src.Drain()
	}
	answered, err := runner.Do(c)
	if err != nil {
		return domain.AppCase{}, err
	}
	if src != nil {
		answered.Effects = src.Drain()
	}
	return answered, nil
}

// compareEffects reports each difference in outbound calls. Order matters: a webhook sent before
// another is a different behavior, so the sequences are compared position by position.
func compareEffects(caseName string, want, got []domain.Effect) []AppMismatch {
	var out []AppMismatch
	n := len(want)
	if len(got) > n {
		n = len(got)
	}
	for i := 0; i < n; i++ {
		var w, g string
		if i < len(want) {
			w = describeEffect(want[i])
		} else {
			w = "(no call)"
		}
		if i < len(got) {
			g = describeEffect(got[i])
		} else {
			g = "(no call)"
		}
		if w != g {
			out = append(out, AppMismatch{Case: caseName, What: "effect " + strconv.Itoa(i+1), Want: w, Got: g})
		}
	}
	return out
}

// describeEffect renders an effect for comparison and reports. Each kind shows the fields that
// make it what it is, so two different effects never look the same.
func describeEffect(e domain.Effect) string {
	if e.Kind == domain.EffectMail {
		return "mail " + e.Method + " -> " + e.Path + "\n" + e.Body
	}
	if e.Kind == domain.EffectDB {
		// An insert is compared by content only: its key is usually a generated number that
		// changes from run to run without any change in behavior. Updates and deletes name a row,
		// so their key stays part of the description.
		if e.Op == "insert" {
			return "db insert " + e.Table + " " + e.Body
		}
		return "db " + e.Op + " " + e.Table + " [" + e.Key + "] " + e.Body
	}
	return e.Method + " " + e.Path + " " + e.Body
}
