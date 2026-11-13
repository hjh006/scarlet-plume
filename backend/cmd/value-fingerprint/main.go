// value-fingerprint reads one exact UTF-8 value from stdin. It never prints it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/hjh006/scarlet-plume/backend/internal/valuefingerprint"
	"io"
	"os"
)

func main() {
	keyID := flag.String("key-id", "", "local key identifier")
	scope := flag.String("scope", "", "authorized workspace data domain")
	flag.Parse()
	raw, e := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	if e != nil || len(raw) > 4096 {
		fmt.Fprintln(os.Stderr, "bounded UTF-8 input required")
		os.Exit(1)
	}
	defer clear(raw)
	token, e := valuefingerprint.Create(os.Getenv("SCARLET_VALUE_KEY_HEX"), *keyID, *scope, string(raw))
	if e != nil {
		fmt.Fprintln(os.Stderr, "invalid local key, scope or bounded UTF-8 input")
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(token)
}
