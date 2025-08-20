// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3http "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
	"github.com/tiktoken-go/tokenizer"
	"google.golang.org/protobuf/types/known/structpb"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/extproc/translator"
	"github.com/envoyproxy/ai-gateway/internal/llmcostcel"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	tracing "github.com/envoyproxy/ai-gateway/internal/tracing/api"
)

func TestChatCompletion_Schema(t *testing.T) {
	t.Run("supported openai / on route", func(t *testing.T) {
		cfg := &processorConfig{}
		routeFilter, err := ChatCompletionProcessorFactory(nil)(cfg, nil, slog.Default(), tracing.NoopTracing{}, false)
		require.NoError(t, err)
		require.NotNil(t, routeFilter)
		require.IsType(t, &chatCompletionProcessorRouterFilter{}, routeFilter)
	})
	t.Run("supported openai / on upstream", func(t *testing.T) {
		cfg := &processorConfig{}
		routeFilter, err := ChatCompletionProcessorFactory(nil)(cfg, nil, slog.Default(), tracing.NoopTracing{}, true)
		require.NoError(t, err)
		require.NotNil(t, routeFilter)
		require.IsType(t, &chatCompletionProcessorUpstreamFilter{}, routeFilter)
	})
}

func Test_chatCompletionProcessorUpstreamFilter_SelectTranslator(t *testing.T) {
	c := &chatCompletionProcessorUpstreamFilter{}
	t.Run("unsupported", func(t *testing.T) {
		err := c.selectTranslator(filterapi.VersionedAPISchema{Name: "Bar", Version: "v123"})
		require.ErrorContains(t, err, "unsupported API schema: backend={Bar v123}")
	})
	t.Run("supported openai", func(t *testing.T) {
		err := c.selectTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI})
		require.NoError(t, err)
		require.NotNil(t, c.translator)
	})
	t.Run("supported aws bedrock", func(t *testing.T) {
		err := c.selectTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaAWSBedrock})
		require.NoError(t, err)
		require.NotNil(t, c.translator)
	})
	t.Run("supported azure openai", func(t *testing.T) {
		err := c.selectTranslator(filterapi.VersionedAPISchema{Name: filterapi.APISchemaAzureOpenAI})
		require.NoError(t, err)
		require.NotNil(t, c.translator)
	})
}

type mockTracer struct {
	tracing.NoopChatCompletionTracer
	startSpanCalled bool
	returnedSpan    tracing.ChatCompletionSpan
}

func (m *mockTracer) StartSpanAndInjectHeaders(_ context.Context, _ map[string]string, headerMutation *extprocv3.HeaderMutation, _ *openai.ChatCompletionRequest, _ []byte) tracing.ChatCompletionSpan {
	m.startSpanCalled = true
	headerMutation.SetHeaders = append(headerMutation.SetHeaders, &corev3.HeaderValueOption{
		Header: &corev3.HeaderValue{
			Key:   "tracing-header",
			Value: "1",
		},
	})
	if m.returnedSpan != nil {
		return m.returnedSpan
	}
	return nil
}

func Test_chatCompletionProcessorRouterFilter_ProcessRequestBody(t *testing.T) {
	t.Run("body parser error", func(t *testing.T) {
		p := &chatCompletionProcessorRouterFilter{
			tracer: tracing.NoopChatCompletionTracer{},
		}
		_, err := p.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: []byte("nonjson")})
		require.ErrorContains(t, err, "invalid character 'o' in literal null")
	})

	t.Run("ok", func(t *testing.T) {
		headers := map[string]string{":path": "/foo"}
		const modelKey = "x-ai-gateway-model-key"
		p := &chatCompletionProcessorRouterFilter{
			config:         &processorConfig{modelNameHeaderKey: modelKey},
			requestHeaders: headers,
			logger:         slog.Default(),
			tracer:         tracing.NoopChatCompletionTracer{},
		}
		resp, err := p.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: bodyFromModel(t, "some-model", false, nil)})
		require.NoError(t, err)
		require.NotNil(t, resp)
		re, ok := resp.Response.(*extprocv3.ProcessingResponse_RequestBody)
		require.True(t, ok)
		require.NotNil(t, re)
		require.NotNil(t, re.RequestBody)
		setHeaders := re.RequestBody.GetResponse().GetHeaderMutation().SetHeaders
		require.Len(t, setHeaders, 2)
		require.Equal(t, modelKey, setHeaders[0].Header.Key)
		require.Equal(t, "some-model", string(setHeaders[0].Header.RawValue))
		require.Equal(t, "x-ai-eg-original-path", setHeaders[1].Header.Key)
		require.Equal(t, "/foo", string(setHeaders[1].Header.RawValue))
	})

	t.Run("span creation", func(t *testing.T) {
		headers := map[string]string{":path": "/v1/chat/completions"}
		const modelKey = "x-ai-gateway-model-key"
		span := &testotel.MockSpan{}
		mockTracerInstance := &mockTracer{returnedSpan: span}

		p := &chatCompletionProcessorRouterFilter{
			config:         &processorConfig{modelNameHeaderKey: modelKey},
			requestHeaders: headers,
			logger:         slog.Default(),
			tracer:         mockTracerInstance,
		}

		// Test with streaming request.
		resp, err := p.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: bodyFromModel(t, "test-model", true, nil)})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.True(t, mockTracerInstance.startSpanCalled)
		require.Equal(t, span, p.span)
		require.True(t, p.originalRequestBody.Stream)

		// Verify headers are injected.
		re, ok := resp.Response.(*extprocv3.ProcessingResponse_RequestBody)
		require.True(t, ok)
		headerMutation := re.RequestBody.GetResponse().GetHeaderMutation()
		require.Contains(t, headerMutation.SetHeaders, &corev3.HeaderValueOption{
			Header: &corev3.HeaderValue{
				Key:   "tracing-header",
				Value: "1",
			},
		})
	})

	t.Run("ok_stream_without_include_usage", func(t *testing.T) {
		for _, opt := range []*openai.StreamOptions{nil, {IncludeUsage: false}} {
			headers := map[string]string{":path": "/foo"}
			const modelKey = "x-ai-gateway-model-key"
			p := &chatCompletionProcessorRouterFilter{
				config: &processorConfig{
					modelNameHeaderKey: modelKey,
					// Ensure that the stream_options.include_usage be forced to true.
					requestCosts: []processorConfigRequestCost{{}},
				},
				requestHeaders: headers,
				logger:         slog.Default(),
				tracer:         tracing.NoopChatCompletionTracer{},
			}
			resp, err := p.ProcessRequestBody(t.Context(), &extprocv3.HttpBody{Body: bodyFromModel(t, "some-model", true, opt)})
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.NotNil(t, p.originalRequestBody.StreamOptions)
			require.True(t, p.forcedStreamOptionIncludeUsage)
			require.True(t, p.originalRequestBody.StreamOptions.IncludeUsage)
			require.Contains(t, string(p.originalRequestBodyRaw), `"stream_options":{"include_usage":true}`)
		}
	})
}

func Test_chatCompletionProcessorUpstreamFilter_ProcessResponseHeaders(t *testing.T) {
	t.Run("error translation", func(t *testing.T) {
		mm := &mockChatCompletionMetrics{}
		mt := &mockTranslator{t: t, expHeaders: make(map[string]string)}
		p := &chatCompletionProcessorUpstreamFilter{
			translator: mt,
			metrics:    mm,
		}
		mt.retErr = errors.New("test error")
		_, err := p.ProcessResponseHeaders(t.Context(), nil)
		require.ErrorContains(t, err, "test error")
		mm.RequireRequestFailure(t)
	})
	t.Run("ok/non-streaming", func(t *testing.T) {
		inHeaders := &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{{Key: "foo", Value: "bar"}, {Key: "dog", RawValue: []byte("cat")}},
		}
		expHeaders := map[string]string{"foo": "bar", "dog": "cat"}
		mm := &mockChatCompletionMetrics{}
		mt := &mockTranslator{t: t, expHeaders: expHeaders}
		p := &chatCompletionProcessorUpstreamFilter{
			translator: mt,
			metrics:    mm,
		}
		res, err := p.ProcessResponseHeaders(t.Context(), inHeaders)
		require.NoError(t, err)
		commonRes := res.Response.(*extprocv3.ProcessingResponse_ResponseHeaders).ResponseHeaders.Response
		require.Equal(t, mt.retHeaderMutation, commonRes.HeaderMutation)
		mm.RequireRequestNotCompleted(t)
		require.Nil(t, res.ModeOverride)
	})
	t.Run("ok/streaming", func(t *testing.T) {
		inHeaders := &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{{Key: ":status", Value: "200"}, {Key: "dog", RawValue: []byte("cat")}},
		}
		expHeaders := map[string]string{":status": "200", "dog": "cat"}
		mm := &mockChatCompletionMetrics{}
		mt := &mockTranslator{t: t, expHeaders: expHeaders}
		p := &chatCompletionProcessorUpstreamFilter{translator: mt, metrics: mm, stream: true}
		res, err := p.ProcessResponseHeaders(t.Context(), inHeaders)
		require.NoError(t, err)
		commonRes := res.Response.(*extprocv3.ProcessingResponse_ResponseHeaders).ResponseHeaders.Response
		require.Equal(t, mt.retHeaderMutation, commonRes.HeaderMutation)
		require.Equal(t, &extprocv3http.ProcessingMode{ResponseBodyMode: extprocv3http.ProcessingMode_STREAMED}, res.ModeOverride)
	})
	t.Run("error/streaming", func(t *testing.T) {
		inHeaders := &corev3.HeaderMap{
			Headers: []*corev3.HeaderValue{{Key: ":status", Value: "500"}, {Key: "dog", RawValue: []byte("cat")}},
		}
		expHeaders := map[string]string{":status": "500", "dog": "cat"}
		mm := &mockChatCompletionMetrics{}
		mt := &mockTranslator{t: t, expHeaders: expHeaders}
		p := &chatCompletionProcessorUpstreamFilter{translator: mt, metrics: mm, stream: true}
		res, err := p.ProcessResponseHeaders(t.Context(), inHeaders)
		require.NoError(t, err)
		commonRes := res.Response.(*extprocv3.ProcessingResponse_ResponseHeaders).ResponseHeaders.Response
		require.Equal(t, mt.retHeaderMutation, commonRes.HeaderMutation)
		require.Nil(t, res.ModeOverride)
	})
}

func Test_chatCompletionProcessorUpstreamFilter_ProcessResponseBody(t *testing.T) {
	t.Run("error translation", func(t *testing.T) {
		mm := &mockChatCompletionMetrics{}
		mt := &mockTranslator{t: t}
		p := &chatCompletionProcessorUpstreamFilter{
			translator: mt,
			metrics:    mm,
		}
		mt.retErr = errors.New("test error")
		_, err := p.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{})
		require.ErrorContains(t, err, "test error")
		mm.RequireRequestFailure(t)
		mm.RequireTokensRecorded(t, 0)
	})
	t.Run("ok", func(t *testing.T) {
		inBody := &extprocv3.HttpBody{Body: []byte("some-body"), EndOfStream: true}
		expBodyMut := &extprocv3.BodyMutation{}
		expHeadMut := &extprocv3.HeaderMutation{}
		mm := &mockChatCompletionMetrics{}
		mt := &mockTranslator{
			t: t, expResponseBody: inBody,
			retBodyMutation: expBodyMut, retHeaderMutation: expHeadMut,
			retUsedToken: translator.LLMTokenUsage{OutputTokens: 123, InputTokens: 1},
		}

		celProgInt, err := llmcostcel.NewProgram("54321")
		require.NoError(t, err)
		celProgUint, err := llmcostcel.NewProgram("uint(9999)")
		require.NoError(t, err)
		p := &chatCompletionProcessorUpstreamFilter{
			translator: mt,
			logger:     slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
			metrics:    mm,
			stream:     true,
			config: &processorConfig{
				metadataNamespace: "ai_gateway_llm_ns",
				requestCosts: []processorConfigRequestCost{
					{LLMRequestCost: &filterapi.LLMRequestCost{Type: filterapi.LLMRequestCostTypeOutputToken, MetadataKey: "output_token_usage"}},
					{LLMRequestCost: &filterapi.LLMRequestCost{Type: filterapi.LLMRequestCostTypeInputToken, MetadataKey: "input_token_usage"}},
					{
						celProg:        celProgInt,
						LLMRequestCost: &filterapi.LLMRequestCost{Type: filterapi.LLMRequestCostTypeCEL, MetadataKey: "cel_int"},
					},
					{
						celProg:        celProgUint,
						LLMRequestCost: &filterapi.LLMRequestCost{Type: filterapi.LLMRequestCostTypeCEL, MetadataKey: "cel_uint"},
					},
				},
			},
			responseHeaders:   map[string]string{":status": "200"},
			backendName:       "some_backend",
			modelNameOverride: "ai_gateway_llm",
		}
		res, err := p.ProcessResponseBody(t.Context(), inBody)
		require.NoError(t, err)
		commonRes := res.Response.(*extprocv3.ProcessingResponse_ResponseBody).ResponseBody.Response
		require.Equal(t, expBodyMut, commonRes.BodyMutation)
		require.Equal(t, expHeadMut, commonRes.HeaderMutation)
		mm.RequireRequestSuccess(t)
		mm.RequireTokensRecorded(t, 1)

		md := res.DynamicMetadata
		require.NotNil(t, md)
		require.Equal(t, float64(123), md.Fields["ai_gateway_llm_ns"].
			GetStructValue().Fields["output_token_usage"].GetNumberValue())
		require.Equal(t, float64(1), md.Fields["ai_gateway_llm_ns"].
			GetStructValue().Fields["input_token_usage"].GetNumberValue())
		require.Equal(t, float64(54321), md.Fields["ai_gateway_llm_ns"].
			GetStructValue().Fields["cel_int"].GetNumberValue())
		require.Equal(t, float64(9999), md.Fields["ai_gateway_llm_ns"].
			GetStructValue().Fields["cel_uint"].GetNumberValue())
		require.Equal(t, "ai_gateway_llm", md.Fields["ai_gateway_llm_ns"].GetStructValue().Fields["model_name_override"].GetStringValue())
		require.Equal(t, "some_backend", md.Fields["ai_gateway_llm_ns"].GetStructValue().Fields["backend_name"].GetStringValue())
	})
}

func bodyFromModel(t *testing.T, model string, stream bool, streamOptions *openai.StreamOptions) []byte {
	openAIReq := &openai.ChatCompletionRequest{}
	openAIReq.Model = model
	openAIReq.Stream = stream
	openAIReq.StreamOptions = streamOptions
	bytes, err := json.Marshal(openAIReq)
	require.NoError(t, err)
	return bytes
}

func Test_chatCompletionProcessorUpstreamFilter_SetBackend(t *testing.T) {
	headers := map[string]string{":path": "/foo"}
	mm := &mockChatCompletionMetrics{}
	p := &chatCompletionProcessorUpstreamFilter{
		config: &processorConfig{
			requestCosts: []processorConfigRequestCost{
				{LLMRequestCost: &filterapi.LLMRequestCost{Type: filterapi.LLMRequestCostTypeOutputToken, MetadataKey: "output_token_usage", CEL: "15"}},
			},
		},
		requestHeaders: headers,
		logger:         slog.Default(),
		metrics:        mm,
	}
	err := p.SetBackend(t.Context(), &filterapi.Backend{
		Name:              "some-backend",
		Schema:            filterapi.VersionedAPISchema{Name: "some-schema", Version: "v10.0"},
		ModelNameOverride: "ai_gateway_llm",
	}, nil, &chatCompletionProcessorRouterFilter{})
	require.ErrorContains(t, err, "unsupported API schema: backend={some-schema v10.0}")
	mm.RequireRequestFailure(t)
	mm.RequireTokensRecorded(t, 0)
	mm.RequireSelectedBackend(t, "some-backend")
	require.False(t, p.stream) // On error, stream should be false regardless of the input.
}

func Test_chatCompletionProcessorUpstreamFilter_ProcessRequestHeaders(t *testing.T) {
	const modelKey = "x-ai-gateway-model-key"
	for _, tc := range []struct {
		name                       string
		stream, forcedIncludeUsage bool
	}{
		{name: "non-streaming", stream: false, forcedIncludeUsage: false},
		{name: "streaming", stream: true, forcedIncludeUsage: false},
		{name: "streaming with forced include usage", stream: true, forcedIncludeUsage: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("translator error", func(t *testing.T) {
				headers := map[string]string{":path": "/foo", modelKey: "some-model"}
				someBody := bodyFromModel(t, "some-model", tc.stream, nil)
				var body openai.ChatCompletionRequest
				require.NoError(t, json.Unmarshal(someBody, &body))
				tr := mockTranslator{t: t, retErr: errors.New("test error"), expRequestBody: &body}
				mm := &mockChatCompletionMetrics{}
				p := &chatCompletionProcessorUpstreamFilter{
					config: &processorConfig{
						modelNameHeaderKey: modelKey,
					},
					requestHeaders:         headers,
					logger:                 slog.Default(),
					metrics:                mm,
					translator:             tr,
					originalRequestBodyRaw: someBody,
					originalRequestBody:    &body,
					stream:                 tc.stream,
				}
				_, err := p.ProcessRequestHeaders(t.Context(), nil)
				require.ErrorContains(t, err, "failed to transform request: test error")
				mm.RequireRequestFailure(t)
				mm.RequireTokensRecorded(t, 0)
				mm.RequireSelectedModel(t, "some-model")
			})
			t.Run("ok", func(t *testing.T) {
				someBody := bodyFromModel(t, "some-model", tc.stream, nil)
				headers := map[string]string{":path": "/foo", modelKey: "some-model"}
				headerMut := &extprocv3.HeaderMutation{
					SetHeaders: []*corev3.HeaderValueOption{{Header: &corev3.HeaderValue{Key: "foo", RawValue: []byte("bar")}}},
				}
				bodyMut := &extprocv3.BodyMutation{Mutation: &extprocv3.BodyMutation_Body{Body: []byte("some body")}}

				var expBody openai.ChatCompletionRequest
				require.NoError(t, json.Unmarshal(someBody, &expBody))
				if tc.stream && tc.forcedIncludeUsage {
					expBody.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
				}
				mt := mockTranslator{t: t, expRequestBody: &expBody, retHeaderMutation: headerMut, retBodyMutation: bodyMut, expForceRequestBodyMutation: tc.forcedIncludeUsage}
				mm := &mockChatCompletionMetrics{}
				p := &chatCompletionProcessorUpstreamFilter{
					config:                         &processorConfig{modelNameHeaderKey: modelKey},
					requestHeaders:                 headers,
					logger:                         slog.Default(),
					metrics:                        mm,
					translator:                     mt,
					originalRequestBodyRaw:         someBody,
					originalRequestBody:            &expBody,
					stream:                         tc.stream,
					forcedStreamOptionIncludeUsage: tc.forcedIncludeUsage,
					handler:                        &mockBackendAuthHandler{},
				}
				resp, err := p.ProcessRequestHeaders(t.Context(), nil)
				require.NoError(t, err)
				require.Equal(t, mt, p.translator)
				require.NotNil(t, resp)
				commonRes := resp.Response.(*extprocv3.ProcessingResponse_RequestHeaders).RequestHeaders.Response
				require.Equal(t, headerMut, commonRes.HeaderMutation)
				require.Equal(t, bodyMut, commonRes.BodyMutation)

				mm.RequireRequestNotCompleted(t)
				mm.RequireSelectedModel(t, "some-model")
				require.Equal(t, tc.stream, p.stream)
			})
		})
	}
}

func TestChatCompletion_ParseBody(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		original := openai.ChatCompletionRequest{Model: "llama3.3"}
		bytes, err := json.Marshal(original)
		require.NoError(t, err)

		modelName, rb, err := parseOpenAIChatCompletionBody(&extprocv3.HttpBody{Body: bytes})
		require.NoError(t, err)
		require.Equal(t, "llama3.3", modelName)
		require.NotNil(t, rb)
	})
	t.Run("error", func(t *testing.T) {
		modelName, rb, err := parseOpenAIChatCompletionBody(&extprocv3.HttpBody{})
		require.Error(t, err)
		require.Empty(t, modelName)
		require.Nil(t, rb)
	})
}

func Test_chatCompletionProcessorUpstreamFilter_MergeWithTokenLatencyMetadata(t *testing.T) {
	t.Run("empty metadata", func(t *testing.T) {
		mm := &mockChatCompletionMetrics{}
		mt := &mockTranslator{}
		ns := "ai_gateway_llm_ns"
		p := &chatCompletionProcessorUpstreamFilter{
			translator: mt,
			logger:     slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
			metrics:    mm,
			stream:     true,
			config:     &processorConfig{metadataNamespace: ns},
		}
		metadata := &structpb.Struct{Fields: map[string]*structpb.Value{}}
		p.mergeWithTokenLatencyMetadata(metadata)

		val, ok := metadata.Fields[ns]
		require.True(t, ok)

		inner := val.GetStructValue()
		require.NotNil(t, inner)
		require.Equal(t, 1000.0, inner.Fields["token_latency_ttft"].GetNumberValue())
		require.Equal(t, 500.0, inner.Fields["token_latency_itl"].GetNumberValue())
	})
	t.Run("existing metadata", func(t *testing.T) {
		mm := &mockChatCompletionMetrics{}
		mt := &mockTranslator{}
		ns := "ai_gateway_llm_ns"
		p := &chatCompletionProcessorUpstreamFilter{
			translator: mt,
			logger:     slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
			metrics:    mm,
			stream:     true,
			config:     &processorConfig{metadataNamespace: ns},
		}
		existingInner := &structpb.Struct{Fields: map[string]*structpb.Value{
			"tokenCost":        {Kind: &structpb.Value_NumberValue{NumberValue: float64(200)}},
			"inputTokenUsage":  {Kind: &structpb.Value_NumberValue{NumberValue: float64(300)}},
			"outputTokenUsage": {Kind: &structpb.Value_NumberValue{NumberValue: float64(400)}},
		}}
		metadata := &structpb.Struct{Fields: map[string]*structpb.Value{
			ns: structpb.NewStructValue(existingInner),
		}}
		p.mergeWithTokenLatencyMetadata(metadata)

		val, ok := metadata.Fields[ns]
		require.True(t, ok)
		inner := val.GetStructValue()
		require.NotNil(t, inner)
		require.Equal(t, 1000.0, inner.Fields["token_latency_ttft"].GetNumberValue())
		require.Equal(t, 500.0, inner.Fields["token_latency_itl"].GetNumberValue())
		require.Equal(t, 200.0, inner.Fields["tokenCost"].GetNumberValue())
		require.Equal(t, 300.0, inner.Fields["inputTokenUsage"].GetNumberValue())
		require.Equal(t, 400.0, inner.Fields["outputTokenUsage"].GetNumberValue())
	})
}

func TestChatCompletionsProcessorRouterFilter_ProcessResponseHeaders_ProcessResponseBody(t *testing.T) {
	t.Run("no ok path with passthrough", func(t *testing.T) {
		p := &chatCompletionProcessorRouterFilter{
			span:                nil,
			originalRequestBody: &openai.ChatCompletionRequest{Stream: true},
		}
		_, err := p.ProcessResponseHeaders(t.Context(), nil)
		require.NoError(t, err)
		_, err = p.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{EndOfStream: true})
		require.NoError(t, err)
	})
	t.Run("ok path with upstream filter", func(t *testing.T) {
		p := &chatCompletionProcessorRouterFilter{
			span:                nil,
			originalRequestBody: &openai.ChatCompletionRequest{Stream: true},
			upstreamFilter: &chatCompletionProcessorUpstreamFilter{
				translator: &mockTranslator{t: t, expHeaders: map[string]string{}},
				logger:     slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
				metrics:    &mockChatCompletionMetrics{},
				config:     &processorConfig{metadataNamespace: ""},
			},
		}
		resp, err := p.ProcessResponseHeaders(t.Context(), &corev3.HeaderMap{Headers: []*corev3.HeaderValue{}})
		require.NoError(t, err)
		require.NotNil(t, resp)

		resp, err = p.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{Body: []byte("some body")})
		require.NoError(t, err)
		require.NotNil(t, resp)
		re, ok := resp.Response.(*extprocv3.ProcessingResponse_ResponseBody)
		require.True(t, ok)
		require.NotNil(t, re)
		require.NotNil(t, re.ResponseBody)
		require.NotNil(t, re.ResponseBody.Response)
		require.IsType(t, &extprocv3.BodyMutation{}, re.ResponseBody.Response.BodyMutation)
		require.IsType(t, &extprocv3.HeaderMutation{}, re.ResponseBody.Response.HeaderMutation)
	})
}

func TestChatCompletionProcessorRouterFilter_ProcessResponseBody_SpanHandling(t *testing.T) {
	t.Run("passthrough without span", func(t *testing.T) {
		p := &chatCompletionProcessorRouterFilter{
			span:                nil,
			originalRequestBody: &openai.ChatCompletionRequest{Stream: false},
		}
		_, err := p.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{EndOfStream: true, Body: []byte("response")})
		require.NoError(t, err)
		// Should not panic when span is nil.
	})

	t.Run("upstream filter span", func(t *testing.T) {
		span := &testotel.MockSpan{}
		mt := &mockTranslator{t: t}
		p := &chatCompletionProcessorRouterFilter{
			originalRequestBody: &openai.ChatCompletionRequest{Stream: true},
			upstreamFilter: &chatCompletionProcessorUpstreamFilter{
				responseHeaders: map[string]string{":status": "200"},
				translator:      mt,
				logger:          slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
				metrics:         &mockChatCompletionMetrics{},
				config:          &processorConfig{metadataNamespace: ""},
				span:            span,
			},
		}

		finalBody := &extprocv3.HttpBody{EndOfStream: true, Body: []byte("final")}
		mt.expResponseBody = finalBody
		_, err := p.ProcessResponseBody(t.Context(), finalBody)
		require.NoError(t, err)
		require.True(t, span.EndSpanCalled)
	})

	t.Run("upstream filter error with span", func(t *testing.T) {
		span := &testotel.MockSpan{}
		p := &chatCompletionProcessorRouterFilter{
			originalRequestBody: &openai.ChatCompletionRequest{Stream: false},
			upstreamFilter: &chatCompletionProcessorUpstreamFilter{
				responseHeaders: map[string]string{":status": "500"},
				translator:      &mockTranslator{t: t},
				logger:          slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
				metrics:         &mockChatCompletionMetrics{},
				config:          &processorConfig{metadataNamespace: ""},
				span:            span,
			},
		}

		errorBody := "error response"
		_, err := p.ProcessResponseBody(t.Context(), &extprocv3.HttpBody{EndOfStream: true, Body: []byte(errorBody)})
		require.NoError(t, err)
		require.Equal(t, 500, span.ErrorStatus)
		require.Equal(t, errorBody, span.ErrBody)
	})
}

// mockTokenizer is a mock implementation of the tokenizer.Codec interface.
type mockTokenizer struct {
	tokenizer.Codec
	tokens map[string]int
}

// Encode overrides [tokenizer.Codec.Encode] method for the mock tokenizer.
func (m *mockTokenizer) Encode(text string) ([]uint, []string, error) {
	if count, ok := m.tokens[text]; ok {
		return make([]uint, count), make([]string, count), nil
	}
	return nil, nil, errors.New("text not found in mock tokenizer")
}

func Test_maybeMiddleOutCompressionImpl(t *testing.T) {
	tests := []struct {
		name                 string
		messages             []openai.ChatCompletionMessageParamUnion
		tokenCounts          map[string]int
		maximumContextLength int
		expectedCompressed   bool
		expectedMessageCount int
		expectedMessageTypes []string
	}{
		{
			name: "no compression needed - under token limit and message count",
			messages: []openai.ChatCompletionMessageParamUnion{
				{Type: openai.ChatMessageRoleSystem, Value: openai.ChatCompletionSystemMessageParam{
					Role:    openai.ChatMessageRoleSystem,
					Content: openai.StringOrArray{Value: "system message"},
				}},
				{Type: openai.ChatMessageRoleUser, Value: openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "user message"},
				}},
				{Type: openai.ChatMessageRoleAssistant, Value: openai.ChatCompletionAssistantMessageParam{
					Role:    openai.ChatMessageRoleAssistant,
					Content: openai.StringOrAssistantRoleContentUnion{Value: "assistant message"},
				}},
			},
			tokenCounts:          map[string]int{"system message": 10, "user message": 10, "assistant message": 10},
			maximumContextLength: 100,
			expectedCompressed:   false,
			expectedMessageCount: 3,
		},
		{
			name: "compression needed - over token limit",
			messages: []openai.ChatCompletionMessageParamUnion{
				{Type: openai.ChatMessageRoleSystem, Value: openai.ChatCompletionSystemMessageParam{
					Role:    openai.ChatMessageRoleSystem,
					Content: openai.StringOrArray{Value: "system message"},
				}},
				{Type: openai.ChatMessageRoleUser, Value: openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "user1"},
				}},
				{Type: openai.ChatMessageRoleUser, Value: openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "user2"},
				}},
				{Type: openai.ChatMessageRoleUser, Value: openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "user3"},
				}},
				{Type: openai.ChatMessageRoleAssistant, Value: openai.ChatCompletionAssistantMessageParam{
					Role:    openai.ChatMessageRoleAssistant,
					Content: openai.StringOrAssistantRoleContentUnion{Value: "assistant message"},
				}},
			},
			tokenCounts:          map[string]int{"system message": 30, "user1": 30, "user2": 30, "user3": 30, "assistant message": 30},
			maximumContextLength: 100,
			expectedCompressed:   true,
			expectedMessageCount: 3,
			expectedMessageTypes: []string{
				openai.ChatMessageRoleSystem,
				openai.ChatMessageRoleUser,
				openai.ChatMessageRoleAssistant,
			},
		},
		{
			name: "user message with array content",
			messages: []openai.ChatCompletionMessageParamUnion{
				{Type: openai.ChatMessageRoleUser, Value: openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: []openai.ChatCompletionContentPartUserUnionParam{
						{TextContent: &openai.ChatCompletionContentPartTextParam{Type: string(openai.ChatCompletionContentPartTextTypeText), Text: "text1"}},
						{TextContent: &openai.ChatCompletionContentPartTextParam{Type: string(openai.ChatCompletionContentPartTextTypeText), Text: "text2"}},
					}},
				}},
			},
			tokenCounts:          map[string]int{"text1": 50, "text2": 60},
			maximumContextLength: 100,
			expectedCompressed:   true,
			expectedMessageCount: 0,
		},
		{
			name: "assistant message with array content",
			messages: []openai.ChatCompletionMessageParamUnion{
				{Type: openai.ChatMessageRoleAssistant, Value: openai.ChatCompletionAssistantMessageParam{
					Role: openai.ChatMessageRoleAssistant,
					Content: openai.StringOrAssistantRoleContentUnion{Value: []openai.ChatCompletionAssistantMessageParamContent{
						{Type: openai.ChatCompletionAssistantMessageParamContentTypeText, Text: ptr.To("assistant text")},
						{Type: openai.ChatCompletionAssistantMessageParamContentTypeRefusal, Refusal: ptr.To("refusal text")},
					}},
				}},
			},
			tokenCounts:          map[string]int{"assistant text": 30, "refusal text": 30},
			maximumContextLength: 100,
			expectedCompressed:   false,
			expectedMessageCount: 1, // Assistant message should be kept.
		},
		{
			name: "system message with array content",
			messages: []openai.ChatCompletionMessageParamUnion{
				{Type: openai.ChatMessageRoleSystem, Value: openai.ChatCompletionSystemMessageParam{
					Role: openai.ChatMessageRoleSystem,
					Content: openai.StringOrArray{Value: []openai.ChatCompletionContentPartTextParam{
						{Text: "system text 1"},
						{Text: "system text 2"},
					}},
				}},
			},
			tokenCounts:          map[string]int{"system text 1": 25, "system text 2": 25},
			maximumContextLength: 100,
			expectedCompressed:   false,
			expectedMessageCount: 1, // System message should be kept.
		},
		{
			name: "developer message with string content",
			messages: []openai.ChatCompletionMessageParamUnion{
				{Type: openai.ChatMessageRoleDeveloper, Value: openai.ChatCompletionDeveloperMessageParam{
					Role:    openai.ChatMessageRoleDeveloper,
					Content: openai.StringOrArray{Value: "developer message"},
				}},
			},
			tokenCounts:          map[string]int{"developer message": 10000},
			maximumContextLength: 100,
			expectedCompressed:   false,
			expectedMessageCount: 1, // Developer message should be kept.
		},
		{
			name: "developer message with array content",
			messages: []openai.ChatCompletionMessageParamUnion{
				{Type: openai.ChatMessageRoleDeveloper, Value: openai.ChatCompletionDeveloperMessageParam{
					Role: openai.ChatMessageRoleDeveloper,
					Content: openai.StringOrArray{Value: []openai.ChatCompletionContentPartTextParam{
						{Text: "dev text"},
					}},
				}},
			},
			tokenCounts:          map[string]int{"dev text": 10000},
			maximumContextLength: 100,
			expectedCompressed:   false,
			expectedMessageCount: 1, // Developer message should be kept.
		},
		{
			name: "tool message with string content",
			messages: []openai.ChatCompletionMessageParamUnion{
				{Type: openai.ChatMessageRoleTool, Value: openai.ChatCompletionToolMessageParam{
					Role:       openai.ChatMessageRoleTool,
					Content:    openai.StringOrArray{Value: "tool response"},
					ToolCallID: "tool-123",
				}},
			},
			tokenCounts:          map[string]int{"tool response": 10000},
			maximumContextLength: 100,
			expectedCompressed:   false,
			expectedMessageCount: 1, // Tool message should be kept.
		},
		{
			name: "tool message with array content",
			messages: []openai.ChatCompletionMessageParamUnion{
				{Type: openai.ChatMessageRoleTool, Value: openai.ChatCompletionToolMessageParam{
					Role: openai.ChatMessageRoleTool,
					Content: openai.StringOrArray{Value: []openai.ChatCompletionContentPartTextParam{
						{Text: "tool text"},
					}},
					ToolCallID: "tool-456",
				}},
			},
			tokenCounts:          map[string]int{"tool text": 10000},
			maximumContextLength: 100,
			expectedCompressed:   false,
			expectedMessageCount: 1, // Tool message should be kept.
		},
		{
			name: "empty string content",
			messages: []openai.ChatCompletionMessageParamUnion{
				{Type: openai.ChatMessageRoleUser, Value: openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: ""},
				}},
				{Type: openai.ChatMessageRoleUser, Value: openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "non-empty"},
				}},
			},
			tokenCounts:          map[string]int{"non-empty": 80},
			maximumContextLength: 100,
			expectedCompressed:   false,
			expectedMessageCount: 2, // Below threshold, both messages kept.
		},
		{
			name: "mixed message types with compression",
			messages: []openai.ChatCompletionMessageParamUnion{
				{Type: openai.ChatMessageRoleSystem, Value: openai.ChatCompletionSystemMessageParam{
					Role:    openai.ChatMessageRoleSystem,
					Content: openai.StringOrArray{Value: "system"},
				}},
				{Type: openai.ChatMessageRoleUser, Value: openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "user1"},
				}},
				{Type: openai.ChatMessageRoleAssistant, Value: openai.ChatCompletionAssistantMessageParam{
					Role:    openai.ChatMessageRoleAssistant,
					Content: openai.StringOrAssistantRoleContentUnion{Value: "assistant1"},
				}},
				{Type: openai.ChatMessageRoleUser, Value: openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "user2"},
				}},
				{Type: openai.ChatMessageRoleUser, Value: openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "user3"},
				}},
				{Type: openai.ChatMessageRoleAssistant, Value: openai.ChatCompletionAssistantMessageParam{
					Role:    openai.ChatMessageRoleAssistant,
					Content: openai.StringOrAssistantRoleContentUnion{Value: "assistant2"},
				}},
			},
			tokenCounts:          map[string]int{"system": 30, "user1": 30, "assistant1": 30, "user2": 30, "user3": 30, "assistant2": 30},
			maximumContextLength: 120,
			expectedCompressed:   true,
			expectedMessageCount: 5,
			expectedMessageTypes: []string{
				openai.ChatMessageRoleSystem,
				openai.ChatMessageRoleUser,
				openai.ChatMessageRoleAssistant,
				openai.ChatMessageRoleUser,
				openai.ChatMessageRoleAssistant,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &openai.ChatCompletionRequest{
				Messages: tt.messages,
			}

			mockTokenizer := &mockTokenizer{
				tokens: tt.tokenCounts,
			}

			compressed := maybeMiddleOutCompressionImpl(req, mockTokenizer, tt.maximumContextLength, slog.Default())

			require.Equal(t, tt.expectedCompressed, compressed, "compression result mismatch")
			require.Equal(t, tt.expectedMessageCount, len(req.Messages), "message count after compression mismatch")

			if tt.expectedMessageTypes != nil {
				actualTypes := make([]string, len(req.Messages))
				for i, msg := range req.Messages {
					actualTypes[i] = msg.Type
				}
				require.Equal(t, tt.expectedMessageTypes, actualTypes, "message types after compression mismatch")
			}
		})
	}
}
