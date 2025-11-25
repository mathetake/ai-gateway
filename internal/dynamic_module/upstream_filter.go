package main

import (
	"context"
	"fmt"
	"strconv"
	"unsafe"

	"github.com/envoyproxy/ai-gateway/internal/dynamic_module/sdk"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/headermutator"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

type (
	// upstreamFilterConfig implements [sdk.HTTPFilterConfig].
	upstreamFilterConfig struct{}
	// upstreamFilter implements [sdk.HTTPFilter].
	upstreamFilter struct {
		rf         *routerFilter
		backend    *filterapi.RuntimeBackend
		reqHeaders map[string]string
		onRetry    bool
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
	rfPtr, err := strconv.ParseInt(rfPtrStr, 10, 64)
	if err != nil {
		e.SendLocalReply(500, nil, []byte(fmt.Sprintf("invalid router filter pointer: %v", err)))
		return sdk.RequestHeadersStatusStopIteration
	}
	f.rf = (*routerFilter)(unsafe.Pointer(uintptr(rfPtr)))
	f.rf.attemptCount++
	f.onRetry = f.rf.attemptCount > 1

	backend, ok := e.GetUpstreamHostMetadataString(internalapi.AIGatewayFilterMetadataNamespace, internalapi.InternalMetadataBackendNameKey)
	if !ok {
		e.SendLocalReply(500, nil, []byte("backend name not found in upstream host metadata"))
		return sdk.RequestHeadersStatusStopIteration
	}
	b, ok := f.rf.fc.Backends[backend]
	if !ok {
		e.SendLocalReply(500, nil, []byte(fmt.Sprintf("backend %s not found in filter config", backend)))
		return sdk.RequestHeadersStatusStopIteration
	}

	f.backend = b
	f.reqHeaders = multiValueHeadersToSingleValue(e.GetRequestHeaders())

	// Now mutate the headers based on the backend configuration.
	if hm := b.Backend.HeaderMutation; hm != nil {
		sets, removes := headermutator.NewHeaderMutator(b.Backend.HeaderMutation, f.rf.originalHeaders).Mutate(f.reqHeaders, f.onRetry)
		for _, h := range sets {
			if !e.SetRequestHeader(h.Key(), []byte(h.Value())) {
				e.SendLocalReply(500, nil, []byte(fmt.Sprintf("failed to set header %s", h.Key())))
				return sdk.RequestHeadersStatusStopIteration
			}
			f.reqHeaders[h.Key()] = h.Value()
		}
		for _, key := range removes {
			if !e.SetRequestHeader(key, nil) {
				e.SendLocalReply(500, nil, []byte(fmt.Sprintf("failed to remove header %s", key)))
				return sdk.RequestHeadersStatusStopIteration
			}
			delete(f.reqHeaders, key)
		}
	}
	return sdk.RequestHeadersStatusContinue
}

// RequestBody implements [sdk.HTTPFilter].
func (f *upstreamFilter) RequestBody(e sdk.EnvoyHTTPFilter, endOfStream bool) sdk.RequestBodyStatus {
	if !endOfStream {
		// TODO: ideally, we should not buffer the entire body for the passthrough case.
		return sdk.RequestBodyStatusStopIterationAndBuffer
	}

	b := f.backend

	// TODO: endpoint specific logic such as translation.
	var newBody []byte
	_ = newBody

	if bm := b.Backend.BodyMutation; bm != nil {
		// TODO: body mutation if needed.
		_ = bm
	}

	// Next is to do the upstream auth if needed.
	if b.Handler != nil {
		var originalOrNewBody []byte
		if newBody != nil {
			originalOrNewBody = newBody
		}

		authHeaders, err := b.Handler.Do(context.Background(), f.reqHeaders, originalOrNewBody)
		if err != nil {
			e.SendLocalReply(500, nil, []byte(fmt.Sprintf("failed to do backend auth: %v", err)))
			return sdk.RequestBodyStatusStopIterationAndBuffer
		}
		for _, h := range authHeaders {
			if !e.SetRequestHeader(h.Key(), []byte(h.Value())) {
				e.SendLocalReply(500, nil, []byte(fmt.Sprintf("failed to set auth header %s", h.Key())))
				return sdk.RequestBodyStatusStopIterationAndBuffer
			}
		}
	}
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
