package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

func TestLaunchLibrarySnapshotSurvivesRestartAndPinsRetry(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in := e.launchRequest("profile-launch")
	in.Library = &domain.LibrarySnapshot{ProfileID: "default", ProfileRevision: 2,
		ResolutionDigest: strings.Repeat("a", 64), ApplicationID: "work",
		ApplicationDigest: strings.Repeat("b", 64), Source: "client-declaration", Loaded: "unknown"}
	first, err := e.service.Launch(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Session.Library == nil || *first.Session.Library != *in.Library {
		t.Fatal("exact selected profile lost", first.Session.Library)
	}
	e.restart(t)
	again, err := e.service.Launch(ctx, in)
	if err != nil || again.Session.ID != first.Session.ID || *again.Session.Library != *in.Library {
		t.Fatal("restart/retry changed profile identity", again, err)
	}
	changed := *in.Library
	changed.ResolutionDigest = strings.Repeat("c", 64)
	in.Library = &changed
	_, err = e.service.Launch(ctx, in)
	assertConflict(t, err)
	if n, _ := e.backend.counts(); n != 1 {
		t.Fatal("profile retry caused new launch", n)
	}
}

func TestLibraryDeclarationCannotAssertNativeLoading(t *testing.T) {
	valid := domain.LibrarySnapshot{ProfileID: "default", ProfileRevision: 1, ResolutionDigest: strings.Repeat("a", 64), Source: "client-declaration", Loaded: "unknown"}
	if err := valid.Validate(); err != nil {
		t.Fatal("invalid positive control", err)
	}
	for _, mutate := range []func(*domain.LibrarySnapshot){
		func(s *domain.LibrarySnapshot) { s.ProfileRevision = 0 },
		func(s *domain.LibrarySnapshot) { s.Loaded = "loaded" },
		func(s *domain.LibrarySnapshot) { s.Source = "native-observed" },
		func(s *domain.LibrarySnapshot) { s.ApplicationID = "owned" },
		func(s *domain.LibrarySnapshot) { s.ResolutionDigest = "latest" },
		func(s *domain.LibrarySnapshot) { s.ProfileID = "../default" },
	} {
		e := setup(t)
		in := e.launchRequest("invalid-profile")
		snapshot := valid
		in.Library = &snapshot
		mutate(in.Library)
		if _, err := e.service.Launch(context.Background(), in); err == nil {
			t.Fatal("accepted misleading/invalid declaration")
		}
		if n, _ := e.backend.counts(); n != 0 {
			t.Fatal("invalid declaration launched runtime", n)
		}
	}
}

func TestGeminiHomeEnvironmentIsCheckedBeforeEffectWithoutBreakingCompletedRetry(t *testing.T) {
	e := setup(t)
	in := e.launchRequest("gemini-env")
	in.Provider = "gemini"
	t.Setenv("GEMINI_CLI_INTEGRATION_TEST", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("CI", "true")
	if _, err := e.service.Launch(context.Background(), in); err == nil {
		t.Fatal("headless home accepted")
	}
	if n, _ := e.backend.counts(); n != 0 {
		t.Fatal("environment refusal had runtime effect")
	}
	t.Setenv("CI", "")
	first, err := e.service.Launch(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CI", "true")
	again, err := e.service.Launch(context.Background(), in)
	if err != nil || again.Session.ID != first.Session.ID {
		t.Fatal("completed retry revalidated effect-only environment", err)
	}
	if n, _ := e.backend.counts(); n != 1 {
		t.Fatal("completed retry replayed runtime", n)
	}
}
