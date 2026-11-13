package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"

	native "github.com/hjh006/scarlet-plume/backend/internal/endpointnativecontrol"
	"github.com/hjh006/scarlet-plume/backend/internal/endpointsecure"
)

const endpointServiceName = "ScarletEndpointNative"

type enterpriseTemplate struct {
	Version                 int      `json:"version"`
	PlatformOrigin          string   `json:"platformOrigin"`
	Scope                   string   `json:"scope"`
	DeviceID                string   `json:"deviceId"`
	USBWriteProtect         bool     `json:"usbWriteProtect"`
	PrintQueues             []string `json:"printQueues"`
	RDPClipboardRedirection bool     `json:"rdpClipboardRedirection"`
}
type enterpriseConfig struct {
	enterpriseTemplate
	SourceToken string `json:"sourceToken"`
	KeyHex      string `json:"keyHex"`
}

func parseEnterpriseTemplate(raw []byte) (enterpriseTemplate, error) {
	var cfg enterpriseTemplate
	if len(raw) > 32<<10 {
		return cfg, errors.New("enterprise template exceeds size limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&cfg) != nil {
		return cfg, errors.New("invalid enterprise template")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return cfg, errors.New("single enterprise template required")
	}
	if cfg.Version != 1 || len(cfg.Scope) < 1 || len(cfg.Scope) > 80 || len(cfg.DeviceID) > 256 || len(cfg.PrintQueues) > 20 {
		return cfg, errors.New("invalid enterprise scope or target budget")
	}
	seen := map[string]bool{}
	for _, name := range cfg.PrintQueues {
		key := strings.ToLower(name)
		if len(name) < 1 || len(name) > 256 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\\/,\x00\r\n\t") || seen[key] {
			return cfg, errors.New("explicit unique local printer queue names required")
		}
		seen[key] = true
	}
	sort.Strings(cfg.PrintQueues)
	if !cfg.USBWriteProtect && !cfg.RDPClipboardRedirection && len(cfg.PrintQueues) == 0 {
		return cfg, errors.New("enable at least one explicit enterprise control target")
	}
	return cfg, nil
}
func provisionEnterprise(templatePath, dir string) error {
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		return errors.New("enterprise template unavailable")
	}
	t, err := parseEnterpriseTemplate(raw)
	if err != nil {
		return err
	}
	if t.DeviceID == "" {
		id := make([]byte, 32)
		if _, err = rand.Read(id); err != nil {
			return errors.New("device identity generation failed")
		}
		t.DeviceID = hex.EncodeToString(id)
	}
	cfg := enterpriseConfig{enterpriseTemplate: t, SourceToken: os.Getenv("SCARLET_OPERATIONS_TOKEN"), KeyHex: os.Getenv("SCARLET_ENDPOINT_KEY_HEX")}
	if cfg.KeyHex == "" {
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return errors.New("source key generation failed")
		}
		cfg.KeyHex = hex.EncodeToString(key)
		clear(key)
	}
	if _, err = cfg.client(); err != nil {
		return err
	}
	store, err := endpointsecure.Open(dir)
	if err != nil {
		return err
	}
	defer store.Close()
	if _, err = store.Read("config.bin"); !errors.Is(err, os.ErrNotExist) {
		return errors.New("existing protected configuration must be recovered and uninstalled before provisioning another source")
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return errors.New("configuration encoding failed")
	}
	defer clear(encoded)
	return store.Write("config.bin", encoded)
}
func loadEnterprise(store *endpointsecure.Store) (enterpriseConfig, error) {
	var cfg enterpriseConfig
	raw, err := store.Read("config.bin")
	if err != nil {
		return cfg, err
	}
	defer clear(raw)
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&cfg) != nil {
		return cfg, errors.New("invalid protected enterprise configuration")
	}
	return cfg, nil
}
func (cfg enterpriseConfig) client() (*endpointClient, error) {
	return newEndpointClientValues(cfg.PlatformOrigin, false, cfg.SourceToken, cfg.KeyHex, cfg.Scope, cfg.DeviceID, "")
}
func (cfg enterpriseConfig) targets(c *endpointClient) ([]native.Target, error) {
	types := map[string][]string{}
	if cfg.USBWriteProtect {
		types[native.USBWrite] = []string{"machine"}
	}
	if cfg.RDPClipboardRedirection {
		types[native.RDPClipboard] = []string{"machine"}
	}
	if len(cfg.PrintQueues) > 0 {
		types[native.PrintPause] = cfg.PrintQueues
	}
	var targets []native.Target
	for executor, names := range types {
		refs := []string{}
		for _, name := range names {
			refs = append(refs, c.ref("native-target", executor+"\x00"+name))
		}
		sort.Strings(refs)
		encoded, _ := json.Marshal(struct {
			Executor string
			Device   string
			Targets  []string
		}{executor, c.device, refs})
		digest := c.ref("native-config", string(encoded))
		for _, name := range names {
			target := native.Target{Executor: executor, TargetRef: c.ref("native-target", executor+"\x00"+name), ConfigDigest: digest}
			if executor == native.PrintPause {
				target.PrinterName = name
			}
			targets = append(targets, target)
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].TargetRef < targets[j].TargetRef })
	if len(targets) == 0 || len(targets) > 22 {
		return nil, errors.New("invalid enterprise target set")
	}
	return targets, nil
}
