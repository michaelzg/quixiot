package impair

import "time"

// BuiltinProfile returns one of the shipped named proxy profiles.
func BuiltinProfile(name string) (Profile, bool) {
	profile, ok := builtinProfiles[name]
	return profile, ok
}

var builtinProfiles = map[string]Profile{
	"passthrough": PassthroughProfile(),
	"wifi-good": {
		Name: "wifi-good",
		Seed: 1001,
		ToServer: DirectionConfig{
			DropProbability:      0.002,
			DuplicateProbability: 0,
			ReorderProbability:   0.001,
			ReorderHold:          2 * time.Millisecond,
			Latency:              8 * time.Millisecond,
			Jitter:               3 * time.Millisecond,
			BandwidthBytesPerSec: 10_000_000,
		},
		ToClient: DirectionConfig{
			DropProbability:      0.002,
			DuplicateProbability: 0,
			ReorderProbability:   0.001,
			ReorderHold:          2 * time.Millisecond,
			Latency:              8 * time.Millisecond,
			Jitter:               3 * time.Millisecond,
			BandwidthBytesPerSec: 10_000_000,
		},
	},
	"cellular-lte": {
		Name: "cellular-lte",
		Seed: 2002,
		ToServer: DirectionConfig{
			DropProbability:      0.01,
			DuplicateProbability: 0.001,
			ReorderProbability:   0.01,
			ReorderHold:          5 * time.Millisecond,
			Latency:              35 * time.Millisecond,
			Jitter:               15 * time.Millisecond,
			BandwidthBytesPerSec: 2_500_000,
		},
		ToClient: DirectionConfig{
			DropProbability:      0.01,
			DuplicateProbability: 0.001,
			ReorderProbability:   0.01,
			ReorderHold:          5 * time.Millisecond,
			Latency:              35 * time.Millisecond,
			Jitter:               15 * time.Millisecond,
			BandwidthBytesPerSec: 2_500_000,
		},
	},
	"cellular-3g": {
		Name: "cellular-3g",
		Seed: 3003,
		ToServer: DirectionConfig{
			DropProbability:      0.03,
			DuplicateProbability: 0.002,
			ReorderProbability:   0.02,
			ReorderHold:          8 * time.Millisecond,
			Latency:              90 * time.Millisecond,
			Jitter:               40 * time.Millisecond,
			BandwidthBytesPerSec: 550_000,
		},
		ToClient: DirectionConfig{
			DropProbability:      0.03,
			DuplicateProbability: 0.002,
			ReorderProbability:   0.02,
			ReorderHold:          8 * time.Millisecond,
			Latency:              90 * time.Millisecond,
			Jitter:               40 * time.Millisecond,
			BandwidthBytesPerSec: 550_000,
		},
	},
	"satellite": {
		Name: "satellite",
		Seed: 4004,
		ToServer: DirectionConfig{
			DropProbability:      0.005,
			DuplicateProbability: 0.001,
			ReorderProbability:   0.01,
			ReorderHold:          15 * time.Millisecond,
			Latency:              600 * time.Millisecond,
			Jitter:               80 * time.Millisecond,
			BandwidthBytesPerSec: 250_000,
		},
		ToClient: DirectionConfig{
			DropProbability:      0.005,
			DuplicateProbability: 0.001,
			ReorderProbability:   0.01,
			ReorderHold:          15 * time.Millisecond,
			Latency:              600 * time.Millisecond,
			Jitter:               80 * time.Millisecond,
			BandwidthBytesPerSec: 250_000,
		},
	},
	"flaky": {
		Name: "flaky",
		Seed: 5005,
		ToServer: DirectionConfig{
			DropProbability:      0.15,
			DuplicateProbability: 0.01,
			ReorderProbability:   0.04,
			ReorderHold:          10 * time.Millisecond,
			Latency:              60 * time.Millisecond,
			Jitter:               50 * time.Millisecond,
			BandwidthBytesPerSec: 900_000,
		},
		ToClient: DirectionConfig{
			DropProbability:      0.15,
			DuplicateProbability: 0.01,
			ReorderProbability:   0.04,
			ReorderHold:          10 * time.Millisecond,
			Latency:              60 * time.Millisecond,
			Jitter:               50 * time.Millisecond,
			BandwidthBytesPerSec: 900_000,
		},
	},
}
