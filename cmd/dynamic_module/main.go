package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/envoyproxy/ai-gateway/internal/backendauth"
	"github.com/envoyproxy/ai-gateway/internal/dynamic_module"
	"github.com/envoyproxy/ai-gateway/internal/dynamic_module/sdk"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
)

func main() {} // This must be present to make a shared library.

// Set the envoy.NewHTTPFilter function to create a new http filter.
func init() {
	sdk.NewHTTPFilterConfig = newHTTPFilterConfig
	ec, err := newEnv(context.Background())
	if err != nil {
		panic("failed to create env config: " + err.Error())
	}

	// TODO: use a writer implemented with the Logger ABI of Envoy.
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug, // Adjust log level from environment variable if needed.
	}))
	if err := filterapi.StartConfigWatcher(context.Background(), ec.filterConfigPath, filterConfigReceiver, logger, time.Second*5); err != nil {
		panic("failed to start filter config watcher: " + err.Error())
	}
}

// newHTTPFilter creates a new http filter based on the config.
//
// `config` is the configuration string that is specified in the Envoy configuration.
func newHTTPFilterConfig(name string, _ []byte) sdk.HTTPFilterConfig {
	switch name {
	case "ai_gateway.router":
		return dynamic_module.NewRouterFilterConfig(&filterConfigReceiver.fc)
	case "ai_gateway.upstream":
		return dynamic_module.NewUpstreamFilterConfig()
	default:
		panic("unknown filter: " + name)
	}
}

var filterConfigReceiver *filterConfigReceiverImpl

// filterConfigReceiverImpl implements [filterapi.ConfigReceiver] to load filter configuration.
type filterConfigReceiverImpl struct {
	fc *filterapi.RuntimeConfig
}

// LoadConfig implements [filterapi.ConfigReceiver.LoadConfig].
func (l *filterConfigReceiverImpl) LoadConfig(ctx context.Context, config *filterapi.Config) error {
	newConfig, err := filterapi.NewRuntimeConfig(ctx, config, backendauth.NewHandler)
	if err != nil {
		return fmt.Errorf("cannot create runtime filter config: %w", err)
	}
	l.fc = newConfig // This is racy but we don't care.
	return nil
}

type env struct {
	filterConfigPath              string
	rootPrefix                    string
	endpointPrefixes              *internalapi.EndpointPrefixes
	chatCompletionMetricsFactory  metrics.ChatCompletionMetricsFactory
	messagesMetricsFactory        metrics.MessagesMetricsFactory
	completionMetricsFactory      metrics.CompletionMetricsFactory
	embeddingsMetricsFactory      metrics.EmbeddingsMetricsFactory
	imageGenerationMetricsFactory metrics.ImageGenerationMetricsFactory
	rerankMetricsFactory          metrics.RerankMetricsFactory
	// TODO: log level.
}

func newEnv(ctx context.Context) (*env, error) {
	promRegistry := prometheus.NewRegistry()
	promReader, err := otelprom.New(otelprom.WithRegisterer(promRegistry))
	if err != nil {
		return nil, fmt.Errorf("failed to create prometheus reader: %w", err)
	}

	meter, _, err := metrics.NewMetricsFromEnv(ctx, os.Stdout, promReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create metrics: %w", err)
	}

	metricsRequestHeaderAttributes, err := internalapi.ParseRequestHeaderAttributeMapping(os.Getenv(
		"AI_GATEWAY_DYNAMIC_MODULE_FILTER_METRICS_REQUEST_HEADER_ATTRIBUTES",
	))
	if err != nil {
		return nil, fmt.Errorf("failed to parse metrics header mapping: %w", err)
	}

	e := &env{
		filterConfigPath: os.Getenv("AI_GATEWAY_DYNAMIC_MODULE_FILTER_CONFIG_PATH"),
		rootPrefix:       os.Getenv("AI_GATEWAY_DYNAMIC_MODULE_ROOT_PREFIX"),
	}
	e.chatCompletionMetricsFactory = metrics.NewChatCompletionFactory(meter, metricsRequestHeaderAttributes)
	e.messagesMetricsFactory = metrics.NewMessagesFactory(meter, metricsRequestHeaderAttributes)
	e.completionMetricsFactory = metrics.NewCompletionFactory(meter, metricsRequestHeaderAttributes)
	e.embeddingsMetricsFactory = metrics.NewEmbeddingsFactory(meter, metricsRequestHeaderAttributes)
	e.imageGenerationMetricsFactory = metrics.NewImageGenerationFactory(meter, metricsRequestHeaderAttributes)
	e.rerankMetricsFactory = metrics.NewRerankFactory(meter, metricsRequestHeaderAttributes)

	return e, nil
}
