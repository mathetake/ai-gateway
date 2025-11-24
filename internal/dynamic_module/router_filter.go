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
		fc                     *routerFilterConfig
		endpoint               endpoint
		originalRequestBody    interface{}
		originalRequestBodyRaw []byte
	}

	requestBodyParserFn func(body []byte) (parsed interface{}, modelName string, err error)
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
	return &routerFilter{fc: f}
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
	var parserFn requestBodyParserFn
	switch f.endpoint {
	case chatCompletionsEndpoint:
		parserFn = chatCompletionsBodyParser
	case completionsEndpoint:
		parserFn = completionsBodyParser
	case embeddingsEndpoint:
		parserFn = embeddingsBodyParser
	case imagesGenerationsEndpoint:
		parserFn = imagesGenerationsBodyParser
	case rerankEndpoint:
		parserFn = rerankBodyParser
	case messagesEndpoint:
		parserFn = messagesBodyParser
	default:
		e.SendLocalReply(500, nil, []byte("BUG: unsupported endpoint at body parsing: "+fmt.Sprintf("%d", f.endpoint)))
	}
	parsed, modelName, err := parserFn(raw)
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
	body, _ := json.Marshal(f.fc.models)
	e.SendLocalReply(200, [][2]string{
		{"content-type", "application/json"},
	}, body)
	return sdk.RequestHeadersStatusStopIteration
}

func chatCompletionsBodyParser(body []byte) (interface{}, string, error) {
	var req openai.ChatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal body: %w", err)
	}
	return req, req.Model, nil
}

func completionsBodyParser(body []byte) (interface{}, string, error) {
	var req openai.CompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal body: %w", err)
	}
	return req, req.Model, nil
}

func embeddingsBodyParser(body []byte) (interface{}, string, error) {
	var req openai.EmbeddingRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal body: %w", err)
	}
	return req, req.Model, nil
}

func imagesGenerationsBodyParser(body []byte) (interface{}, string, error) {
	var req openaisdk.ImageGenerateParams
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal body: %w", err)
	}
	return req, req.Model, nil
}

func rerankBodyParser(body []byte) (interface{}, string, error) {
	var req cohereschema.RerankV2Request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal body: %w", err)
	}
	return req, req.Model, nil
}

func messagesBodyParser(body []byte) (interface{}, string, error) {
	var anthropicReq anthropic.MessagesRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal body: %w", err)
	}
	return anthropicReq, anthropicReq.GetModel(), nil
}
