package runtimesecurity

import (
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// ValueFingerprint is computed only at a customer-authorized source. It is an
// exact-match token, never a sample value or a credential.
type ValueFingerprint struct {
	KeyID    string `json:"keyId"`
	Scope    string `json:"scope"`
	Version  string `json:"version"`
	Digest   string `json:"digest"`
	Locator  string `json:"locator"`
	Category string `json:"category"`
}

// RuntimeContext contains explicit source observations/declarations. Countries
// are customer-provided labels; no IP geolocation or legal inference is made.
type RuntimeContext struct {
	ClientCountry      string `json:"clientCountry,omitempty"`
	DestinationIP      string `json:"destinationIp,omitempty"`
	DestinationDomain  string `json:"destinationDomain,omitempty"`
	DestinationCountry string `json:"destinationCountry,omitempty"`
	Recipient          string `json:"recipient,omitempty"`
	Purpose            string `json:"purpose,omitempty"`
	Volume             *int64 `json:"volume,omitempty"`
	VolumeUnit         string `json:"volumeUnit,omitempty"`
}

var fingerprintID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,80}$`)
var countryCode = regexp.MustCompile(`^[A-Z]{2}$`)
var safeDomain = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)

func normalizeRuntimeEvidence(o *Observation) error {
	if o.ClientIP != "" {
		ip, e := netip.ParseAddr(o.ClientIP)
		if e != nil || ip.Zone() != "" || ip.IsUnspecified() {
			return ErrInvalidObservation
		}
		o.ClientIP = ip.Unmap().String()
	}
	if !slices.Contains([]string{"", "unknown", "http", "https"}, o.Transport) || !slices.Contains([]string{"", "declared", "proxy_upstream", "source_connection"}, o.TransportHop) {
		return ErrInvalidObservation
	}
	if o.Context != nil {
		c := *o.Context
		for _, value := range []string{c.ClientCountry, c.DestinationCountry} {
			if value != "" && !countryCode.MatchString(value) {
				return ErrInvalidObservation
			}
		}
		if c.DestinationIP != "" {
			ip, e := netip.ParseAddr(c.DestinationIP)
			if e != nil || ip.Zone() != "" || ip.IsUnspecified() {
				return ErrInvalidObservation
			}
			c.DestinationIP = ip.Unmap().String()
		}
		c.DestinationDomain = strings.ToLower(c.DestinationDomain)
		if c.DestinationDomain != "" && (!safeDomain.MatchString(c.DestinationDomain) || strings.Contains(c.DestinationDomain, "..")) {
			return ErrInvalidObservation
		}
		for _, value := range []string{c.Recipient, c.Purpose} {
			if len(value) > 160 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
				return ErrInvalidObservation
			}
		}
		if c.Volume != nil {
			if *c.Volume < 0 || *c.Volume > 1000000000000 || !slices.Contains([]string{"bytes", "records"}, c.VolumeUnit) {
				return ErrInvalidObservation
			}
			v := *c.Volume
			c.Volume = &v
		} else if c.VolumeUnit != "" {
			return ErrInvalidObservation
		}
		o.Context = &c
	}
	if len(o.ValueFingerprints) > 32 {
		return ErrInvalidObservation
	}
	fingerprints := append([]ValueFingerprint(nil), o.ValueFingerprints...)
	for _, f := range fingerprints {
		if !fingerprintID.MatchString(f.KeyID) || !fingerprintID.MatchString(f.Scope) || f.Version != "utf8_exact_v1" || !subjectReference.MatchString(f.Digest) {
			return ErrInvalidObservation
		}
		matched := false
		for _, field := range o.DataFields {
			if field.Name == f.Locator && field.Category == f.Category {
				matched = true
				break
			}
		}
		if !matched {
			return ErrInvalidObservation
		}
	}
	slices.SortFunc(fingerprints, func(a, b ValueFingerprint) int {
		return strings.Compare(a.KeyID+"\x00"+a.Scope+"\x00"+a.Digest+"\x00"+a.Locator+"\x00"+a.Category, b.KeyID+"\x00"+b.Scope+"\x00"+b.Digest+"\x00"+b.Locator+"\x00"+b.Category)
	})
	o.ValueFingerprints = slices.Compact(fingerprints)
	return nil
}
