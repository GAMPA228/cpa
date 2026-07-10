package thinking

import "testing"

func TestParseLevelSuffixSupportsMaxAndUltra(t *testing.T) {
	tests := map[string]ThinkingLevel{
		"max":   LevelMax,
		"MAX":   LevelMax,
		"ultra": LevelUltra,
		"ULTRA": LevelUltra,
	}

	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			got, ok := ParseLevelSuffix(input)
			if !ok {
				t.Fatalf("ParseLevelSuffix(%q) ok = false, want true", input)
			}
			if got != want {
				t.Fatalf("ParseLevelSuffix(%q) = %q, want %q", input, got, want)
			}
		})
	}
}
