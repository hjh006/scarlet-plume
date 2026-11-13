package apivalidator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/dataformat"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func policyDigest(p store.APIValidationProbePolicy) string {
	b, _ := json.Marshal(p)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func sensitiveCount(body map[string]any, fields []store.APIValidationSensitiveField) (int, bool) {
	count := 0
	for _, f := range fields {
		var current any = body
		missing := false
		for _, part := range strings.Split(f.Path, ".") {
			object, ok := current.(map[string]any)
			if !ok {
				return 0, false
			}
			current, ok = object[part]
			if !ok || current == nil {
				missing = true
				break
			}
		}
		if missing {
			continue
		}
		text, ok := current.(string)
		if !ok {
			return 0, false
		}
		if dataformat.Matches(f.Category, text) {
			count++
		}
	}
	return count, true
}
func extendedProbe(ctx context.Context, client *http.Client, b Binding, claim store.APIValidationClaim, rule string, owner response) (store.APIValidationRuleResult, bool) {
	r := store.APIValidationRuleResult{Rule: rule, Outcome: "inconclusive", Requests: 1, OwnerStatus: owner.status, OwnerConfirmed: confirmed(owner, b.AccountA), PolicyConfirmed: true, PolicyDigest: policyDigest(b.ProbePolicy), Gaps: []string{}}
	if rule != "auth_required" && rule != "sensitive_exposure" && rule != "response_headers" {
		return r, false
	}
	if !r.OwnerConfirmed {
		gap := owner.gap
		if gap == "" {
			gap = "owner_unconfirmed"
		}
		r.Gaps = []string{gap}
		return r, true
	}
	if claim.Mode != "validation" {
		r.Gaps = []string{"connection_only"}
		return r, true
	}
	if rule == "response_headers" {
		r.HeaderChecked = len(b.ProbePolicy.ResponseHeaders)
		r.HeaderMismatches = []string{}
		for _, h := range b.ProbePolicy.ResponseHeaders {
			if owner.headers[h.Name] != h.Value {
				r.HeaderMismatches = append(r.HeaderMismatches, h.Name)
			}
		}
		r.ComparisonComplete = true
		r.Outcome = "not_observed"
		if len(r.HeaderMismatches) > 0 {
			r.Outcome = "vulnerable"
		}
		return r, true
	}
	if rule == "sensitive_exposure" {
		count, complete := sensitiveCount(owner.body, b.ProbePolicy.SensitiveFields)
		r.OwnerSensitiveCount = count
		r.SensitiveChecked = len(b.ProbePolicy.SensitiveFields)
		if !complete {
			r.Gaps = []string{"field_shape_unknown"}
			return r, true
		}
		if count < 1 {
			r.Gaps = []string{"sensitive_control_missing"}
			return r, true
		}
	}
	anonymous := request(ctx, client, b, Account{}, b.AccountA.ObjectPath, true)
	r.Requests = 2
	r.AnonymousStatus = anonymous.status
	r.AnonymousObjectConfirmed = confirmed(anonymous, b.AccountA)
	if anonymous.gap != "" {
		r.Gaps = []string{anonymous.gap}
		return r, true
	}
	if anonymous.status == 401 || anonymous.status == 403 {
		r.ComparisonComplete = true
		r.Outcome = "not_observed"
		return r, true
	}
	if !r.AnonymousObjectConfirmed {
		r.Gaps = []string{"anonymous_response_unexpected"}
		return r, true
	}
	if rule == "auth_required" {
		r.ComparisonComplete = true
		r.Outcome = "vulnerable"
		return r, true
	}
	count, complete := sensitiveCount(anonymous.body, b.ProbePolicy.SensitiveFields)
	r.AnonymousSensitiveCount = count
	if !complete {
		r.Gaps = []string{"field_shape_unknown"}
		return r, true
	}
	r.ComparisonComplete = true
	r.Outcome = "not_observed"
	if count > 0 {
		r.Outcome = "vulnerable"
	}
	return r, true
}
