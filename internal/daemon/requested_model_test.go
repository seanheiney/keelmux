package daemon

import (
	"context"
	"reflect"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

func TestRequestedModelDeclarationPersistsWithoutAssertingAcceptance(t *testing.T) {
	for _, tc := range []struct{ provider, version, model string }{
		{"gemini", "0.52.0", "requested-gemini"},
		{"grok", "1.0.50 (c58f321264ba) [stable]", "requested-grok"},
		{"opencode", "1.18.21", "openrouter/vendor/model:variant"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			t.Setenv("CI", "")
			t.Setenv("GITHUB_ACTIONS", "")
			e := setup(t)
			in := e.launchRequest("declaration")
			in.Provider, in.ProviderVersion = tc.provider, tc.version
			in.Argv = []string{tc.provider, "--model", tc.model}
			e.backend.beforeLaunch = func(spec domain.LaunchSpec) {
				if spec.Session.Execution.RequestedModel.Value != tc.model {
					t.Fatal("declaration was not durable before runtime launch")
				}
			}
			first, err := e.service.Launch(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			got := first.Session.Execution
			if got.RequestedModel.Value != tc.model || got.RequestedModel.Source != "native-argv-declaration" || !got.RequestedModel.ObservedAt.Equal(first.Session.CreatedAt) {
				t.Fatal("explicit declaration attribution lost", got.RequestedModel)
			}
			if got.Provider != tc.provider || got.ActualModel != domain.Unknown() || got.Account != domain.Unknown() || got.NativeThread != domain.Unknown() {
				t.Fatal("requested model became observed acceptance, routing or identity", got)
			}
			e.restart(t)
			again, err := e.service.Launch(context.Background(), in)
			if err != nil || !reflect.DeepEqual(again.Session.Execution.RequestedModel, got.RequestedModel) {
				t.Fatal("restart/retry changed original requested evidence", again, err)
			}
			changed := in
			changed.Argv = []string{tc.provider, "--model", "other/model"}
			_, err = e.service.Launch(context.Background(), changed)
			assertConflict(t, err)
			if launches, _ := e.backend.counts(); launches != 1 {
				t.Fatal("declaration retry replayed launch", launches)
			}
		})
	}
}

func TestUnreviewedAndAbsentRequestedModelsRemainUnknown(t *testing.T) {
	for _, tc := range []struct {
		provider, version string
		args              []string
	}{
		{"opencode", "1.18.21", nil},
		{"opencode", "unreviewed", []string{"--model", "openrouter/vendor/model"}},
		{"opencode", "1.18.21", []string{"--prompt=--model=openrouter/vendor/model"}},
		{"grok", "1.0.50 (c58f321264ba) [stable]", []string{"--model", "unsafe\nvalue"}},
		{"fixture", "v1", []string{"--model", "unreviewed"}},
	} {
		e := setup(t)
		in := e.launchRequest("unknown-declaration")
		in.Provider, in.ProviderVersion = tc.provider, tc.version
		in.Argv = append([]string{tc.provider}, tc.args...)
		response, err := e.service.Launch(context.Background(), in)
		if err != nil || response.Session.Execution.RequestedModel != domain.Unknown() || response.Session.Execution.ActualModel != domain.Unknown() {
			t.Fatal("unqualified declaration asserted a model", response, err)
		}
	}
}
