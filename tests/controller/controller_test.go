//go:build test_controller

package controller

import (
	"os"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/envoyproxy/ai-gateway/tests"
)

var c client.Client

func TestMain(m *testing.M) {
	os.Exit(tests.RunEnvTest(m, &c))
}
