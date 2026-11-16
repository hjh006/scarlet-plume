package aisecurity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/hjh006/scarlet-plume/backend/pkg/datasafety"
	"github.com/hjh006/scarlet-plume/backend/pkg/gatewaygovernance"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrConfig = errors.New("invalid AI security catalog")
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$`)
var category = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validID(s string) bool { return identifier.MatchString(s) }
func bounded(s string, n int) bool {
	return utf8.ValidString(s) && len(s) <= n && !strings.ContainsAny(s, "\x00\r\n")
}
func list(v []string, max int, valid func(string) bool) bool {
	if len(v) > max {
		return false
	}
	seen := map[string]bool{}
	for _, s := range v {
		if !valid(s) || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}
func ids(v []string) bool        { return list(v, 2000, validID) }
func categories(v []string) bool { return list(v, 64, category.MatchString) }
func metadata(v, s, d string) bool {
	return bounded(v, 80) && bounded(s, 240) && (d == "" || digest.MatchString(d))
}
func references(v []string, known map[string]bool) bool {
	for _, id := range v {
		if !known[id] {
			return false
		}
	}
	return true
}
func addID(m map[string]bool, id string) bool {
	if !validID(id) || m[id] {
		return false
	}
	m[id] = true
	return true
}

// ValidateConfig rejects ambiguous identities, dangling references and malformed policies.
// Empty catalogs are valid and deny all runtime requests.
func ValidateConfig(c Config) error {
	p := c.Policy
	if _, err := compileInspection(p.Inspection); err != nil || p.Inspection != nil && p.RequiredInspectionVersion != InspectionVersion {
		return ErrConfig
	}
	if p.RequiredRuntimeProtocolVersion != "" && p.RequiredRuntimeProtocolVersion != RuntimeProtocolVersion || p.RequiredInspectionVersion != "" && p.RequiredInspectionVersion != InspectionVersion && (p.RequiredInspectionVersion != LegacyInspectionVersion || p.Inspection != nil) {
		return ErrConfig
	}
	if !validID(c.Version) || !validID(p.Version) || p.MaxRequestAgeSeconds < 1 || p.MaxRequestAgeSeconds > 3600 || p.MaxInputBytes < 1 || p.MaxInputBytes > 16<<20 || p.MaxOutputBytes < 1 || p.MaxOutputBytes > 16<<20 || !categories(p.AllowedInputCategories) || !categories(p.AllowedOutputCategories) || !list(p.AllowedModelRegions, 128, validID) {
		return ErrConfig
	}
	for _, patterns := range [][]string{p.BlockedInputPatterns, p.BlockedOutputPatterns} {
		if len(patterns) > 32 {
			return ErrConfig
		}
		for _, pattern := range patterns {
			if len(pattern) == 0 || len(pattern) > 256 || !utf8.ValidString(pattern) {
				return ErrConfig
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return ErrConfig
			}
		}
	}
	if len(c.Applications) > 128 || len(c.Principals) > 2000 || len(c.Documents) > 2000 || len(c.Models) > 128 || len(c.Tools) > 256 || len(c.Approvals) > 1000 {
		return ErrConfig
	}
	apps, principals, docs, models, tools := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, a := range c.Applications {
		if !addID(apps, a.ID) || !bounded(a.Name, 160) || !ids(a.PrincipalIDs) || !ids(a.DocumentIDs) || !ids(a.ModelIDs) || !ids(a.ToolIDs) || !metadata(a.Version, a.SourceRef, a.IntegrityDigest) {
			return ErrConfig
		}
	}
	for _, p := range c.Principals {
		if !addID(principals, p.ID) || !bounded(p.Name, 160) || !ids(p.ApplicationIDs) || p.AuthorizationExpiresAt != nil && p.AuthorizationExpiresAt.IsZero() {
			return ErrConfig
		}
	}
	for _, d := range c.Documents {
		if !validateDataUse(d.UsePolicy) {
			return ErrConfig
		}
		if d.AuthorizationExpiresAt != nil && d.AuthorizationExpiresAt.IsZero() {
			return ErrConfig
		}
		if !addID(docs, d.ID) || !bounded(d.Name, 160) || !validID(d.Version) || !validID(d.ACLVersion) || !validID(d.KnowledgeBaseID) || !bounded(d.SourceRef, 240) || !ids(d.AllowedPrincipalIDs) || !categories(d.Categories) || d.IntegrityDigest != "" && !digest.MatchString(d.IntegrityDigest) {
			return ErrConfig
		}
	}
	for _, m := range c.Models {
		if !validateMediaPolicy(m.Media) || !validateDataHandling(m.Handling) {
			return ErrConfig
		}
		if m.ResponseFormat != "" && m.ResponseFormat != "json" && m.ResponseFormat != "openai_chat" {
			return ErrConfig
		}
		if !addID(models, m.ID) || !bounded(m.Name, 160) || !bounded(m.Provider, 80) || !bounded(m.UpstreamModel, 160) || !validID(m.EndpointRef) || !validID(m.Region) || !ids(m.AllowedApplicationIDs) || !categories(m.AllowedInputCategories) || !categories(m.AllowedOutputCategories) || !metadata(m.Version, m.SourceRef, m.IntegrityDigest) {
			return ErrConfig
		}
	}
	for _, t := range c.Tools {
		if !validateDataHandling(t.Handling) || !categories(t.AllowedInputCategories) {
			return ErrConfig
		}
		if !addID(tools, t.ID) || !bounded(t.Name, 160) || !validID(t.EndpointRef) || !ids(t.AllowedApplicationIDs) || !ids(t.AllowedPrincipalIDs) || !list(t.Actions, 64, validID) || !list(t.Targets, 128, func(s string) bool { return s != "" && bounded(s, 256) }) || !list(t.RequiredArguments, 32, validID) || len(t.AllowedArguments) > 32 || !metadata(t.Version, t.SourceRef, t.IntegrityDigest) {
			return ErrConfig
		}
		if t.Protocol != "" && t.Protocol != "json" && t.Protocol != "mcp_http" {
			return ErrConfig
		}
		if t.Protocol == "mcp_http" {
			if t.ProtocolVersion != "" && t.ProtocolVersion != MCPVersion || len(t.DefinitionDigests) != 0 && len(t.DefinitionDigests) != len(t.Actions) {
				return ErrConfig
			}
			for action, value := range t.DefinitionDigests {
				if !slices.Contains(t.Actions, action) || !digest.MatchString(value) {
					return ErrConfig
				}
			}
			if !validID(t.TargetArgument) || slices.Contains(t.RequiredArguments, t.TargetArgument) {
				return ErrConfig
			}
			if _, exists := t.AllowedArguments[t.TargetArgument]; exists {
				return ErrConfig
			}
		} else if t.TargetArgument != "" || t.ProtocolVersion != "" || len(t.DefinitionDigests) != 0 {
			return ErrConfig
		}
		for k, v := range t.AllowedArguments {
			if !validID(k) || !list(v, 128, func(s string) bool { return bounded(s, 1024) }) || len(v) == 0 {
				return ErrConfig
			}
		}
		for _, k := range t.RequiredArguments {
			if _, ok := t.AllowedArguments[k]; !ok {
				return ErrConfig
			}
		}
	}
	for _, a := range c.Applications {
		if !references(a.PrincipalIDs, principals) || !references(a.DocumentIDs, docs) || !references(a.ModelIDs, models) || !references(a.ToolIDs, tools) {
			return ErrConfig
		}
	}
	for _, p := range c.Principals {
		if !references(p.ApplicationIDs, apps) {
			return ErrConfig
		}
	}
	for _, d := range c.Documents {
		if !references(d.AllowedPrincipalIDs, principals) {
			return ErrConfig
		}
	}
	for _, m := range c.Models {
		if !references(m.AllowedApplicationIDs, apps) {
			return ErrConfig
		}
	}
	for _, t := range c.Tools {
		if !references(t.AllowedApplicationIDs, apps) || !references(t.AllowedPrincipalIDs, principals) {
			return ErrConfig
		}
	}
	seen := map[string]bool{}
	for _, a := range c.Approvals {
		if !addID(seen, a.ID) || !digest.MatchString(a.RequestDigest) || !digest.MatchString(a.CatalogDigest) || !apps[a.ApplicationID] || !principals[a.PrincipalID] || !tools[a.ToolID] || !validID(a.ApprovedBy) || a.ApprovedBy == a.PrincipalID || !validID(a.Action) || a.Target == "" || !bounded(a.Target, 256) || a.ExpiresAt.IsZero() {
			return ErrConfig
		}
		if (a.LeaseChainDigest != "" || a.LeaseProtocolVersion != "") && (!digest.MatchString(a.LeaseChainDigest) || a.LeaseProtocolVersion != gatewaygovernance.TaskLeaseProtocolVersion) {
			return ErrConfig
		}
	}
	return nil
}
func hash(v any) string {
	raw, _ := json.Marshal(v)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// ConfigDigest excludes approvals so an independently issued approval can bind this catalog.
func ConfigDigest(c Config) string { c.Approvals = nil; return hash(c) }

// RequestDigest excludes approvalId; functional parameters, nonce and expiry remain bound.
func RequestDigest(r Request) string {
	r.ApprovalID = ""
	r.IssuedAt = r.IssuedAt.UTC()
	r.ExpiresAt = r.ExpiresAt.UTC()
	return hash(r)
}
func allowedCategories(found, global, local []string, perTarget bool) bool {
	for _, v := range found {
		if !slices.Contains(global, v) || perTarget && !slices.Contains(local, v) {
			return false
		}
	}
	return true
}
func inspect(text string, limit int, patterns []string, profiles ...*InspectionPolicy) ([]string, string) {
	if len(text) > limit || !utf8.ValidString(text) {
		return nil, "content_budget_or_encoding"
	}
	if text == "" {
		return []string{}, ""
	}
	var facts datasafety.Context
	var err error
	if strings.HasPrefix(strings.TrimSpace(text), "{") || strings.HasPrefix(strings.TrimSpace(text), "[") {
		facts, _, err = datasafety.InspectJSON(strings.NewReader(text))
	} else {
		facts, _, err = datasafety.InspectText(strings.NewReader(text))
	}
	if err != nil || facts.Capture != "complete" {
		return nil, "content_inspection_incomplete"
	}
	// InspectJSON already bounds token/depth/byte budgets. Match decoded scalar
	// strings as well as wire bytes, so JSON Unicode escapes do not bypass a rule.
	stringsToCheck := []string{text}
	if json.Valid([]byte(text)) {
		var value any
		if json.Unmarshal([]byte(text), &value) == nil {
			var walk func(any)
			walk = func(v any) {
				switch x := v.(type) {
				case string:
					stringsToCheck = append(stringsToCheck, x)
				case []any:
					for _, y := range x {
						walk(y)
					}
				case map[string]any:
					for k, y := range x {
						stringsToCheck = append(stringsToCheck, k)
						if value, ok := y.(string); ok && len(profiles) > 0 && profiles[0] != nil {
							// Retain decoded key/value context for structured credentials.
							stringsToCheck = append(stringsToCheck, k+": "+value)
						}
						walk(y)
					}
				}
			}
			walk(value)
		}
	}
	var profile *InspectionPolicy
	if len(profiles) > 0 {
		profile = profiles[0]
	}
	additional, code := inspectAdditional(stringsToCheck, profile)
	if code != "" {
		return nil, code
	}
	facts.Categories = unique(append(facts.Categories, additional...))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, "inspection_profile_invalid"
		}
		for _, candidate := range stringsToCheck {
			if re.MatchString(candidate) {
				return facts.Categories, "configured_pattern_match"
			}
		}
	}
	return facts.Categories, ""
}
func unique(v []string) []string { slices.Sort(v); return slices.Compact(v) }

// Evaluate is pure: it authorizes authenticated context, not HTTP execution or replay consumption.
func Evaluate(c Config, r Request, now time.Time) Decision {
	d := Decision{Outcome: "deny", Code: "catalog_invalid", Rules: []string{}, CatalogVersion: c.Version, CatalogDigest: ConfigDigest(c), RequestDigest: RequestDigest(r), Kind: r.Kind, ApplicationID: r.ApplicationID, PrincipalID: r.PrincipalID, AllowedDocumentIDs: []string{}, Categories: []string{}, Coverage: []string{"catalog_acl_and_exact_allowlists", "bounded_four_format_categories", "configured_regex_not_general_prompt_injection_detection", "evaluation_is_not_execution"}}
	if !validID(d.CatalogVersion) {
		d.CatalogVersion = "invalid"
	}
	if !validID(d.ApplicationID) {
		d.ApplicationID = ""
	}
	if !validID(d.PrincipalID) {
		d.PrincipalID = ""
	}
	if !slices.Contains([]string{"rag", "model", "tool"}, d.Kind) {
		d.Kind = "unknown"
	}
	deny := func(code string) Decision {
		d.Outcome = "deny"
		d.Code = code
		d.Rules = append(d.Rules, code)
		d.AllowedDocumentIDs = []string{}
		return d
	}
	if ValidateConfig(c) != nil {
		return deny("catalog_invalid")
	}
	if c.Policy.Inspection != nil {
		d.Coverage = append(d.Coverage, "versioned_classification_profile:"+hash(c.Policy.Inspection), "credential_shape_and_business_checksum_are_not_validity_or_secrecy_proof")
	}
	if !validID(r.RequestID) || len(r.RequestID) < 16 || !validID(r.ApplicationID) || !validID(r.PrincipalID) || !slices.Contains([]string{"rag", "model", "tool"}, r.Kind) || r.IssuedAt.IsZero() || r.ExpiresAt.IsZero() || !r.ExpiresAt.After(r.IssuedAt) || r.ExpiresAt.Sub(r.IssuedAt) > time.Duration(c.Policy.MaxRequestAgeSeconds)*time.Second || r.IssuedAt.After(now.Add(5*time.Second)) || !r.ExpiresAt.After(now) || now.Sub(r.IssuedAt) > time.Duration(c.Policy.MaxRequestAgeSeconds)*time.Second {
		return deny("request_invalid_or_expired")
	}
	var app *Application
	var principal *Principal
	for i := range c.Applications {
		if c.Applications[i].ID == r.ApplicationID {
			app = &c.Applications[i]
		}
	}
	for i := range c.Principals {
		if c.Principals[i].ID == r.PrincipalID {
			principal = &c.Principals[i]
		}
	}
	if app == nil || !app.Enabled || principal == nil || !principal.Enabled {
		return deny("application_or_principal_unknown")
	}
	if !authorizationCurrent(principal.AuthorizationExpiresAt, c.Policy.RequireAuthorizationLeases, now) {
		return deny("principal_authorization_lease_missing_or_expired")
	}
	if principal.AuthorizationExpiresAt == nil {
		d.Coverage = append(d.Coverage, "principal_authorization_without_expiry")
	} else {
		d.Coverage = append(d.Coverage, "principal_authorization_lease_checked")
	}
	if !slices.Contains(app.PrincipalIDs, principal.ID) || !slices.Contains(principal.ApplicationIDs, app.ID) {
		return deny("application_principal_not_allowed")
	}
	purpose := ""
	if app.RequireTask && r.TaskID == "" {
		return deny("task_required")
	}
	if r.TaskID != "" || r.taskContext != nil {
		ctx := r.taskContext
		if !validID(r.TaskID) || ctx == nil || ctx.TaskID != r.TaskID || !validID(ctx.Purpose) || !digest.MatchString(ctx.ScopeDigest) || !digest.MatchString(ctx.LeaseChainDigest) || ctx.LeaseProtocolVersion != gatewaygovernance.TaskLeaseProtocolVersion || !ctx.ExpiresAt.After(now) || !categories(ctx.Categories) || !list(ctx.SourceDocumentIDs, 64, validID) {
			return deny("task_context_required_or_invalid")
		}
		purpose = ctx.Purpose
		d.TaskID, d.TaskPurpose, d.TaskScopeDigest = r.TaskID, purpose, ctx.ScopeDigest
		d.LeaseChainDigest, d.LeaseProtocolVersion = ctx.LeaseChainDigest, ctx.LeaseProtocolVersion
		d.Coverage = append(d.Coverage, "trusted_task_scope_and_context_labels", "immutable_task_execution_lease_chain")
	}
	if !ids(r.DocumentIDs) || len(r.DocumentIDs) > 64 {
		return deny("document_scope_invalid")
	}
	docByID := map[string]Document{}
	for _, doc := range c.Documents {
		docByID[doc.ID] = doc
	}
	trusted := []string{}
	sourceIDs := append([]string{}, r.DocumentIDs...)
	if r.taskContext != nil {
		trusted = append(trusted, r.taskContext.Categories...)
		sourceIDs = append(sourceIDs, r.taskContext.SourceDocumentIDs...)
	}
	sourceDocs := []Document{}
	for _, id := range unique(sourceIDs) {
		doc, ok := docByID[id]
		if !ok || !doc.Enabled || !slices.Contains(app.DocumentIDs, id) || !slices.Contains(doc.AllowedPrincipalIDs, principal.ID) {
			return deny("document_acl_denied")
		}
		if !authorizationCurrent(doc.AuthorizationExpiresAt, c.Policy.RequireAuthorizationLeases, now) {
			return deny("document_authorization_lease_missing_or_expired")
		}
		if c.Policy.RequirePinnedSourceContent && doc.IntegrityDigest == "" {
			return deny("document_content_pin_required")
		}
		if code := sourceUseAllowed(doc, purpose, nil, false, now); code != "" {
			return deny(code)
		}
		trusted = append(trusted, doc.Categories...)
		sourceDocs = append(sourceDocs, doc)
		if slices.Contains(r.DocumentIDs, id) {
			d.AllowedDocumentIDs = append(d.AllowedDocumentIDs, id)
		}
	}
	if int64(len(r.Input))+r.sourceInputBytes > int64(c.Policy.MaxInputBytes) || r.sourceInputBytes < 0 {
		return deny("input_content_budget_or_encoding")
	}
	if len(r.Output) > c.Policy.MaxOutputBytes {
		return deny("output_content_budget_or_encoding")
	}
	inputText, outputText := r.Input, r.Output
	if r.mediaInputProjection != nil {
		inputText = *r.mediaInputProjection
	}
	if r.mediaOutputProjection != nil {
		outputText = *r.mediaOutputProjection
	}
	d.MediaDigests = unique(r.mediaDigests)
	for _, source := range r.sourceEvidence {
		d.SourceDigests = append(d.SourceDigests, sourceEvidenceDigest(source))
	}
	if r.taskContext != nil {
		d.SourceDigests = append(d.SourceDigests, r.taskContext.SourceDigests...)
	}
	d.SourceDigests = unique(d.SourceDigests)
	if len(d.SourceDigests) > 0 {
		d.Coverage = append(d.Coverage, "source_id_version_acl_and_actual_content_digest")
	}
	if len(d.MediaDigests) > 0 {
		d.MediaInspectionVersion = MediaInspectionVersion
		d.Coverage = append(d.Coverage, MediaInspectionVersion)
	}
	input, code := inspect(inputText, c.Policy.MaxInputBytes, c.Policy.BlockedInputPatterns, c.Policy.Inspection)
	input = unique(append(input, r.mediaInputCategories...))
	if code != "" {
		return deny("input_" + code)
	}
	if r.sourceInputProjection != nil {
		sourceCategories, sourceCode := inspect(*r.sourceInputProjection, c.Policy.MaxInputBytes, c.Policy.BlockedInputPatterns, c.Policy.Inspection)
		if sourceCode != "" {
			return deny("source_input_" + sourceCode)
		}
		input = unique(append(input, sourceCategories...))
	}
	input = unique(append(input, r.sourceInputCategories...))
	output, code := inspect(outputText, c.Policy.MaxOutputBytes, c.Policy.BlockedOutputPatterns, c.Policy.Inspection)
	output = unique(append(output, r.mediaOutputCategories...))
	if code != "" {
		return deny("output_" + code)
	}
	d.Categories = unique(append(append([]string{}, input...), output...))
	if !allowedCategories(input, c.Policy.AllowedInputCategories, nil, false) {
		return deny("input_category_denied")
	}
	if !allowedCategories(output, c.Policy.AllowedOutputCategories, nil, false) {
		return deny("output_category_denied")
	}
	switch r.Kind {
	case "rag":
		if len(r.DocumentIDs) == 0 || r.ModelID != "" || r.ToolID != "" || r.Action != "" || r.Target != "" || len(r.Arguments) != 0 || r.ApprovalID != "" {
			return deny("rag_scope_invalid")
		}
		if !allowedCategories(trusted, c.Policy.AllowedOutputCategories, nil, false) {
			return deny("document_category_denied")
		}
		d.Categories = unique(append(d.Categories, trusted...))
	case "model":
		if r.ToolID != "" || r.Action != "" || r.Target != "" || len(r.Arguments) != 0 || r.ApprovalID != "" {
			return deny("model_scope_invalid")
		}
		var model *Model
		for i := range c.Models {
			if c.Models[i].ID == r.ModelID {
				model = &c.Models[i]
			}
		}
		if model == nil || !model.Enabled || !slices.Contains(app.ModelIDs, model.ID) || !slices.Contains(model.AllowedApplicationIDs, app.ID) {
			return deny("model_not_allowed")
		}
		// A JSON model request must obey the same fixed-selector text adapter as
		// execution. Plain text remains a non-executable policy preview only.
		if json.Valid([]byte(r.Input)) {
			if _, err := modelRequestBody(r.Input, *model); err != nil {
				return deny("model_payload_target_or_protocol_invalid")
			}
			if _, assets, _, err := parseModelPayload(r.Input, *model); err != nil || len(assets) > 0 && r.mediaInputProjection == nil {
				return deny("media_input_inspection_required")
			}
		}
		if !slices.Contains(c.Policy.AllowedModelRegions, model.Region) {
			return deny("model_region_denied")
		}
		if c.Policy.RequireDataHandling && !currentDataHandling(model.Handling, now) {
			return deny("destination_handling_missing_or_expired")
		}
		// Conservatively include the catalog data available to this app+principal even
		// if a caller omits documentIds. Omission cannot downgrade trusted categories.
		for _, id := range app.DocumentIDs {
			doc := docByID[id]
			if doc.Enabled && slices.Contains(doc.AllowedPrincipalIDs, principal.ID) {
				if !authorizationCurrent(doc.AuthorizationExpiresAt, c.Policy.RequireAuthorizationLeases, now) {
					return deny("document_authorization_lease_missing_or_expired")
				}
				trusted = append(trusted, doc.Categories...)
				if !slices.Contains(sourceIDs, id) {
					sourceDocs = append(sourceDocs, doc)
				}
			}
		}
		for _, doc := range sourceDocs {
			if code := sourceUseAllowed(doc, purpose, model.Handling, true, now); code != "" {
				return deny(code)
			}
		}
		input = unique(append(input, trusted...))
		d.Categories = unique(append(d.Categories, input...))
		if !allowedCategories(input, c.Policy.AllowedInputCategories, model.AllowedInputCategories, true) {
			return deny("model_input_category_denied")
		}
		// Context is not silently declassified by a benign-looking summary. Apply the
		// trusted/input categories to outputs even when no four-format signal remains.
		output = unique(append(output, input...))
		if !allowedCategories(output, c.Policy.AllowedOutputCategories, model.AllowedOutputCategories, true) {
			return deny("model_output_category_denied")
		}
	case "tool":
		if r.ModelID != "" || len(r.DocumentIDs) != 0 || r.Input != "" || r.Output != "" || len(r.Arguments) > 32 {
			return deny("tool_scope_invalid")
		}
		var tool *Tool
		for i := range c.Tools {
			if c.Tools[i].ID == r.ToolID {
				tool = &c.Tools[i]
			}
		}
		if tool == nil || !tool.Enabled || !slices.Contains(app.ToolIDs, tool.ID) || !slices.Contains(tool.AllowedApplicationIDs, app.ID) || !slices.Contains(tool.AllowedPrincipalIDs, principal.ID) {
			return deny("tool_not_allowed")
		}
		if c.Policy.RequireDataHandling && !currentDataHandling(tool.Handling, now) {
			return deny("destination_handling_missing_or_expired")
		}
		for _, doc := range sourceDocs {
			if code := sourceUseAllowed(doc, purpose, tool.Handling, true, now); code != "" {
				return deny(code)
			}
		}
		argumentBody, _ := json.Marshal(struct {
			Target    string            `json:"target"`
			Arguments map[string]string `json:"arguments"`
		}{r.Target, r.Arguments})
		argumentCategories, code := inspect(string(argumentBody), c.Policy.MaxInputBytes, c.Policy.BlockedInputPatterns, c.Policy.Inspection)
		if code != "" {
			return deny("tool_input_" + code)
		}
		argumentCategories = unique(append(argumentCategories, trusted...))
		d.Categories = unique(append(d.Categories, argumentCategories...))
		if !allowedCategories(argumentCategories, c.Policy.AllowedInputCategories, tool.AllowedInputCategories, r.taskContext != nil || len(tool.AllowedInputCategories) > 0) {
			return deny("tool_input_category_denied")
		}
		if !slices.Contains(tool.Actions, r.Action) || !slices.Contains(tool.Targets, r.Target) {
			return deny("tool_action_or_target_denied")
		}
		if tool.Protocol == "mcp_http" {
			d.Coverage = append(d.Coverage, "mcp_http_2026_07_28")
			if len(tool.DefinitionDigests) == 0 {
				d.Coverage = append(d.Coverage, "mcp_tool_definition_unpinned")
			}
			if _, exists := r.Arguments[tool.TargetArgument]; exists {
				return deny("tool_target_argument_override")
			}
		}
		for key, value := range r.Arguments {
			allowed, ok := tool.AllowedArguments[key]
			if !ok || !slices.Contains(allowed, value) {
				return deny("tool_argument_denied")
			}
		}
		for _, key := range tool.RequiredArguments {
			if _, ok := r.Arguments[key]; !ok {
				return deny("tool_argument_missing")
			}
		}
		if tool.HighImpact {
			d.RequiresApproval = true
			var a *Approval
			for i := range c.Approvals {
				if c.Approvals[i].ID == r.ApprovalID {
					a = &c.Approvals[i]
				}
			}
			if a == nil || a.Revoked || !a.ExpiresAt.After(now) || a.RequestDigest != d.RequestDigest || a.CatalogDigest != d.CatalogDigest || a.ApplicationID != r.ApplicationID || a.PrincipalID != r.PrincipalID || a.ToolID != r.ToolID || a.Action != r.Action || a.Target != r.Target || a.ApprovedBy == r.PrincipalID || a.LeaseChainDigest != d.LeaseChainDigest || a.LeaseProtocolVersion != d.LeaseProtocolVersion {
				return deny("tool_approval_required_or_mismatched")
			}
		}
	}
	d.Outcome = "allow"
	d.Code = "allowed"
	d.Rules = append(d.Rules, "catalog_policy_satisfied")
	return d
}

func authorizationCurrent(expiry *time.Time, required bool, now time.Time) bool {
	return expiry == nil && !required || expiry != nil && expiry.After(now)
}
