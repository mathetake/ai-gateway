package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unsafe"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	cohereschema "github.com/envoyproxy/ai-gateway/internal/apischema/cohere"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/dynamic_module/sdk"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	openaisdk "github.com/openai/openai-go/v2"
)

const routerFilterPointerDynamicMetadataKey = "router_filter_pointer"

type (
	// routerFilterConfig implements [sdk.HTTPFilterConfig].
	//
	// This is mostly for debugging purposes, it does not do anything except
	// setting a response header with the version of the dynamic module.
	routerFilterConfig struct {
		fcr    *filterConfigReceiverImpl
		models openai.ModelList
	}
	// routerFilter implements [sdk.HTTPFilter].
	routerFilter struct {
		fc                     *filterapi.RuntimeConfig
		models                 *openai.ModelList
		endpoint               endpoint
		originalHeaders        map[string]string
		originalRequestBody    interface{}
		originalRequestBodyRaw []byte
		attemptCount           int
	}
)

func newRouterFilterConfig(fcr *filterConfigReceiverImpl) *routerFilterConfig {
	config := fcr.fc
	models := openai.ModelList{
		Object: "list",
		Data:   make([]openai.Model, 0, len(config.DeclaredModels)),
	}
	for _, m := range config.DeclaredModels {
		models.Data = append(models.Data, openai.Model{
			ID:      m.Name,
			Object:  "model",
			OwnedBy: m.OwnedBy,
			Created: openai.JSONUNIXTime(m.CreatedAt),
		})
	}
	return &routerFilterConfig{fcr: fcr, models: models}
}

// NewFilter implements [sdk.HTTPFilterConfig].
func (f *routerFilterConfig) NewFilter() sdk.HTTPFilter {
	return &routerFilter{fc: f.fcr.fc, models: &f.models}
}

// RequestHeaders implements [sdk.HTTPFilter].
func (f *routerFilter) RequestHeaders(e sdk.EnvoyHTTPFilter, _ bool) sdk.RequestHeadersStatus {
	p, _ := e.GetRequestHeader(":path") // The :path pseudo header is always present.
	// Strip query parameters for processor lookup.
	if queryIndex := strings.Index(p, "?"); queryIndex != -1 {
		p = p[:queryIndex]
	}
	// TODO: prefix config.
	switch p {
	case "/v1/chat/completions":
		f.endpoint = chatCompletionsEndpoint
		return sdk.RequestHeadersStatusContinue
	case "/v1/completions":
		f.endpoint = completionsEndpoint
		return sdk.RequestHeadersStatusContinue
	case "/v1/embeddings":
		f.endpoint = embeddingsEndpoint
		return sdk.RequestHeadersStatusContinue
	case "/v1/images/generations":
		f.endpoint = imagesGenerationsEndpoint
		return sdk.RequestHeadersStatusContinue
	case "/cohere/v2/rerank":
		f.endpoint = rerankEndpoint
		return sdk.RequestHeadersStatusContinue
	case "/anthropic/v1/messages":
		f.endpoint = messagesEndpoint
		return sdk.RequestHeadersStatusContinue
	case "/v1/models":
		return f.handleModelsEndpoint(e)
	default:
		e.SendLocalReply(404, nil, []byte(fmt.Sprintf("unsupported path: %s", p)))
		return sdk.RequestHeadersStatusStopIteration
	}
}

// RequestBody implements [sdk.HTTPFilter].
func (f *routerFilter) RequestBody(e sdk.EnvoyHTTPFilter, endOfStream bool) sdk.RequestBodyStatus {
	if !endOfStream {
		return sdk.RequestBodyStatusStopIterationAndBuffer
	}
	b, ok := e.GetRequestBody()
	if !ok {
		e.SendLocalReply(400, nil, []byte("failed to read request body"))
		return sdk.RequestBodyStatusStopIterationAndBuffer
	}
	raw, err := io.ReadAll(b)
	if err != nil {
		e.SendLocalReply(400, nil, []byte("failed to read request body: "+err.Error()))
		return sdk.RequestBodyStatusStopIterationAndBuffer
	}
	f.originalRequestBodyRaw = raw
	var parsed any
	var modelName string
	switch f.endpoint {
	case chatCompletionsEndpoint:
		parsed, modelName, err = parseBodyWithModel(raw, func(req *openai.ChatCompletionRequest) string { return req.Model })
	case completionsEndpoint:
		parsed, modelName, err = parseBodyWithModel(raw, func(req *openai.CompletionRequest) string { return req.Model })
	case embeddingsEndpoint:
		parsed, modelName, err = parseBodyWithModel(raw, func(req *openai.EmbeddingRequest) string { return req.Model })
	case imagesGenerationsEndpoint:
		parsed, modelName, err = parseBodyWithModel(raw, func(req *openaisdk.ImageGenerateParams) string { return req.Model })
	case rerankEndpoint:
		parsed, modelName, err = parseBodyWithModel(raw, func(req *cohereschema.RerankV2Request) string { return req.Model })
	case messagesEndpoint:
		parsed, modelName, err = parseBodyWithModel(raw, func(req *anthropic.MessagesRequest) string { return req.GetModel() })
	default:
		e.SendLocalReply(500, nil, []byte("BUG: unsupported endpoint at body parsing: "+fmt.Sprintf("%d", f.endpoint)))
	}
	if err != nil {
		e.SendLocalReply(400, nil, []byte("failed to parse request body: "+err.Error()))
		return sdk.RequestBodyStatusStopIterationAndBuffer
	}
	f.originalRequestBody = parsed
	if !e.SetRequestHeader(internalapi.ModelNameHeaderKeyDefault, []byte(modelName)) {
		e.SendLocalReply(500, nil, []byte("failed to set model name header"))
		return sdk.RequestBodyStatusStopIterationAndBuffer
	}
	// Store the pointer to the filter in dynamic metadata for later retrieval in the upstream filter.
	e.SetDynamicMetadataString(internalapi.AIGatewayFilterMetadataNamespace, routerFilterPointerDynamicMetadataKey,
		fmt.Sprintf("%d", uintptr(unsafe.Pointer(f))))

	f.originalHeaders = multiValueHeadersToSingleValue(e.GetRequestHeaders())
	return sdk.RequestBodyStatusContinue
}

// ResponseHeaders implements [sdk.HTTPFilter].
func (f *routerFilter) ResponseHeaders(sdk.EnvoyHTTPFilter, bool) sdk.ResponseHeadersStatus {
	return sdk.ResponseHeadersStatusContinue
}

// ResponseBody implements [sdk.HTTPFilter].
func (f *routerFilter) ResponseBody(sdk.EnvoyHTTPFilter, bool) sdk.ResponseBodyStatus {
	return sdk.ResponseBodyStatusContinue
}

// handleModelsEndpoint handles the /v1/models endpoint by returning the list of declared models in the filter configuration.
//
// This is called on request headers phase.
func (f *routerFilter) handleModelsEndpoint(e sdk.EnvoyHTTPFilter) sdk.RequestHeadersStatus {
	body, _ := json.Marshal(f.models)
	e.SendLocalReply(200, [][2]string{
		{"content-type", "application/json"},
	}, body)
	return sdk.RequestHeadersStatusStopIteration
}

func parseBodyWithModel[T any](body []byte, modelExtractFn func(req *T) string) (interface{}, string, error) {
	var req T
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal body: %w", err)
	}
	return req, modelExtractFn(&req), nil
}

// multiValueHeadersToSingleValue converts a map of headers with multiple values to a map of headers with single values by taking the first value for each header.
//
// TODO: this is purely for feature parity with the old filter where we ignore the case of multiple header values.
func multiValueHeadersToSingleValue(headers map[string][]string) map[string]string {
	singleValueHeaders := make(map[string]string, len(headers))
	for k, v := range headers {
		singleValueHeaders[k] = v[0]
	}
	return singleValueHeaders
}
