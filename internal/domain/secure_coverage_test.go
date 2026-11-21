package domain

import "testing"

// officialServerClauses lists every 소분류 of the official server common security requirements
// V3.0 R1 (국가용 보안요구사항 1·2편, 2023-08-21): 1.1-1.5, 2.1-2.5, 3.1-3.2, 4.1-4.2, 5.1,
// 6.1-6.2, 7.1-7.5, 8.1-8.4, 9.1-9.3. The checklist must anchor every one of them: an item with no
// clause behind it is noise, and a clause with no item is a silent gap in the confirmation run.
var officialServerClauses = []string{
	"1.1", "1.2", "1.3", "1.4", "1.5",
	"2.1", "2.2", "2.3", "2.4", "2.5",
	"3.1", "3.2",
	"4.1", "4.2",
	"5.1",
	"6.1", "6.2",
	"7.1", "7.2", "7.3", "7.4", "7.5",
	"8.1", "8.2", "8.3", "8.4",
	"9.1", "9.2", "9.3",
}

func TestSecurityChecklistCoversEveryOfficialClause(t *testing.T) {
	items := SecurityChecklist()
	covered := map[string]bool{}
	ids := map[string]bool{}
	for _, it := range items {
		if ids[it.ID] {
			t.Errorf("duplicate checklist id %s", it.ID)
		}
		ids[it.ID] = true
		if it.Ref == "" {
			t.Errorf("%s has no ref to an official clause", it.ID)
		}
		if it.Ref != "6" { // "6" is the one 대분류-level anchor; everything else is a 소분류
			covered[it.Ref] = true
		}
		if it.Category == "" || it.CategoryEN == "" || it.Requirement == "" || it.RequirementEN == "" {
			t.Errorf("%s must carry Korean and English for category and requirement", it.ID)
		}
		if it.Method == SecureManual && (it.Guidance == "" || it.GuidanceEN == "") {
			t.Errorf("%s is manual and must carry bilingual guidance", it.ID)
		}
		switch it.Severity {
		case SecureMandatory, SecureRecommended:
		default:
			t.Errorf("%s has unknown severity %q", it.ID, it.Severity)
		}
	}
	for _, clause := range officialServerClauses {
		if !covered[clause] {
			t.Errorf("official clause %s is not anchored by any checklist item; the confirmation run would silently skip it", clause)
		}
	}
}
