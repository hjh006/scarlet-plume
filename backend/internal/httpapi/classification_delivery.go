package httpapi

import (
	"encoding/base64"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"net/http"
	"strings"
)

const classificationDeliveryClaimPath = "/api/v1/classification-delivery-agent/claim"
const classificationDeliveryReceiptPath = "/api/v1/classification-delivery-agent/receipts"
const classificationDeliveryTokenPrefix = "spcd_"

func (s *server) classificationDeliveryRoutes(mux *http.ServeMux) {
	const base = "/api/v1/workspaces/{slug}/classification"
	mux.HandleFunc("GET "+base+"/delivery-targets", s.workspace(false, s.classificationDeliveryTargets))
	mux.HandleFunc("POST "+base+"/delivery-targets", s.workspace(false, s.saveClassificationDeliveryTarget))
	mux.HandleFunc("PUT "+base+"/delivery-targets/{id}", s.workspace(false, s.saveClassificationDeliveryTarget))
	mux.HandleFunc("POST "+base+"/delivery-targets/{id}/rotate", s.workspace(false, s.rotateClassificationDeliveryTarget))
	mux.HandleFunc("GET "+base+"/deliveries", s.workspace(false, s.classificationDeliveries))
	mux.HandleFunc("POST "+base+"/publications/{id}/deliveries", s.workspace(false, s.createClassificationDelivery))
	mux.HandleFunc("POST /api/v1/workspaces/{slug}/operations/events/{id}/deliveries", s.workspace(false, s.createSecurityEventDelivery))
	mux.HandleFunc("POST "+classificationDeliveryClaimPath, s.claimClassificationDelivery)
	mux.HandleFunc("POST "+classificationDeliveryReceiptPath, s.receiveClassificationDelivery)
}
func (s *server) classificationDeliveryTargets(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	out, e := s.store.ClassificationDeliveryTargets(r.Context(), ws.ID, u.ID)
	if e != nil {
		classificationWorkbenchFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"items": out})
}
func (s *server) saveClassificationDeliveryTarget(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	if ws.Role != "owner" {
		classificationWorkbenchFailure(w, store.ErrForbidden)
		return
	}
	var input store.ClassificationDeliveryTargetInput
	if !readJSONLimit(w, r, &input, 8<<10) {
		return
	}
	var id int64
	token := ""
	if r.Method == http.MethodPut {
		var ok bool
		id, ok = discoveryID(w, r)
		if !ok {
			return
		}
	} else {
		random, e := sessionToken()
		if e != nil {
			storeFailure(w, e)
			return
		}
		token = classificationDeliveryTokenPrefix + random
	}
	out, e := s.store.SaveClassificationDeliveryTarget(r.Context(), ws.ID, u.ID, id, input, sessionHash(token), false)
	if e != nil {
		classificationWorkbenchFailure(w, e)
		return
	}
	if id == 0 {
		respond(w, 201, map[string]any{"target": out, "token": token})
		return
	}
	respond(w, 200, out)
}
func (s *server) rotateClassificationDeliveryTarget(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	if ws.Role != "owner" {
		classificationWorkbenchFailure(w, store.ErrForbidden)
		return
	}
	id, ok := discoveryID(w, r)
	if !ok {
		return
	}
	var input struct {
		Revision int64 `json:"revision"`
	}
	if !readJSONLimit(w, r, &input, 1024) {
		return
	}
	random, e := sessionToken()
	if e != nil {
		storeFailure(w, e)
		return
	}
	token := classificationDeliveryTokenPrefix + random
	out, e := s.store.SaveClassificationDeliveryTarget(r.Context(), ws.ID, u.ID, id, store.ClassificationDeliveryTargetInput{Revision: input.Revision}, sessionHash(token), true)
	if e != nil {
		classificationWorkbenchFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"target": out, "token": token})
}
func (s *server) classificationDeliveries(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	pub, ok := optionalQueryID(w, r, "publicationId")
	if !ok {
		return
	}
	target, ok := optionalQueryID(w, r, "targetId")
	if !ok {
		return
	}
	items, e := s.store.ClassificationDeliveries(r.Context(), ws.ID, u.ID, pub, target)
	if e != nil {
		classificationWorkbenchFailure(w, e)
		return
	}
	respond(w, 200, map[string]any{"items": items})
}
func (s *server) createClassificationDelivery(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	s.createEvidenceDelivery(w, r, u, ws, false)
}
func (s *server) createSecurityEventDelivery(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace) {
	s.createEvidenceDelivery(w, r, u, ws, true)
}
func (s *server) createEvidenceDelivery(w http.ResponseWriter, r *http.Request, u store.User, ws store.Workspace, event bool) {
	id, ok := discoveryID(w, r)
	if !ok {
		return
	}
	var input struct {
		TargetID int64 `json:"targetId"`
	}
	if !readJSONLimit(w, r, &input, 1024) {
		return
	}
	if input.TargetID < 1 {
		classificationWorkbenchFailure(w, store.ErrInvalid)
		return
	}
	var out store.ClassificationDelivery
	var e error
	if event {
		out, e = s.store.CreateSecurityEventDelivery(r.Context(), ws.ID, u.ID, id, input.TargetID)
	} else {
		out, e = s.store.CreateClassificationDelivery(r.Context(), ws.ID, u.ID, id, input.TargetID)
	}
	if e != nil {
		classificationWorkbenchFailure(w, e)
		return
	}
	respond(w, 201, out)
}
func classificationDeliveryBearer(w http.ResponseWriter, r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || !strings.HasPrefix(token, classificationDeliveryTokenPrefix) || len(token) != len(classificationDeliveryTokenPrefix)+43 {
		operationsFailure(w, store.ErrAutomationUnauthorized)
		return "", false
	}
	if _, e := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(token, classificationDeliveryTokenPrefix)); e != nil {
		operationsFailure(w, store.ErrAutomationUnauthorized)
		return "", false
	}
	return sessionHash(token), true
}
func (s *server) claimClassificationDelivery(w http.ResponseWriter, r *http.Request) {
	digest, ok := classificationDeliveryBearer(w, r)
	if !ok {
		return
	}
	var input struct {
		LeaseID string `json:"leaseId"`
	}
	if !readJSONLimit(w, r, &input, 1024) {
		return
	}
	out, e := s.store.ClaimClassificationDelivery(r.Context(), digest, input.LeaseID)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	if out == nil {
		w.WriteHeader(204)
		return
	}
	respond(w, 200, out)
}
func (s *server) receiveClassificationDelivery(w http.ResponseWriter, r *http.Request) {
	digest, ok := classificationDeliveryBearer(w, r)
	if !ok {
		return
	}
	var input store.ClassificationDeliveryReceipt
	if !readJSONLimit(w, r, &input, 4<<10) {
		return
	}
	out, e := s.store.ReceiveClassificationDelivery(r.Context(), digest, input)
	if e != nil {
		operationsFailure(w, e)
		return
	}
	respond(w, 200, out)
}
