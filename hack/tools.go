//go:build tools

// Package tools pins build-time dependencies (controller-gen) in go.mod so
// `go run sigs.k8s.io/controller-tools/cmd/controller-gen` works from a
// clean checkout.
package tools

import (
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
)
