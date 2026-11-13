//go:build !windows

package main

import (
	"github.com/hjh006/scarlet-plume/backend/internal/endpointnative"
	"os"
)

func lockClipboardSession(*os.Root) (func(), error) { return nil, endpointnative.ErrUnsupported }
