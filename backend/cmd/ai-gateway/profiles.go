package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
)

func loadProfiles(path string, local bool) ([]aisecurity.BifrostProfile, error) {
	raw, err := privateFile(path, 1<<20)
	if err != nil {
		return nil, err
	}
	var profiles []aisecurity.BifrostProfile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		err = decoder.Decode(&profiles)
	} else {
		var p aisecurity.BifrostProfile
		err = decoder.Decode(&p)
		profiles = []aisecurity.BifrostProfile{p}
	}
	var trailing any
	if err == nil && decoder.Decode(&trailing) != io.EOF {
		err = errors.New("profile trailing JSON")
	}
	if err != nil || len(profiles) < 1 || len(profiles) > 8 {
		return nil, errors.New("invalid explicit Bifrost profiles")
	}
	for i := range profiles {
		p := &profiles[i]
		if p.EndpointRef != "" {
			if !strings.HasPrefix(p.EndpointRef, "env:SCARLET_GATEWAY_") {
				return nil, errors.New("provider endpoint environment reference required")
			}
			actual := os.Getenv(strings.TrimPrefix(p.EndpointRef, "env:"))
			if actual == "" || p.BaseURL != "" && actual != p.BaseURL {
				return nil, errors.New("provider endpoint environment unavailable or changed")
			}
			p.BaseURL = actual
		}
		if aisecurity.ValidateBifrostProfile(*p, local) != nil {
			return nil, errors.New("invalid explicit provider profile")
		}
		token := os.Getenv(p.APIKeyEnv)
		if len(token) < 16 || len(token) > 4096 {
			return nil, errors.New("provider credential environment unavailable")
		}
	}
	return profiles, nil
}
