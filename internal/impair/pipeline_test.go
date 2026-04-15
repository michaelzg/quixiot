package impair

import (
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"quixiot/internal/config"
)

func TestPipelineObservedDropRateWithinOneSigma(t *testing.T) {
	const (
		samples = 20000
		drop    = 0.15
	)

	p, err := NewPipeline(DirectionConfig{DropProbability: drop}, 2)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}

	now := time.Unix(1700000000, 0)
	for i := 0; i < samples; i++ {
		p.Enqueue(now, []byte{byte(i)})
	}

	ready := p.ReleaseReady(now)
	dropped := samples - len(ready)
	expected := float64(samples) * drop
	sigma := math.Sqrt(float64(samples) * drop * (1 - drop))

	if diff := math.Abs(float64(dropped) - expected); diff > sigma {
		t.Fatalf("drop diff beyond 1 sigma: dropped=%d expected=%.1f sigma=%.2f diff=%.2f", dropped, expected, sigma, diff)
	}
}

func TestPipelineReorderFlushesWithoutFollower(t *testing.T) {
	p, err := NewPipeline(DirectionConfig{
		ReorderProbability: 1,
		ReorderHold:        5 * time.Millisecond,
	}, 7)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}

	start := time.Unix(1700000000, 0)
	p.Enqueue(start, []byte("first"))

	if ready := p.ReleaseReady(start); len(ready) != 0 {
		t.Fatalf("ready at start: want 0 got %d", len(ready))
	}

	next, ok := p.NextWake()
	if !ok || !next.Equal(start.Add(5*time.Millisecond)) {
		t.Fatalf("NextWake: want %s got %s ok=%v", start.Add(5*time.Millisecond), next, ok)
	}

	ready := p.ReleaseReady(start.Add(5 * time.Millisecond))
	if got := payloads(ready); !reflect.DeepEqual(got, []string{"first"}) {
		t.Fatalf("ready after hold: want [first] got %v", got)
	}
}

func TestPipelineReordersAdjacentPackets(t *testing.T) {
	p, err := NewPipeline(DirectionConfig{
		ReorderProbability: 1,
		ReorderHold:        10 * time.Millisecond,
	}, 11)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}

	start := time.Unix(1700000000, 0)
	p.Enqueue(start, []byte("first"))
	p.Enqueue(start.Add(2*time.Millisecond), []byte("second"))

	ready := p.ReleaseReady(start.Add(2 * time.Millisecond))
	if got := payloads(ready); !reflect.DeepEqual(got, []string{"second", "first"}) {
		t.Fatalf("ready after reorder: want [second first] got %v", got)
	}
}

func TestPipelineBandwidthSpacing(t *testing.T) {
	p, err := NewPipeline(DirectionConfig{BandwidthBytesPerSec: 1000}, 13)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}

	start := time.Unix(1700000000, 0)
	p.Enqueue(start, make([]byte, 500))
	p.Enqueue(start, make([]byte, 500))

	if next, ok := p.NextWake(); !ok || !next.Equal(start.Add(500*time.Millisecond)) {
		t.Fatalf("first wake: want %s got %s ok=%v", start.Add(500*time.Millisecond), next, ok)
	}
	if ready := p.ReleaseReady(start.Add(499 * time.Millisecond)); len(ready) != 0 {
		t.Fatalf("ready too early: want 0 got %d", len(ready))
	}
	if ready := p.ReleaseReady(start.Add(500 * time.Millisecond)); len(ready) != 1 {
		t.Fatalf("ready at first slot: want 1 got %d", len(ready))
	}
	if next, ok := p.NextWake(); !ok || !next.Equal(start.Add(time.Second)) {
		t.Fatalf("second wake: want %s got %s ok=%v", start.Add(time.Second), next, ok)
	}
}

func TestProfileConfigsLoadAndValidate(t *testing.T) {
	files := []string{
		"proxy-wifi-good.yaml",
		"proxy-cellular-lte.yaml",
		"proxy-cellular-3g.yaml",
		"proxy-satellite.yaml",
		"proxy-flaky.yaml",
	}

	for _, name := range files {
		t.Run(name, func(t *testing.T) {
			var profile Profile
			path := filepath.Join("..", "..", "configs", name)
			if err := config.LoadFile(path, &profile); err != nil {
				t.Fatalf("LoadFile(%s): %v", path, err)
			}
			if _, err := NormalizeProfile(profile); err != nil {
				t.Fatalf("NormalizeProfile(%s): %v", name, err)
			}
		})
	}
}

func payloads(pkts [][]byte) []string {
	out := make([]string, 0, len(pkts))
	for _, pkt := range pkts {
		out = append(out, string(pkt))
	}
	return out
}
