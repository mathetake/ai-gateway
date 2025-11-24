package main

import (
	"fmt"
	"strconv"
	"unsafe"

	"github.com/envoyproxy/ai-gateway/internal/dynamic_module/sdk"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

type (
	// upstreamFilterConfig implements [sdk.HTTPFilterConfig].
	upstreamFilterConfig struct{}
	// upstreamFilter implements [sdk.HTTPFilter].
	upstreamFilter struct {
		rf *routerFilter
	}
)

// NewFilter implements [sdk.HTTPFilterConfig].
func (f *upstreamFilterConfig) NewFilter() sdk.HTTPFilter {
	return &upstreamFilter{}
}

// RequestHeaders implements [sdk.HTTPFilter].
func (f *upstreamFilter) RequestHeaders(e sdk.EnvoyHTTPFilter, _ bool) sdk.RequestHeadersStatus {
	rfPtrStr, ok := e.GetDynamicMetadataString(internalapi.AIGatewayFilterMetadataNamespace,
		routerFilterPointerDynamicMetadataKey)
	if !ok {
		e.SendLocalReply(500, nil, []byte("router filter pointer not found in dynamic metadata"))
		return sdk.RequestHeadersStatusStopIteration
	}
	// Parse %d string to uintptr
	rfPtr, err := strconv.ParseUint(rfPtrStr, 10, 64)
	if err != nil {
		e.SendLocalReply(500, nil, []byte(fmt.Sprintf("invalid router filter pointer: %v", err)))
		return sdk.RequestHeadersStatusStopIteration
	}
	f.rf = (*routerFilter)(unsafe.Pointer(uintptr(rfPtr)))
	return sdk.RequestHeadersStatusContinue
}

// RequestBody implements [sdk.HTTPFilter].
func (f *upstreamFilter) RequestBody(sdk.EnvoyHTTPFilter, bool) sdk.RequestBodyStatus {
	return sdk.RequestBodyStatusContinue
}

// ResponseHeaders implements [sdk.HTTPFilter].
func (f *upstreamFilter) ResponseHeaders(e sdk.EnvoyHTTPFilter, _ bool) sdk.ResponseHeadersStatus {
	return sdk.ResponseHeadersStatusContinue
}

// ResponseBody implements [sdk.HTTPFilter].
func (f *upstreamFilter) ResponseBody(sdk.EnvoyHTTPFilter, bool) sdk.ResponseBodyStatus {
	return sdk.ResponseBodyStatusContinue
}
