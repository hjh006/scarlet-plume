package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/runtimesecurity"
)

const (
	queueCapacity = 512
	maxBatchSize  = 100
	batchTimeout  = 4 * time.Second
)

type observationEvent struct {
	ClientIP           string                             `json:"clientIp,omitempty"`
	Transport          string                             `json:"transport,omitempty"`
	TransportHop       string                             `json:"transportHop,omitempty"`
	Context            *runtimesecurity.RuntimeContext    `json:"context,omitempty"`
	ValueFingerprints  []runtimesecurity.ValueFingerprint `json:"valueFingerprints,omitempty"`
	Method             string                             `json:"method"`
	Route              string                             `json:"route"`
	Status             int                                `json:"status"`
	ObservedAt         time.Time                          `json:"observedAt"`
	EventID            string                             `json:"eventId"`
	DataFields         []runtimesecurity.DataField        `json:"dataFields,omitempty"`
	CaptureWarnings    []string                           `json:"captureWarnings,omitempty"`
	SecuritySignals    []runtimesecurity.SecuritySignal   `json:"securitySignals,omitempty"`
	InspectedLocations []string                           `json:"inspectedLocations,omitempty"`
}

type observer struct {
	queue    chan observationEvent
	overflow atomic.Uint64
	client   *http.Client
	endpoint string
	token    string
}

func newObserver(endpoint, token string) *observer {
	return &observer{
		queue: make(chan observationEvent, queueCapacity),
		client: &http.Client{
			Transport:     directTransport(),
			Timeout:       1200 * time.Millisecond,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		endpoint: endpoint,
		token:    token,
	}
}

func (o *observer) enqueue(event observationEvent) {
	select {
	case o.queue <- event:
	default:
		o.overflow.Add(1)
	}
}

func (o *observer) run(ctx context.Context) {
	batch := make([]observationEvent, 0, maxBatchSize)
	var failed uint64
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	report := func() {
		if overflow := o.overflow.Swap(0); overflow > 0 || failed > 0 {
			slog.Warn("runtime observations dropped", "queue_full", overflow, "delivery_failed", failed)
			failed = 0
		}
	}
	defer report()
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			report()
		case event := <-o.queue:
			batch = append(batch[:0], event)
		drain:
			for len(batch) < cap(batch) {
				select {
				case event := <-o.queue:
					batch = append(batch, event)
				default:
					break drain
				}
			}
			if err := o.deliver(ctx, batch); err != nil {
				failed += uint64(len(batch))
			}
		}
	}
}

func (o *observer) deliver(ctx context.Context, events []observationEvent) error {
	for i := range events {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return errors.New("cannot create runtime event identity")
		}
		events[i].EventID = hex.EncodeToString(random[:])
	}
	body, err := json.Marshal(events)
	if err != nil {
		return errors.New("cannot encode runtime observations")
	}

	ctx, cancel := context.WithTimeout(ctx, batchTimeout)
	defer cancel()
	for attempt := range 3 {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, bytes.NewReader(body))
		if err != nil {
			return errors.New("cannot create runtime delivery")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+o.token)
		response, err := o.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		status := response.StatusCode
		switch {
		case status >= 200 && status < 300:
			return nil
		case status == 408 || status == 425 || status == 429 || status >= 500 && status < 600:
			continue
		default:
			return errors.New("runtime receiver rejected observations")
		}
	}
	return errors.New("runtime receiver unavailable")
}
