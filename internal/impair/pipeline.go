package impair

import (
	"container/heap"
	"fmt"
	"math/rand"
	"time"
)

const defaultReorderHold = 3 * time.Millisecond

type DirectionConfig struct {
	DropProbability      float64       `yaml:"drop_probability"`
	DuplicateProbability float64       `yaml:"duplicate_probability"`
	ReorderProbability   float64       `yaml:"reorder_probability"`
	ReorderHold          time.Duration `yaml:"reorder_hold"`
	Latency              time.Duration `yaml:"latency"`
	Jitter               time.Duration `yaml:"jitter"`
	BandwidthBytesPerSec int64         `yaml:"bandwidth_bytes_per_sec"`
}

type Profile struct {
	Name     string          `yaml:"name"`
	Seed     int64           `yaml:"seed"`
	ToServer DirectionConfig `yaml:"to_server"`
	ToClient DirectionConfig `yaml:"to_client"`
}

type Pipeline struct {
	cfg            DirectionConfig
	rng            *rand.Rand
	queue          packetHeap
	held           *heldPacket
	nextSeq        uint64
	bandwidthReady time.Time
}

type heldPacket struct {
	data      []byte
	releaseAt time.Time
}

type scheduledPacket struct {
	data      []byte
	releaseAt time.Time
	seq       uint64
}

type packetHeap []scheduledPacket

func PassthroughProfile() Profile {
	return Profile{Name: "passthrough"}
}

func NormalizeProfile(profile Profile) (Profile, error) {
	if profile == (Profile{}) {
		profile = PassthroughProfile()
	}
	if profile.Name == "" {
		profile.Name = "custom"
	}
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func (p Profile) Validate() error {
	if err := p.ToServer.Validate("to_server"); err != nil {
		return err
	}
	if err := p.ToClient.Validate("to_client"); err != nil {
		return err
	}
	return nil
}

func (c DirectionConfig) Validate(field string) error {
	switch {
	case c.DropProbability < 0 || c.DropProbability > 1:
		return fmt.Errorf("impair: %s.drop_probability must be between 0 and 1", field)
	case c.DuplicateProbability < 0 || c.DuplicateProbability > 1:
		return fmt.Errorf("impair: %s.duplicate_probability must be between 0 and 1", field)
	case c.ReorderProbability < 0 || c.ReorderProbability > 1:
		return fmt.Errorf("impair: %s.reorder_probability must be between 0 and 1", field)
	case c.ReorderHold < 0:
		return fmt.Errorf("impair: %s.reorder_hold must be non-negative", field)
	case c.Latency < 0:
		return fmt.Errorf("impair: %s.latency must be non-negative", field)
	case c.Jitter < 0:
		return fmt.Errorf("impair: %s.jitter must be non-negative", field)
	case c.BandwidthBytesPerSec < 0:
		return fmt.Errorf("impair: %s.bandwidth_bytes_per_sec must be non-negative", field)
	default:
		return nil
	}
}

func NewPipeline(cfg DirectionConfig, seed int64) (*Pipeline, error) {
	if err := cfg.Validate("direction"); err != nil {
		return nil, err
	}
	return &Pipeline{
		cfg: cfg,
		rng: rand.New(rand.NewSource(seed)),
	}, nil
}

func (p *Pipeline) Enqueue(now time.Time, data []byte) {
	if p == nil {
		return
	}
	p.flushExpiredHeld(now)
	if p.roll(p.cfg.DropProbability) {
		return
	}

	copies := 1
	if p.roll(p.cfg.DuplicateProbability) {
		copies++
	}
	for range copies {
		p.enqueueCopy(now, data)
	}
}

func (p *Pipeline) Flush(now time.Time) {
	if p == nil || p.held == nil {
		return
	}
	held := p.held
	p.held = nil
	p.schedule(now, held.data)
}

func (p *Pipeline) NextWake() (time.Time, bool) {
	if p == nil {
		return time.Time{}, false
	}
	var next time.Time
	if p.held != nil {
		next = p.held.releaseAt
	}
	if len(p.queue) > 0 {
		queued := p.queue[0].releaseAt
		if next.IsZero() || queued.Before(next) {
			next = queued
		}
	}
	if next.IsZero() {
		return time.Time{}, false
	}
	return next, true
}

func (p *Pipeline) ReleaseReady(now time.Time) [][]byte {
	if p == nil {
		return nil
	}
	p.flushExpiredHeld(now)

	var ready [][]byte
	for len(p.queue) > 0 {
		next := p.queue[0]
		if next.releaseAt.After(now) {
			break
		}
		item := heap.Pop(&p.queue).(scheduledPacket)
		ready = append(ready, item.data)
	}
	return ready
}

func (p *Pipeline) enqueueCopy(now time.Time, data []byte) {
	if p.held == nil && p.roll(p.cfg.ReorderProbability) {
		p.held = &heldPacket{
			data:      data,
			releaseAt: now.Add(p.reorderHold()),
		}
		return
	}
	if p.held != nil {
		p.schedule(now, data)
		held := p.held
		p.held = nil
		p.schedule(now, held.data)
		return
	}
	p.schedule(now, data)
}

func (p *Pipeline) flushExpiredHeld(now time.Time) {
	if p.held == nil || p.held.releaseAt.After(now) {
		return
	}
	held := p.held
	p.held = nil
	p.schedule(held.releaseAt, held.data)
}

func (p *Pipeline) schedule(now time.Time, data []byte) {
	releaseAt := now
	if p.cfg.Latency > 0 {
		releaseAt = releaseAt.Add(p.cfg.Latency)
	}
	if p.cfg.Jitter > 0 {
		releaseAt = releaseAt.Add(time.Duration(p.rng.Int63n(int64(p.cfg.Jitter) + 1)))
	}
	if p.cfg.BandwidthBytesPerSec > 0 {
		if p.bandwidthReady.After(releaseAt) {
			releaseAt = p.bandwidthReady
		}
		releaseAt = releaseAt.Add(transmitDuration(len(data), p.cfg.BandwidthBytesPerSec))
		p.bandwidthReady = releaseAt
	}

	heap.Push(&p.queue, scheduledPacket{
		data:      data,
		releaseAt: releaseAt,
		seq:       p.nextSeq,
	})
	p.nextSeq++
}

func (p *Pipeline) reorderHold() time.Duration {
	if p.cfg.ReorderHold > 0 {
		return p.cfg.ReorderHold
	}
	return defaultReorderHold
}

func (p *Pipeline) roll(prob float64) bool {
	switch {
	case prob <= 0:
		return false
	case prob >= 1:
		return true
	default:
		return p.rng.Float64() < prob
	}
}

func transmitDuration(size int, bytesPerSecond int64) time.Duration {
	if size <= 0 || bytesPerSecond <= 0 {
		return 0
	}
	numerator := int64(size) * int64(time.Second)
	rounded := (numerator + bytesPerSecond - 1) / bytesPerSecond
	if rounded == 0 {
		rounded = 1
	}
	return time.Duration(rounded)
}

func (h packetHeap) Len() int {
	return len(h)
}

func (h packetHeap) Less(i, j int) bool {
	if h[i].releaseAt.Equal(h[j].releaseAt) {
		return h[i].seq < h[j].seq
	}
	return h[i].releaseAt.Before(h[j].releaseAt)
}

func (h packetHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *packetHeap) Push(x any) {
	*h = append(*h, x.(scheduledPacket))
}

func (h *packetHeap) Pop() any {
	old := *h
	last := len(old) - 1
	item := old[last]
	*h = old[:last]
	return item
}
