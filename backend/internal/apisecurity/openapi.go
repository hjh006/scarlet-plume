package apisecurity

import (
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hjh006/scarlet-plume/backend/internal/collector"
)

type Scan struct {
	Version              string
	Operations           []Operation
	UnresolvedReferences int
}

type Operation struct {
	Method          string           `json:"method"`
	Path            string           `json:"path"`
	Auth            string           `json:"auth"`
	Deprecated      bool             `json:"deprecated"`
	SensitiveFields []SensitiveField `json:"sensitiveFields"`
	Findings        []Finding        `json:"findings"`
	AssetID         int64            `json:"assetId,omitempty"`
	CheckID         int64            `json:"checkId,omitempty"`
}

type SensitiveField struct {
	Location string `json:"location"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

type Finding struct {
	Code     string `json:"code"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
}

type object = map[string]any

const MaxOperations = 200
const maxNodes = 20000
const maxCandidates = 3000

var versionPattern = regexp.MustCompile(`^3\.(0|1)\.[0-9]{1,4}$`)
var objectIDPattern = regexp.MustCompile(`(?i)\{[a-z0-9_]*(id|uuid)\}`)
var methods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

type inspector struct {
	root       object
	nodes      int
	candidates int
	unresolved int
}

func Analyze(document json.RawMessage) (Scan, error) {
	var root object
	if err := json.Unmarshal(document, &root); err != nil {
		return Scan{}, errors.New("请上传有效的 OpenAPI JSON 对象。")
	}
	version, _ := root["openapi"].(string)
	if !versionPattern.MatchString(version) {
		return Scan{}, errors.New("请选择 OpenAPI 3.0 或 3.1 JSON 规范。")
	}
	paths, _ := root["paths"].(object)
	if len(paths) == 0 {
		return Scan{}, errors.New("规范中没有 paths 接口定义。")
	}
	i := inspector{root: root}
	scan := Scan{Version: version, Operations: make([]Operation, 0)}
	for _, path := range slices.Sorted(maps.Keys(paths)) {
		if strings.HasPrefix(path, "x-") {
			continue
		}
		if !safePath(path) {
			return Scan{}, errors.New("规范包含无效或过长的接口路径。")
		}
		item := i.resolve(paths[path])
		for _, method := range methods {
			operation, ok := item[method].(object)
			if !ok {
				continue
			}
			if len(scan.Operations) == MaxOperations {
				return Scan{}, errors.New("单次扫描最多支持 200 个接口操作。")
			}
			result := i.operation(strings.ToUpper(method), path, item, operation)
			if i.nodes > maxNodes || i.candidates > maxCandidates {
				return Scan{}, errors.New("规范中的字段结构超出单次分析上限。")
			}
			scan.Operations = append(scan.Operations, result)
		}
	}
	if len(scan.Operations) == 0 {
		return Scan{}, errors.New("规范中没有可分析的 HTTP 操作。")
	}
	scan.UnresolvedReferences = i.unresolved
	return scan, nil
}

func safePath(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") && !strings.ContainsAny(value, "?#") && metadataName(value, 240)
}

func metadataName(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= limit &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func (i *inspector) resolve(value any) object {
	current, _ := value.(object)
	for depth := 0; depth < 16; depth++ {
		ref, referenced := current["$ref"]
		if !referenced {
			return current
		}
		path, _ := ref.(string)
		if !strings.HasPrefix(path, "#/components/") {
			break
		}
		pointer, err := url.PathUnescape(path[2:])
		if err != nil {
			break
		}
		current = i.root
		for _, key := range strings.Split(pointer, "/") {
			key = strings.ReplaceAll(strings.ReplaceAll(key, "~1", "/"), "~0", "~")
			current, _ = current[key].(object)
		}
		if current == nil {
			break
		}
	}
	i.unresolved++
	return nil
}

func (i *inspector) operation(method, path string, item, spec object) Operation {
	deprecated, _ := spec["deprecated"].(bool)
	result := Operation{Method: method, Path: path, Deprecated: deprecated,
		Auth: i.declaredAuth(spec), SensitiveFields: []SensitiveField{}, Findings: []Finding{}}
	fields := make(map[SensitiveField]struct{})
	parameters := make(map[[2]string]object)
	for _, value := range []any{item["parameters"], spec["parameters"]} {
		list, _ := value.([]any)
		for _, entry := range list {
			param := i.resolve(entry)
			name, _ := param["name"].(string)
			location, _ := param["in"].(string)
			if metadataName(name, 240) {
				parameters[[2]string{location, name}] = param
			}
		}
	}
	for key, param := range parameters {
		schema := i.resolve(param["schema"])
		addCandidate(fields, "parameter", key[1], classify(key[1], schema))
		i.inspectSchema(fields, "parameter", key[1], schema, 0)
		i.inspectContent(fields, "parameter", param)
	}
	i.inspectContent(fields, "request", i.resolve(spec["requestBody"]))
	responses, _ := spec["responses"].(object)
	for _, key := range slices.Sorted(maps.Keys(responses)) {
		i.inspectContent(fields, "response", i.resolve(responses[key]))
	}
	result.SensitiveFields = slices.AppendSeq(result.SensitiveFields, maps.Keys(fields))
	slices.SortFunc(result.SensitiveFields, func(a, b SensitiveField) int {
		if cmp := strings.Compare(a.Location, b.Location); cmp != 0 {
			return cmp
		}
		if cmp := strings.Compare(a.Name, b.Name); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Category, b.Category)
	})
	i.candidates += len(fields)
	result.Findings = findings(result)
	return result
}

func (i *inspector) inspectContent(fields map[SensitiveField]struct{}, location string, container object) {
	content, _ := container["content"].(object)
	for _, key := range slices.Sorted(maps.Keys(content)) {
		media, _ := content[key].(object)
		i.inspectSchema(fields, location, "", media["schema"], 0)
	}
}

func (i *inspector) inspectSchema(fields map[SensitiveField]struct{}, location, prefix string, value any, depth int) {
	if value == nil || i.nodes > maxNodes || len(fields) > maxCandidates {
		return
	}
	if depth > 20 {
		i.unresolved++
		return
	}
	i.nodes++
	schema := i.resolve(value)
	properties, _ := schema["properties"].(object)
	for _, name := range slices.Sorted(maps.Keys(properties)) {
		if !metadataName(name, 120) {
			i.unresolved++
			continue
		}
		child := i.resolve(properties[name])
		qualified := name
		if prefix != "" {
			qualified = prefix + "." + name
		}
		if metadataName(qualified, 240) {
			addCandidate(fields, location, qualified, classify(name, child))
			i.inspectSchema(fields, location, qualified, child, depth+1)
		} else {
			i.unresolved++
		}
	}
	i.inspectSchema(fields, location, prefix, schema["items"], depth+1)
	for _, group := range []string{"allOf", "oneOf", "anyOf"} {
		alternatives, _ := schema[group].([]any)
		for _, alternative := range alternatives {
			i.inspectSchema(fields, location, prefix, alternative, depth+1)
		}
	}
}

func addCandidate(fields map[SensitiveField]struct{}, location, name, category string) {
	if category != "" {
		fields[SensitiveField{Location: location, Name: name, Category: category}] = struct{}{}
	}
}

func classify(name string, schema object) string {
	format, _ := schema["format"].(string)
	switch format {
	case "email", "idn-email":
		return "email"
	case "password":
		return "credential"
	case "ipv4", "ipv6":
		return "ip_address"
	}
	fieldType, _ := schema["type"].(string)
	return collector.MetadataSensitivity(name, fieldType)
}

func (i *inspector) declaredAuth(operation object) string {
	value, exists := operation["security"]
	if !exists {
		value, exists = i.root["security"]
	}
	if !exists {
		return "unspecified"
	}
	list, ok := value.([]any)
	if !ok {
		return "invalid"
	}
	if len(list) == 0 {
		return "anonymous"
	}
	components, _ := i.root["components"].(object)
	schemes, _ := components["securitySchemes"].(object)
	anonymous := false
	for _, entry := range list {
		requirements, ok := entry.(object)
		if !ok {
			return "invalid"
		}
		anonymous = anonymous || len(requirements) == 0
		for name, scopes := range requirements {
			scheme := i.resolve(schemes[name])
			switch scheme["type"] {
			case "apiKey", "http", "oauth2", "openIdConnect", "mutualTLS":
			default:
				return "invalid"
			}
			if scopeList, ok := scopes.([]any); !ok || slices.ContainsFunc(scopeList, func(value any) bool {
				_, ok := value.(string)
				return !ok
			}) {
				return "invalid"
			}
		}
	}
	if anonymous {
		return "anonymous"
	}
	return "required"
}

func findings(operation Operation) []Finding {
	result := make([]Finding, 0)
	responseCandidate := slices.ContainsFunc(operation.SensitiveFields, func(field SensitiveField) bool {
		return field.Location == "response"
	})
	credentialResponse := slices.ContainsFunc(operation.SensitiveFields, func(field SensitiveField) bool {
		return field.Location == "response" && field.Category == "credential"
	})
	if operation.Auth == "invalid" {
		result = append(result, Finding{"invalid_security", "鉴权声明引用无效或结构不完整", "medium"})
	}
	if responseCandidate && operation.Auth != "required" {
		result = append(result, Finding{"sensitive_response_auth", "响应声明疑似敏感字段，需核对实际鉴权和返回范围", "high"})
	} else if operation.Auth == "anonymous" || operation.Auth == "unspecified" {
		result = append(result, Finding{"authentication_review", "接口允许匿名或未声明鉴权，需核对开放范围", "medium"})
	}
	if credentialResponse {
		result = append(result, Finding{"credential_response", "响应声明疑似凭据字段，需确认用途与返回范围", "high"})
	}
	if objectIDPattern.MatchString(operation.Path) {
		result = append(result, Finding{"object_authorization_review", "路径含对象标识，需验证对象级访问授权", "medium"})
	}
	if operation.Deprecated {
		result = append(result, Finding{"deprecated_operation", "规范已标记弃用，需核对下线与实际暴露范围", "low"})
	}
	return result
}
