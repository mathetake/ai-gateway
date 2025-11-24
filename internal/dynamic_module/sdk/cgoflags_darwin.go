//go:build darwin && cgo

package sdk

// #cgo LDFLAGS: -Wl,-undefined,dynamic_lookup
import "C"
