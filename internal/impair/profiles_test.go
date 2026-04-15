package impair

import (
	"path/filepath"
	"reflect"
	"testing"

	"quixiot/internal/config"
)

func TestBuiltinProfilesMatchYAMLFixtures(t *testing.T) {
	cases := []string{
		"wifi-good",
		"cellular-lte",
		"cellular-3g",
		"satellite",
		"flaky",
	}

	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			builtin, ok := BuiltinProfile(name)
			if !ok {
				t.Fatalf("BuiltinProfile(%q): missing", name)
			}

			var fixture Profile
			path := filepath.Join("..", "..", "configs", "proxy-"+name+".yaml")
			if err := config.LoadFile(path, &fixture); err != nil {
				t.Fatalf("LoadFile(%s): %v", path, err)
			}

			if !reflect.DeepEqual(builtin, fixture) {
				t.Fatalf("builtin profile mismatch for %s", name)
			}
		})
	}
}
