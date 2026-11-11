package runtimesecurity

import (
	"cmp"
	"slices"
)

func normalizeSecurity(o *Observation) error {
	if len(o.InspectedLocations) > 2 {
		return ErrInvalidObservation
	}
	o.InspectedLocations = slices.Clone(o.InspectedLocations)
	for _, location := range o.InspectedLocations {
		if !slices.Contains([]string{"request", "response"}, location) {
			return ErrInvalidObservation
		}
	}
	slices.Sort(o.InspectedLocations)
	o.InspectedLocations = slices.Compact(o.InspectedLocations)
	if !slices.Contains([]string{"", "unknown", "authenticated", "anonymous"}, o.AuthState) || len(o.SecuritySignals) > 3 {
		return ErrInvalidObservation
	}
	if o.AuthRequired != nil {
		v := *o.AuthRequired
		o.AuthRequired = &v
	}
	o.SecuritySignals = slices.Clone(o.SecuritySignals)
	for _, signal := range o.SecuritySignals {
		if !slices.Contains([]string{"sql_injection", "xss", "path_traversal"}, signal.Kind) || signal.Location != "request" || signal.Count < 0 || signal.Observed < 1 || signal.Observed > 1000000 || signal.Count > signal.Observed {
			return ErrInvalidObservation
		}
	}
	slices.SortFunc(o.SecuritySignals, func(a, b SecuritySignal) int { return cmp.Compare(a.Kind, b.Kind) })
	for i := 1; i < len(o.SecuritySignals); i++ {
		if o.SecuritySignals[i].Kind == o.SecuritySignals[i-1].Kind && o.SecuritySignals[i] != o.SecuritySignals[i-1] {
			return ErrInvalidObservation
		}
	}
	o.SecuritySignals = slices.Compact(o.SecuritySignals)
	return nil
}
