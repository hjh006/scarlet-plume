package httpapi

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

type scimUserInput struct {
	Schemas     []string `json:"schemas"`
	ID          string   `json:"id,omitempty"`
	ExternalID  string   `json:"externalId"`
	UserName    string   `json:"userName"`
	DisplayName string   `json:"displayName"`
	Active      *bool    `json:"active,omitempty"`
	Name        *struct {
		Formatted       string `json:"formatted"`
		GivenName       string `json:"givenName"`
		FamilyName      string `json:"familyName"`
		MiddleName      string `json:"middleName"`
		HonorificPrefix string `json:"honorificPrefix"`
		HonorificSuffix string `json:"honorificSuffix"`
	} `json:"name,omitempty"`
	Emails []struct {
		Value   string `json:"value"`
		Type    string `json:"type,omitempty"`
		Primary bool   `json:"primary,omitempty"`
	} `json:"emails,omitempty"`
	Meta json.RawMessage `json:"meta,omitempty"`
}
type scimMember struct {
	Value   string `json:"value"`
	Ref     string `json:"$ref,omitempty"`
	Display string `json:"display,omitempty"`
}
type scimGroupInput struct {
	Schemas     []string        `json:"schemas"`
	ID          string          `json:"id,omitempty"`
	ExternalID  string          `json:"externalId"`
	DisplayName string          `json:"displayName"`
	Members     []scimMember    `json:"members"`
	Meta        json.RawMessage `json:"meta,omitempty"`
}
type scimPatch struct {
	Schemas    []string `json:"schemas"`
	Operations []struct {
		Op    string          `json:"op"`
		Path  string          `json:"path,omitempty"`
		Value json.RawMessage `json:"value,omitempty"`
	} `json:"Operations"`
}

func scimSchemas(values []string, want string) bool { return len(values) == 1 && values[0] == want }
func scimMembers(values []scimMember) ([]int64, error) {
	if len(values) > 1000 {
		return nil, store.ErrInvalid
	}
	ids := []int64{}
	for _, v := range values {
		id, e := strconv.ParseInt(v.Value, 10, 64)
		if e != nil || id < 1 || slices.Contains(ids, id) {
			return nil, store.ErrInvalid
		}
		ids = append(ids, id)
	}
	return ids, nil
}
func scimDecode(raw json.RawMessage, out any) error {
	if string(raw) == "null" {
		return store.ErrInvalid
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func applySCIMUserPatch(base store.IdentityUser, p scimPatch) (store.IdentityUser, error) {
	if !scimSchemas(p.Schemas, scimPatchSchema) || len(p.Operations) == 0 || len(p.Operations) > 100 {
		return base, store.ErrInvalid
	}
	for _, op := range p.Operations {
		if strings.ToLower(op.Op) != "replace" && strings.ToLower(op.Op) != "add" {
			return base, store.ErrInvalid
		}
		switch strings.ToLower(op.Path) {
		case "active":
			if e := scimDecode(op.Value, &base.Active); e != nil {
				return base, store.ErrInvalid
			}
		case "username":
			if e := scimDecode(op.Value, &base.UserName); e != nil {
				return base, store.ErrInvalid
			}
		case "displayname":
			if e := scimDecode(op.Value, &base.DisplayName); e != nil {
				return base, store.ErrInvalid
			}
		case "":
			var values map[string]json.RawMessage
			if e := scimDecode(op.Value, &values); e != nil || len(values) == 0 {
				return base, store.ErrInvalid
			}
			for k, v := range values {
				switch strings.ToLower(k) {
				case "active":
					if scimDecode(v, &base.Active) != nil {
						return base, store.ErrInvalid
					}
				case "username":
					if scimDecode(v, &base.UserName) != nil {
						return base, store.ErrInvalid
					}
				case "displayname":
					if scimDecode(v, &base.DisplayName) != nil {
						return base, store.ErrInvalid
					}
				default:
					return base, store.ErrInvalid
				}
			}
		default:
			return base, store.ErrInvalid
		}
	}
	return base, nil
}
func applySCIMGroupPatch(base store.IdentityGroup, p scimPatch) (store.IdentityGroup, error) {
	if !scimSchemas(p.Schemas, scimPatchSchema) || len(p.Operations) == 0 || len(p.Operations) > 100 {
		return base, store.ErrInvalid
	}
	for _, op := range p.Operations {
		kind, path := strings.ToLower(op.Op), strings.ToLower(op.Path)
		if kind != "add" && kind != "replace" && kind != "remove" {
			return base, store.ErrInvalid
		}
		if path == "displayname" && (kind == "replace" || kind == "add") {
			if scimDecode(op.Value, &base.DisplayName) != nil {
				return base, store.ErrInvalid
			}
			continue
		}
		if strings.HasPrefix(path, `members[value eq "`) && strings.HasSuffix(path, `"]`) && kind == "remove" {
			raw := op.Path[len(`members[value eq `) : len(op.Path)-1]
			var value string
			if json.Unmarshal([]byte(raw), &value) != nil {
				return base, store.ErrInvalid
			}
			id, e := strconv.ParseInt(value, 10, 64)
			if e != nil || id < 1 {
				return base, store.ErrInvalid
			}
			base.Members = slices.DeleteFunc(base.Members, func(n int64) bool { return n == id })
			continue
		}
		if path == "" && (kind == "replace" || kind == "add") {
			var values map[string]json.RawMessage
			if scimDecode(op.Value, &values) != nil || len(values) == 0 {
				return base, store.ErrInvalid
			}
			for k, v := range values {
				if strings.ToLower(k) != "members" && strings.ToLower(k) != "displayname" {
					return base, store.ErrInvalid
				}
				single := scimPatch{Schemas: p.Schemas, Operations: []struct {
					Op    string          `json:"op"`
					Path  string          `json:"path,omitempty"`
					Value json.RawMessage `json:"value,omitempty"`
				}{{Op: kind, Path: k, Value: v}}}
				var e error
				base, e = applySCIMGroupPatch(base, single)
				if e != nil {
					return base, e
				}
			}
			continue
		}
		if path != "members" {
			return base, store.ErrInvalid
		}
		if kind == "remove" && len(op.Value) == 0 {
			base.Members = []int64{}
			continue
		}
		var members []scimMember
		if scimDecode(op.Value, &members) != nil {
			return base, store.ErrInvalid
		}
		ids, e := scimMembers(members)
		if e != nil {
			return base, e
		}
		switch kind {
		case "replace":
			base.Members = ids
		case "add":
			for _, id := range ids {
				if !slices.Contains(base.Members, id) {
					base.Members = append(base.Members, id)
				}
			}
		case "remove":
			base.Members = slices.DeleteFunc(base.Members, func(id int64) bool { return slices.Contains(ids, id) })
		}
		if len(base.Members) > 1000 {
			return base, store.ErrInvalid
		}
	}
	return base, nil
}

func (s *server) scimWrite(w http.ResponseWriter, r *http.Request) {
	h, ok := s.scimAuth(w, r)
	if !ok {
		return
	}
	id := int64(0)
	var e error
	if r.Method != "POST" {
		id, e = scimID(r)
		if e != nil {
			scimFailure(w, e)
			return
		}
	}
	status := 200
	if id == 0 {
		status = 201
	}
	if scimIsGroup(r) {
		var base store.IdentityGroup
		if id != 0 {
			items, _, e := s.store.SCIMIdentityGroups(r.Context(), r.PathValue("slug"), h, id, "", "", 0, 1)
			if e != nil {
				scimFailure(w, e)
				return
			}
			if len(items) == 0 {
				scimFailure(w, store.ErrNotFound)
				return
			}
			base = items[0]
			if !scimMatch(w, r, base.Revision) {
				return
			}
		}
		var in store.IdentityGroup
		if r.Method == "PATCH" {
			var patch scimPatch
			if !readIdentityJSON(w, r, &patch) {
				return
			}
			in, e = applySCIMGroupPatch(base, patch)
		} else {
			var body scimGroupInput
			if !readIdentityJSON(w, r, &body) {
				return
			}
			if !scimSchemas(body.Schemas, scimGroupSchema) || (body.ID != "" && body.ID != strconv.FormatInt(id, 10)) {
				scimFailure(w, store.ErrInvalid)
				return
			}
			in = store.IdentityGroup{ExternalID: body.ExternalID, DisplayName: body.DisplayName}
			in.Members, e = scimMembers(body.Members)
		}
		if e != nil {
			scimFailure(w, e)
			return
		}
		out, e := s.store.SaveSCIMIdentityGroup(r.Context(), r.PathValue("slug"), h, id, in, base.Revision)
		if e != nil {
			scimFailure(w, e)
			return
		}
		w.Header().Set("ETag", scimVersion(out.Revision))
		w.Header().Set("Location", s.scimLocation(r, "Groups", out.ID))
		scimRespond(w, status, s.scimGroup(r, out))
	} else {
		var base store.IdentityUser
		if id != 0 {
			items, _, e := s.store.SCIMIdentityUsers(r.Context(), r.PathValue("slug"), h, id, "", "", 0, 1)
			if e != nil {
				scimFailure(w, e)
				return
			}
			if len(items) == 0 {
				scimFailure(w, store.ErrNotFound)
				return
			}
			base = items[0]
			if !scimMatch(w, r, base.Revision) {
				return
			}
		}
		var in store.IdentityUser
		if r.Method == "PATCH" {
			var patch scimPatch
			if !readIdentityJSON(w, r, &patch) {
				return
			}
			in, e = applySCIMUserPatch(base, patch)
		} else {
			var body scimUserInput
			if !readIdentityJSON(w, r, &body) {
				return
			}
			if !scimSchemas(body.Schemas, scimUserSchema) || (body.ID != "" && body.ID != strconv.FormatInt(id, 10)) || len(body.Emails) > 10 {
				scimFailure(w, store.ErrInvalid)
				return
			}
			in = store.IdentityUser{ExternalID: body.ExternalID, UserName: body.UserName, DisplayName: body.DisplayName, Active: true}
			if body.Active != nil {
				in.Active = *body.Active
			}
		}
		if e != nil {
			scimFailure(w, e)
			return
		}
		out, e := s.store.SaveSCIMIdentityUser(r.Context(), r.PathValue("slug"), h, id, in, base.Revision)
		if e != nil {
			scimFailure(w, e)
			return
		}
		w.Header().Set("ETag", scimVersion(out.Revision))
		w.Header().Set("Location", s.scimLocation(r, "Users", out.ID))
		scimRespond(w, status, s.scimUser(r, out))
	}
}
