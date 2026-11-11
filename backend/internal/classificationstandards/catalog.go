package classificationstandards

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sync"
	"time"
)

const DefaultID = "gbt-43697-2024"
const CatalogAsOf = "2026-11-11"

//go:embed catalog/*.json
var files embed.FS

var ErrSelection = errors.New("invalid classification standard, level or rule")

type Standard struct {
	ID          string  `json:"id"`
	Code        string  `json:"code"`
	Title       string  `json:"title"`
	Industry    string  `json:"industry"`
	Kind        string  `json:"kind"`
	Status      string  `json:"status"`
	PublishedOn string  `json:"publishedOn,omitempty"`
	EffectiveOn string  `json:"effectiveOn,omitempty"`
	Authority   string  `json:"authority"`
	URL         string  `json:"url"`
	Scope       string  `json:"scope"`
	Levels      []Level `json:"levels"`
	Rules       []Rule  `json:"rules"`
}

type Level struct {
	Code              string `json:"code"`
	Label             string `json:"label"`
	StatutoryCategory string `json:"statutoryCategory,omitempty"`
}

type Rule struct {
	ID         string   `json:"id"`
	Clause     string   `json:"clause"`
	Title      string   `json:"title"`
	Criterion  string   `json:"criterion"`
	Evidence   string   `json:"evidence"`
	LevelCodes []string `json:"levelCodes,omitempty"`
}

// Reference is an immutable snapshot of the version and provision chosen in a review.
type Reference struct {
	ID                string `json:"id"`
	Code              string `json:"code"`
	Title             string `json:"title"`
	Industry          string `json:"industry"`
	Kind              string `json:"kind"`
	URL               string `json:"url"`
	LevelCode         string `json:"levelCode,omitempty"`
	LevelLabel        string `json:"levelLabel,omitempty"`
	StatutoryCategory string `json:"statutoryCategory,omitempty"`
	RuleID            string `json:"ruleId,omitempty"`
	RuleClause        string `json:"ruleClause,omitempty"`
	RuleTitle         string `json:"ruleTitle,omitempty"`
	RuleCriterion     string `json:"ruleCriterion,omitempty"`
	RuleEvidence      string `json:"ruleEvidence,omitempty"`
}

var standardID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)
var load = sync.OnceValues(readCatalog)

func List() ([]Standard, error) {
	catalog, err := load()
	return catalog, err
}

func IsSelectable(id string) (bool, error) {
	standard, err := lookup(id)
	return standard != nil && standard.Status == "effective", err
}

func lookup(id string) (*Standard, error) {
	catalog, err := load()
	if err != nil {
		return nil, err
	}
	for i := range catalog {
		if catalog[i].ID == id {
			return &catalog[i], nil
		}
	}
	return nil, nil
}

func Select(id, levelCode, ruleID, statutoryCategory string) (*Reference, error) {
	standard, err := lookup(id)
	if err != nil {
		return nil, err
	}
	if standard == nil {
		return nil, ErrSelection
	}
	referenceOnly := standard.Status == "published" && standard.EffectiveOn == "" &&
		len(standard.Levels) == 0 && len(standard.Rules) == 0
	if standard.Status != "effective" && !referenceOnly {
		return nil, ErrSelection
	}
	reference := &Reference{
		ID: standard.ID, Code: standard.Code, Title: standard.Title,
		Industry: standard.Industry, Kind: standard.Kind, URL: standard.URL,
	}
	if len(standard.Levels) > 0 {
		i := slices.IndexFunc(standard.Levels, func(level Level) bool { return level.Code == levelCode })
		if i < 0 {
			return nil, ErrSelection
		}
		level := standard.Levels[i]
		if level.StatutoryCategory != "" && level.StatutoryCategory != statutoryCategory {
			return nil, ErrSelection
		}
		reference.LevelCode, reference.LevelLabel = level.Code, level.Label
		reference.StatutoryCategory = level.StatutoryCategory
	} else if levelCode != "" {
		return nil, ErrSelection
	}
	if len(standard.Rules) > 0 {
		i := slices.IndexFunc(standard.Rules, func(rule Rule) bool { return rule.ID == ruleID })
		if i < 0 {
			return nil, ErrSelection
		}
		rule := standard.Rules[i]
		if len(rule.LevelCodes) > 0 && !slices.Contains(rule.LevelCodes, levelCode) {
			return nil, ErrSelection
		}
		reference.RuleID, reference.RuleClause, reference.RuleTitle = rule.ID, rule.Clause, rule.Title
		reference.RuleCriterion, reference.RuleEvidence = rule.Criterion, rule.Evidence
	} else if ruleID != "" {
		return nil, ErrSelection
	}
	return reference, nil
}

func readCatalog() ([]Standard, error) {
	entries, err := files.ReadDir("catalog")
	if err != nil {
		return nil, err
	}
	var catalog []Standard
	seen := make(map[string]bool)
	for _, entry := range entries {
		data, err := files.ReadFile("catalog/" + entry.Name())
		if err != nil {
			return nil, err
		}
		var standards []Standard
		if err := json.Unmarshal(data, &standards); err != nil {
			return nil, fmt.Errorf("read classification standards %s: %w", entry.Name(), err)
		}
		for _, standard := range standards {
			if seen[standard.ID] || !validStandard(standard) {
				return nil, fmt.Errorf("invalid classification standard %q", standard.ID)
			}
			seen[standard.ID] = true
			if standard.Levels == nil {
				standard.Levels = []Level{}
			}
			if standard.Rules == nil {
				standard.Rules = []Rule{}
			}
			catalog = append(catalog, standard)
		}
	}
	if !slices.ContainsFunc(catalog, func(standard Standard) bool {
		return standard.ID == DefaultID && standard.Status == "effective" && len(standard.Rules) > 0
	}) {
		return nil, errors.New("classification standard default is unavailable")
	}
	return catalog, nil
}

func validStandard(standard Standard) bool {
	if !standardID.MatchString(standard.ID) || standard.Code == "" || standard.Title == "" ||
		standard.Industry == "" || standard.Authority == "" || standard.Scope == "" {
		return false
	}
	if !slices.Contains([]string{"national", "industry", "guideline"}, standard.Kind) ||
		!slices.Contains([]string{"effective", "published", "superseded"}, standard.Status) {
		return false
	}
	source, err := url.Parse(standard.URL)
	if err != nil || source.Scheme != "https" || source.Hostname() == "" || source.User != nil {
		return false
	}
	for _, date := range []string{standard.PublishedOn, standard.EffectiveOn} {
		if date != "" {
			if _, err := time.Parse(time.DateOnly, date); err != nil {
				return false
			}
		}
	}
	levels := make(map[string]bool)
	for _, level := range standard.Levels {
		if level.Code == "" || level.Label == "" || levels[level.Code] ||
			!slices.Contains([]string{"", "general", "important", "core"}, level.StatutoryCategory) {
			return false
		}
		levels[level.Code] = true
	}
	rules := make(map[string]bool)
	for _, rule := range standard.Rules {
		if rule.ID == "" || rule.Clause == "" || rule.Title == "" || rule.Criterion == "" ||
			rule.Evidence == "" || rules[rule.ID] {
			return false
		}
		rules[rule.ID] = true
		for _, code := range rule.LevelCodes {
			if !levels[code] {
				return false
			}
		}
	}
	return true
}
