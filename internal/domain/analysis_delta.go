package domain

import "sort"

// AnalysisDelta is how an analysis changed against an earlier one of the same module. The int
// fields are current minus previous, so a positive value means the code grew.
type AnalysisDelta struct {
	PrevVersion  int `json:"prev_version"`
	Packages     int `json:"packages"`
	Lines        int `json:"lines"`
	Functions    int `json:"functions"`
	CallEdges    int `json:"call_edges"`
	DynamicCalls int `json:"dynamic_calls"`
	Risks        int `json:"risks"`
	StartHere    int `json:"start_here"`
	// AddedPackages and RemovedPackages are import paths, sorted.
	AddedPackages   []string `json:"added_packages,omitempty"`
	RemovedPackages []string `json:"removed_packages,omitempty"`
	// NewRisks are the current risk sites whose kind and file:line were not in the previous
	// analysis, most serious kind first. A risk that only moved to another line counts as new.
	NewRisks []LocatedRisk `json:"new_risks,omitempty"`
}

// DiffAnalyses compares two analyses. It is pure: it reads both and changes neither.
// PrevVersion is left zero; the caller knows the version numbers.
func DiffAnalyses(prev, cur Analysis) AnalysisDelta {
	ps, cs := analysisStats(prev), analysisStats(cur)
	d := AnalysisDelta{
		Packages:     len(cur.Packages) - len(prev.Packages),
		Lines:        cs.lines - ps.lines,
		Functions:    cs.functions - ps.functions,
		CallEdges:    cur.CallEdges - prev.CallEdges,
		DynamicCalls: cur.DynamicCalls - prev.DynamicCalls,
		Risks:        cs.risks - ps.risks,
		StartHere:    len(cur.StartHere) - len(prev.StartHere),
	}
	before := map[string]bool{}
	for _, p := range prev.Packages {
		before[p.ImportPath] = true
	}
	now := map[string]bool{}
	for _, p := range cur.Packages {
		now[p.ImportPath] = true
		if !before[p.ImportPath] {
			d.AddedPackages = append(d.AddedPackages, p.ImportPath)
		}
	}
	for _, p := range prev.Packages {
		if !now[p.ImportPath] {
			d.RemovedPackages = append(d.RemovedPackages, p.ImportPath)
		}
	}
	sort.Strings(d.AddedPackages)
	sort.Strings(d.RemovedPackages)

	seen := map[string]bool{}
	for _, p := range prev.Packages {
		for _, s := range p.RiskSites {
			seen[riskKey(s)] = true
		}
	}
	for _, r := range RankedRisks(cur) {
		if !seen[riskKey(r.RiskSite)] {
			d.NewRisks = append(d.NewRisks, r)
		}
	}
	return d
}

// riskKey identifies a risk site by kind and position. Positions are relative to the analyzed
// root, so the file path already tells packages apart.
func riskKey(s RiskSite) string { return s.Kind + "\x00" + s.Pos }

type analysisTotals struct{ lines, functions, risks int }

func analysisStats(a Analysis) analysisTotals {
	var s analysisTotals
	for _, p := range a.Packages {
		s.lines += p.Lines
		s.functions += len(p.Functions)
		s.risks += len(p.RiskSites)
	}
	return s
}
