package providers

import (
	"reflect"
	"strings"
	"testing"
)

var declarationVersions = map[string]string{
	"gemini":   "0.52.0",
	"grok":     "1.0.50 (c58f321264ba) [stable]",
	"opencode": "1.18.21",
}

func TestRequestedModelPreservesReviewedExplicitSelectors(t *testing.T) {
	for provider, version := range declarationVersions {
		model := "literal model é中 $value `quoted`"
		if provider == "opencode" {
			model = "openrouter/vendor/model.v2:variant"
		}
		for _, args := range [][]string{{"-m", model}, {"--model", model}, {"--model=" + model}} {
			t.Run(provider+"/"+args[0], func(t *testing.T) {
				before := append([]string(nil), args...)
				got, err := RequestedModel(provider, version, args)
				if err != nil || got != model {
					t.Fatalf("declaration = %q, %v; want exact %q", got, err, model)
				}
				if !reflect.DeepEqual(args, before) {
					t.Fatal("declaration parsing mutated argv")
				}
			})
		}
	}
}

func TestRequestedModelConsumesOtherOptionsAndLiteralBoundaries(t *testing.T) {
	cases := []struct {
		provider string
		args     []string
		want     string
	}{
		{"gemini", nil, ""},
		{"grok", []string{}, ""},
		{"opencode", []string{"--pure"}, ""},
		{"gemini", []string{"--prompt-interactive=--model=shadow", "--policy", "model-file", "--resume=--model=shadow"}, ""},
		{"grok", []string{"--rules=--model=shadow", "--agent", "model-file", "--", "--model=shadow"}, ""},
		{"grok", []string{"please explain --model shadow"}, ""},
		{"opencode", []string{"--prompt=--model=shadow", "--agent", "model-file"}, ""},
		{"gemini", []string{"-i", "please explain --model shadow", "--resume", "--model=actual", "--screen-reader"}, "actual"},
		{"grok", []string{"--rules=--model=shadow", "--resume", "--model=actual", "--", "hello"}, "actual"},
		{"opencode", []string{"--prompt", "please explain --model shadow", "--model=openrouter/vendor/actual", "--pure"}, "openrouter/vendor/actual"},
	}
	for i, tc := range cases {
		if err := expandedContract(tc.provider).validate(tc.args); err != nil {
			t.Fatalf("case %d is outside the existing native grammar: %v", i, err)
		}
		before := append([]string(nil), tc.args...)
		got, err := RequestedModel(tc.provider, declarationVersions[tc.provider], tc.args)
		if err != nil || got != tc.want {
			t.Fatalf("case %d declaration = %q, %v; want %q", i, got, err, tc.want)
		}
		if len(tc.args) != len(before) || len(tc.args) > 0 && !reflect.DeepEqual(tc.args, before) {
			t.Fatalf("case %d mutated argv", i)
		}
	}
}

func TestRequestedModelRejectsUnreviewedProviderAndVersion(t *testing.T) {
	for _, tc := range []struct{ provider, version string }{
		{"codex", "0.159.0"}, {"claude", "2.1.296"}, {"openrouter", "1.18.21"}, {"unknown", "1"},
		{"gemini", "0.53.0"}, {"gemini", "0.52.0-preview.1"}, {"gemini", ""},
		{"grok", "grok 1.0.50 (c58f321264ba) [stable]"}, {"grok", "1.0.50 (aaaaaaaaaaaa) [stable]"},
		{"grok", "1.0.50 (c58f321264ba) [alpha]"}, {"opencode", "1.18.22"},
	} {
		if got, err := RequestedModel(tc.provider, tc.version, []string{"--model", "example/model"}); err == nil || got != "" {
			t.Fatalf("unreviewed %q/%q yielded %q, %v", tc.provider, tc.version, got, err)
		}
	}
}

func TestRequestedModelUsesWholeNativeGrammarAndReturnsNoPartialValue(t *testing.T) {
	for provider, version := range declarationVersions {
		model := "model"
		if provider == "opencode" {
			model = "openrouter/model"
		}
		for _, args := range [][]string{
			{"--model"}, {"--model="}, {"--model", "--pure"}, {"-m=" + model}, {"-m" + model},
			{"--model", model, "--model=" + model}, {"--model=" + model, "-m", model},
			{"--model=" + model, "--unknown"}, {"--model=" + model, "login"},
			{"--model=" + model, "--", "login"}, {"--model", "private\x00model"},
		} {
			if err := expandedContract(provider).validate(args); err == nil {
				t.Fatalf("expected existing grammar refusal: %s %q", provider, args)
			}
			if got, err := RequestedModel(provider, version, args); err == nil || got != "" {
				t.Fatalf("invalid %s argv yielded partial %q, %v", provider, got, err)
			}
		}
	}
	for _, model := range []string{"model-only", "openrouter/", "openrouter//model", "openrouter/../model", "openrouter/a b", "openrouter/$(command)"} {
		if got, err := RequestedModel("opencode", declarationVersions["opencode"], []string{"--model", model}); err == nil || got != "" {
			t.Fatalf("invalid OpenCode route yielded %q, %v", got, err)
		}
	}
}

func TestRequestedModelDisplayLimitsDoNotNarrowNativeValidation(t *testing.T) {
	for _, provider := range []string{"gemini", "grok"} {
		for _, model := range []string{strings.Repeat("a", 256), strings.Repeat("é", 128)} {
			got, err := RequestedModel(provider, declarationVersions[provider], []string{"--model", model})
			if err != nil || got != model {
				t.Fatalf("256-byte declaration refused: %s %q, %v", provider, got, err)
			}
		}
		for _, model := range []string{
			strings.Repeat("a", 257), strings.Repeat("é", 129), "PRIVATE\xffVALUE",
			"PRIVATE\nVALUE", "PRIVATE\rVALUE", "PRIVATE\tVALUE", "PRIVATE\x1bVALUE", "PRIVATE\x7fVALUE",
			"PRIVATE\u0085VALUE", "PRIVATE\u202eVALUE", "PRIVATE\u200bVALUE",
		} {
			args := []string{"--model", model}
			if err := expandedContract(provider).validate(args); err != nil {
				t.Fatalf("display-only limit changed native validation for %s: %v", provider, err)
			}
			got, err := RequestedModel(provider, declarationVersions[provider], args)
			if err == nil || got != "" || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("unsafe display declaration leaked or passed: %s %q, %v", provider, got, err)
			}
		}
	}
	for _, model := range []string{"openrouter/" + strings.Repeat("a", 245), "openrouter/" + strings.Repeat("a", 246)} {
		args := []string{"--model", model}
		if err := expandedContract("opencode").validate(args); err != nil {
			t.Fatal("display length narrowed native route validation", err)
		}
		got, err := RequestedModel("opencode", declarationVersions["opencode"], args)
		if len(model) == 256 && (err != nil || got != model) || len(model) > 256 && (err == nil || got != "") {
			t.Fatalf("OpenCode display boundary length=%d: %q, %v", len(model), got, err)
		}
	}
}
