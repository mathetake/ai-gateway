package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/envoyproxy/ai-gateway/internal/backendauth"
	"github.com/envoyproxy/ai-gateway/internal/dynamic_module/sdk"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

func main() {} // This must be present to make a shared library.

// Set the envoy.NewHTTPFilter function to create a new http filter.
func init() {
	sdk.NewHTTPFilterConfig = newHTTPFilterConfig
	ec := newEnvConfig()

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
		return newRouterFilterConfig(filterConfigReceiver)
	case "ai_gateway.upstream":
		return &upstreamFilterConfig{}
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

type envConfig struct {
	filterConfigPath string
	// TODO: log level.
}

func newEnvConfig() *envConfig {
	return &envConfig{
		filterConfigPath: os.Getenv("AI_GATEWAY_DYNAMIC_MODULE_FILTER_CONFIG_PATH"),
	}
}

// endpoint represents the type of the endpoint that the request is targeting.
type endpoint int

const (
	// chatCompletionsEndpoint represents the /v1/chat/completions endpoint.
	chatCompletionsEndpoint endpoint = iota
	// completionsEndpoint represents the /v1/completions endpoint.
	completionsEndpoint
	// embeddingsEndpoint represents the /v1/embeddings endpoint.
	embeddingsEndpoint
	// imagesGenerationsEndpoint represents the /v1/images/generations endpoint.
	imagesGenerationsEndpoint
	// rerankEndpoint represents the /v2/rerank endpoint of cohere.
	rerankEndpoint
	// messagesEndpoint represents the /v1/messages endpoint of anthropic.
	messagesEndpoint
	// modelsEndpoint represents the /v1/models endpoint.
	modelsEndpoint
)
