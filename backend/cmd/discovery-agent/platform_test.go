package main

import "testing"

func TestPlatformLocalDockerIsFixedService(t *testing.T) {
	for _, endpoint := range []string{"http://app:8080", "http://app:8080/"} {
		if e := validatePlatform(endpoint, false, true); e != nil {
			t.Fatal(e)
		}
	}
	for _, endpoint := range []string{"http://10.0.0.4:8080", "http://example.com:8080", "https://example.com", "http://app:8081", "http://app.evil:8080", "http://user@app:8080", "http://app:8080/api", "http://127.0.0.1:8080", "http://app:8080?x=1"} {
		if e := validatePlatform(endpoint, true, true); e == nil {
			t.Fatalf("Docker mode accepted %s", endpoint)
		}
	}
	if validatePlatform("http://app:8080", true, false) == nil {
		t.Fatal("loopback mode expanded to Docker hostname")
	}
	if validatePlatform("http://127.0.0.1:8890", true, false) != nil {
		t.Fatal("literal loopback development rejected")
	}
	if validatePlatform("https://example.com", false, false) != nil {
		t.Fatal("production HTTPS rejected")
	}
}
