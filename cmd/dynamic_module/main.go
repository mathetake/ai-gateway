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
	g := &globalState{}
	if err := g.initializeEnv(); err != nil {
		panic("failed to create env config: " + err.Error())
	}

	// TODO: use a writer implemented with the Logger ABI of Envoy.
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug, // Adjust log level from environment variable if needed.
	}))
	if err := filterapi.StartConfigWatcher(context.Background(),
		os.Getenv("AI_GATEWAY_DYNAMIC_MODULE_FILTER_CONFIG_PATH"), g, logger, time.Second*5); err != nil {
		panic("failed to start filter config watcher: " + err.Error())
	}
	sdk.NewHTTPFilterConfig = g.newHTTPFilterConfig
}

// filterConfigReceiverImpl implements [filterapi.ConfigReceiver] to load filter configuration.
type globalState struct {
	fc  *filterapi.RuntimeConfig
	env *dynamic_module.Env
}

// newHTTPFilter creates a new http filter based on the config.
//
// `config` is the configuration string that is specified in the Envoy configuration.
func (g *globalState) newHTTPFilterConfig(name string, _ []byte) sdk.HTTPFilterConfig {
	switch name {
	case "ai_gateway.router":
		return dynamic_module.NewRouterFilterConfig(g.env, &g.fc)
	case "ai_gateway.upstream":
		return dynamic_module.NewUpstreamFilterConfig(g.env)
	default:
		panic("unknown filter: " + name)
	}
}

// LoadConfig implements [filterapi.ConfigReceiver.LoadConfig].
func (g *globalState) LoadConfig(ctx context.Context, config *filterapi.Config) error {
	newConfig, err := filterapi.NewRuntimeConfig(ctx, config, backendauth.NewHandler)
	if err != nil {
		return fmt.Errorf("cannot create runtime filter config: %w", err)
	}
	g.fc = newConfig // This is racy but we don't care.
	return nil
}

func (g *globalState) initializeEnv() error {
	ctx := context.Background()
	promRegistry := prometheus.NewRegistry()
	promReader, err := otelprom.New(otelprom.WithRegisterer(promRegistry))
	if err != nil {
		return fmt.Errorf("failed to create prometheus reader: %w", err)
	}

	meter, _, err := metrics.NewMetricsFromEnv(ctx, os.Stdout, promReader)
	if err != nil {
		return fmt.Errorf("failed to create metrics: %w", err)
	}

	endpointPrefixes, err := internalapi.ParseEndpointPrefixes(os.Getenv(
		"AI_GATEWAY_DYNAMIC_MODULE_FILTER_ENDPOINT_PREFIXES",
	))
	if err != nil {
		return fmt.Errorf("failed to parse endpoint prefixes: %w", err)
	}

	metricsRequestHeaderAttributes, err := internalapi.ParseRequestHeaderAttributeMapping(os.Getenv(
		"AI_GATEWAY_DYNAMIC_MODULE_FILTER_METRICS_REQUEST_HEADER_ATTRIBUTES",
	))
	if err != nil {
		return fmt.Errorf("failed to parse metrics header mapping: %w", err)
	}

	g.env = &dynamic_module.Env{
		RootPrefix:                    os.Getenv("AI_GATEWAY_DYNAMIC_MODULE_ROOT_PREFIX"),
		EndpointPrefixes:              endpointPrefixes,
		ChatCompletionMetricsFactory:  metrics.NewChatCompletionFactory(meter, metricsRequestHeaderAttributes),
		MessagesMetricsFactory:        metrics.NewMessagesFactory(meter, metricsRequestHeaderAttributes),
		CompletionMetricsFactory:      metrics.NewCompletionFactory(meter, metricsRequestHeaderAttributes),
		EmbeddingsMetricsFactory:      metrics.NewEmbeddingsFactory(meter, metricsRequestHeaderAttributes),
		ImageGenerationMetricsFactory: metrics.NewImageGenerationFactory(meter, metricsRequestHeaderAttributes),
		RerankMetricsFactory:          metrics.NewRerankFactory(meter, metricsRequestHeaderAttributes),
	}
	return nil
}
