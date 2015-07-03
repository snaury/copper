package benchmark

import (
	"os"
	"testing"

	"google.golang.org/grpc/benchmark/stats"
)

func TestMain(m *testing.M) {
	os.Exit(stats.RunTestMain(m))
}
