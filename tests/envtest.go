package tests

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/envoyproxy/ai-gateway/internal/controller"
)

func RunEnvTest(m *testing.M, c *client.Client) int {
	log.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))
	base := filepath.Join("..", "..", "manifests", "charts", "ai-gateway-helm", "crds")

	wd, _ := os.Getwd()
	fmt.Println(base, wd)

	crds := make([]string, 0, 3)
	for _, crd := range []string{
		"aigateway.envoyproxy.io_llmroutes.yaml",
		"aigateway.envoyproxy.io_llmbackends.yaml",
		"aigateway.envoyproxy.io_backendsecuritypolicies.yaml",
	} {
		crds = append(crds, filepath.Join(base, crd))
	}

	env := &envtest.Environment{CRDDirectoryPaths: crds}
	cfg, err := env.Start()
	if err != nil {
		panic(fmt.Sprintf("Failed to start testenv: %v", err))
	}

	_, cancel := context.WithCancel(ctrl.SetupSignalHandler())
	defer func() {
		cancel()
		if err := env.Stop(); err != nil {
			panic(fmt.Sprintf("Failed to stop testenv: %v", err))
		}
	}()

	*c, err = client.New(cfg, client.Options{})
	if err != nil {
		panic(fmt.Sprintf("Error initializing client: %v", err))
	}

	controller.MustInitializeScheme((*c).Scheme())
	return m.Run()
}
