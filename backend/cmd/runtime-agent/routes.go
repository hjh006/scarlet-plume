package main

import (
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

const maxRouteTemplates = 250

type routeTemplate struct {
	pattern     string
	segments    []string // Empty values are full-segment parameters.
	specificity int
}

func addRoute(routes *[]routeTemplate, pattern string) error {
	if len(*routes) >= maxRouteTemplates {
		return errors.New("too many route templates")
	}
	if _, err := runtimesecurity.NormalizeObservation(runtimesecurity.Observation{
		Method: "GET", Route: pattern, Status: 200, ObservedAt: time.Unix(1, 0),
	}); err != nil {
		return errors.New("route must be a safe path template with full-segment placeholders")
	}

	route := routeTemplate{pattern: pattern}
	if pattern != "/" {
		route.segments = strings.Split(pattern[1:], "/")
	}
	for i, segment := range route.segments {
		if segment[0] == '{' {
			route.segments[i] = ""
		} else {
			route.specificity++
		}
	}
	for _, existing := range *routes {
		if route.specificity == existing.specificity && overlaps(route.segments, existing.segments) {
			return errors.New("duplicate or ambiguous route templates")
		}
	}
	*routes = append(*routes, route)
	slices.SortStableFunc(*routes, func(a, b routeTemplate) int { return b.specificity - a.specificity })
	return nil
}

func overlaps(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i, segment := range left {
		if segment != "" && right[i] != "" && segment != right[i] {
			return false
		}
	}
	return true
}

func matchRoute(routes []routeTemplate, path string) string {
	if len(path) > 2048 || !strings.HasPrefix(path, "/") {
		return ""
	}
	var parts []string
	if path != "/" {
		parts = strings.Split(path[1:], "/")
	}
	if len(parts) > 20 {
		return ""
	}
	for i, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, "/\\%") ||
			strings.IndexFunc(decoded, unicode.IsControl) >= 0 {
			return ""
		}
		parts[i] = decoded
	}
	for _, route := range routes {
		if len(route.segments) != len(parts) {
			continue
		}
		matched := true
		for i, segment := range route.segments {
			if segment != "" && segment != parts[i] {
				matched = false
				break
			}
		}
		if matched {
			return route.pattern
		}
	}
	return ""
}
