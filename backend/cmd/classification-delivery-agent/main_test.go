package main

import (
	"context"
	"encoding/json"
	"github.com/hjh006/scarlet-plume/backend/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeliveryUsesPinnedReceiverAndAcknowledgement(t *testing.T) {
	for _, mode := range []string{"accepted", "bad_ack", "wrong_endpoint", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			const platformToken = "synthetic-platform-token"
			const receiverToken = "synthetic-receiver-token"
			payload, hash, e := store.CanonicalClassificationDelivery([]byte(`{"schemaVersion":"scarlet-evidence-delivery-v1","deliveryId":7,"snapshot":{"synthetic":true}}`))
			if e != nil {
				t.Fatal(e)
			}
			requests := 0
			redirected := 0
			var receipt store.ClassificationDeliveryReceipt
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++; w.WriteHeader(204) }))
			defer other.Close()
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("Authorization") != "Bearer "+receiverToken || r.Header.Get("Idempotency-Key") != "scarlet-delivery:7" {
					t.Error("receiver credentials or idempotency key wrong")
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != string(payload) || strings.Contains(string(body), platformToken) {
					t.Error("payload or credential boundary wrong")
				}
				if mode == "redirect" {
					w.Header().Set("Location", other.URL)
					w.WriteHeader(302)
					return
				}
				digest := hash
				if mode == "bad_ack" {
					digest = strings.Repeat("0", 64)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"deliveryId": 7, "payloadDigest": digest, "status": "accepted", "remoteRef": "fixture-7"})
			}))
			defer receiver.Close()
			platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+platformToken {
					t.Error("platform credential not isolated")
				}
				if strings.HasSuffix(r.URL.Path, "/claim") {
					var input struct{ LeaseID string }
					_ = json.NewDecoder(r.Body).Decode(&input)
					endpoint := receiver.URL
					if mode == "wrong_endpoint" {
						endpoint = other.URL
					}
					_ = json.NewEncoder(w).Encode(store.ClassificationDeliveryClaim{ClassificationDelivery: store.ClassificationDelivery{ID: 7, PayloadDigest: hash}, LeaseID: input.LeaseID, LeaseUntil: time.Now().Add(time.Minute), Endpoint: endpoint, Payload: payload})
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/receipts") {
					t.Error("unexpected platform path")
				}
				_ = json.NewDecoder(r.Body).Decode(&receipt)
				w.WriteHeader(200)
			}))
			defer platform.Close()
			cfg := configuration{Platform: platform.URL, Token: platformToken, Endpoint: receiver.URL, ReceiverToken: receiverToken, Local: true}
			client, e := newClient(cfg)
			if e != nil {
				t.Fatal(e)
			}
			worked, e := runOnce(context.Background(), cfg, client)
			if e != nil || !worked {
				t.Fatal(worked, e)
			}
			want := "failed"
			if mode == "accepted" {
				want = "accepted"
			}
			if receipt.Status != want || receipt.PayloadDigest != hash || receipt.DeliveryID != 7 {
				t.Fatalf("invalid durable receipt: %+v", receipt)
			}
			if mode == "wrong_endpoint" && requests != 0 {
				t.Fatal("unapproved target was contacted")
			}
			if mode != "wrong_endpoint" && requests != 1 {
				t.Fatal("expected exactly one approved receiver request")
			}
			if redirected != 0 {
				t.Fatal("receiver redirect followed")
			}
		})
	}
}
func TestDeliveryRequiresVerifiedOrExplicitLoopbackURLs(t *testing.T) {
	for _, raw := range []string{"http://192.0.2.10/hook", "https://user:password@example.test/hook", "https://example.test/hook?token=private", "file:///tmp/target", "https://example.test/#fragment"} {
		if validURL(raw, true) {
			t.Fatal("unsafe approved URL accepted")
		}
	}
	if validURL("http://127.0.0.1/hook", false) || !validURL("http://127.0.0.1/hook", true) || !validURL("https://receiver.example.test/hook", false) {
		t.Fatal("HTTPS/loopback policy incorrect")
	}
}
