package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

func bifrostHealthy(ctx context.Context, port int) bool {
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	request, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/health", port), nil)
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return response.StatusCode == 200
}

// A published Docker TCP port may accept connections before Bifrost finishes
// loading its model catalog. Do not open the safety gateway until HTTP is ready.
func waitBifrost(ctx context.Context, port int, childExit <-chan error) error {
	bounded, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		request, _ := http.NewRequestWithContext(bounded, "GET", fmt.Sprintf("http://127.0.0.1:%d/health", port), nil)
		if response, err := client.Do(request); err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				return nil
			}
		}
		select {
		case <-bounded.Done():
			return errors.New("Bifrost readiness deadline exceeded")
		case <-childExit:
			return errors.New("Bifrost stopped before readiness")
		case <-tick.C:
		}
	}
}
