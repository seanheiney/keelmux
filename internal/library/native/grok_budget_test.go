package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/library"
)

var grokBudgetCases = []struct{ name, unit string }{
	{"plain", "x"}, {"quotes", "\""}, {"HTML", "<"},
	{"control", "\x01"}, {"Unicode", "é"}, {"line-separator", "\u2028"},
}

func grokBudgetRule(t *testing.T, unit string) string {
	t.Helper()
	base, _ := json.Marshal("\n<!-- pack/rule -->\n\n")
	encodedUnit, _ := json.Marshal(unit)
	remaining, cost := (32<<10)-len(base), len(encodedUnit)-2
	rule := strings.Repeat(unit, remaining/cost) + strings.Repeat("x", remaining%cost)
	text := "\n<!-- pack/rule -->\n" + rule + "\n"
	encoded, _ := json.Marshal(text)
	if len(encoded) != 32<<10 || len(text) >= 64<<10 {
		t.Fatal("fixture failed to reach exact encoded bound", len(encoded), len(text))
	}
	return rule
}

func importGrokBudgetRule(t *testing.T, reg library.Registry, version, rule string) {
	t.Helper()
	root := privateDir(t)
	b := []byte(rule)
	m := library.Manifest{Schema: 1, ID: "pack", Version: version,
		Files: map[string]string{"rule.md": hash(b)},
		Rules: []library.Asset{{ID: "rule", Path: "rule.md"}}}
	put(t, root, "rule.md", b)
	put(t, root, library.ManifestFilename, canonical(m))
	p, err := reg.Import(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reg.SelectPackage(context.Background(), "default", "pack", p.Digest, true); err != nil {
		t.Fatal(err)
	}
}

func grokBudgetRegistry(t *testing.T, rule string) (library.Registry, Target) {
	t.Helper()
	reg, err := library.Open(privateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	importGrokBudgetRule(t, reg, "1", rule)
	return reg, Target{Harness: "grok", Workspace: privateDir(t), ApplicationID: "app"}
}

func grokBudgetPreview(t *testing.T, reg library.Registry, target Target) (Plan, error) {
	t.Helper()
	r, err := reg.Resolve(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	return Preview(context.Background(), reg, r, target)
}

func TestGrokEncodedRulesBudgetAllowsExactBoundAndPreservesLiteral(t *testing.T) {
	for _, tc := range grokBudgetCases {
		t.Run(tc.name, func(t *testing.T) {
			rule := grokBudgetRule(t, tc.unit)
			reg, target := grokBudgetRegistry(t, rule)
			p, err := grokBudgetPreview(t, reg, target)
			if err != nil {
				t.Fatal(err)
			}
			noLedger(t, target)
			want := "\n<!-- pack/rule -->\n" + rule + "\n"
			if len(p.Unmapped) != 0 || len(p.LaunchArgs) != 2 || p.LaunchArgs[1] != want {
				t.Fatal("literal changed or was not mapped")
			}
			r, err := Apply(context.Background(), reg, p)
			if err != nil {
				t.Fatal(err)
			}
			s, err := Status(context.Background(), target)
			if err != nil || !s.Verified || s.Receipt.Digest != r.Digest || s.Receipt.LaunchArgs[1] != want || r.Coverage != "complete_for_declared_resolution" || r.Loaded != "unknown" {
				t.Fatal("exact bound application/status failed", s, err)
			}
		})
	}
}

func TestGrokEncodedRulesBudgetRefusesBeforeState(t *testing.T) {
	for _, tc := range grokBudgetCases {
		t.Run(tc.name, func(t *testing.T) {
			reg, target := grokBudgetRegistry(t, grokBudgetRule(t, tc.unit)+"x")
			if _, err := grokBudgetPreview(t, reg, target); !errors.Is(err, ErrUnsupported) {
				t.Fatal("oversized encoded argument accepted", err)
			}
			noLedger(t, target)
		})
	}
}

func TestGrokEncodedRulesBudgetRefusesUpdateWithoutChangingApplication(t *testing.T) {
	for _, tc := range grokBudgetCases {
		t.Run(tc.name, func(t *testing.T) {
			reg, target := grokBudgetRegistry(t, "safe existing instructions")
			p, err := grokBudgetPreview(t, reg, target)
			if err != nil {
				t.Fatal(err)
			}
			r, err := Apply(context.Background(), reg, p)
			if err != nil {
				t.Fatal(err)
			}
			before := map[string][]byte{}
			for _, path := range []string{appPath(target, "generated/rules.md"), appPath(target, "receipt.json")} {
				before[path], err = os.ReadFile(filepath.Join(target.Workspace, path))
				if err != nil {
					t.Fatal(err)
				}
			}
			importGrokBudgetRule(t, reg, "2", grokBudgetRule(t, tc.unit)+"x")
			if _, err = grokBudgetPreview(t, reg, target); !errors.Is(err, ErrUnsupported) {
				t.Fatal("oversized update preview accepted", err)
			}
			if _, err = Apply(context.Background(), reg, p); !errors.Is(err, ErrUnsupported) {
				t.Fatal("stale plan bypassed fresh encoded limit", err)
			}
			for path, want := range before {
				got, err := os.ReadFile(filepath.Join(target.Workspace, path))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatal("refused update changed owned bytes", path, err)
				}
			}
			s, err := Status(context.Background(), target)
			if err != nil || !s.Verified || s.Receipt.Digest != r.Digest {
				t.Fatal("refused update changed receipt", s, err)
			}
			if _, err = os.Lstat(filepath.Join(target.Workspace, appPath(target, "intent.json"))); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refused update created intent", err)
			}
		})
	}
}

func TestGrokEncodedRulesBudgetRejectsResealedOversizedReceipt(t *testing.T) {
	for _, tc := range grokBudgetCases {
		t.Run(tc.name, func(t *testing.T) {
			reg, target := grokBudgetRegistry(t, "safe existing instructions")
			p, err := grokBudgetPreview(t, reg, target)
			if err != nil {
				t.Fatal(err)
			}
			r, err := Apply(context.Background(), reg, p)
			if err != nil {
				t.Fatal(err)
			}
			text := "\n<!-- pack/rule -->\n" + grokBudgetRule(t, tc.unit) + "x\n"
			path := appPath(target, "generated/rules.md")
			put(t, target.Workspace, path, []byte(text))
			r.Files = []OwnedFile{{Path: path, SHA256: hash([]byte(text)), Bytes: len(text)}}
			r.LaunchArgs = []string{"--rules", text}
			r.Digest = sealReceipt(r)
			put(t, target.Workspace, appPath(target, "receipt.json"), canonical(r))
			if s, err := Status(context.Background(), target); !errors.Is(err, ErrUnsafe) || s.Verified {
				t.Fatal("resealed over-budget receipt became verified", s, err)
			}
		})
	}
}
