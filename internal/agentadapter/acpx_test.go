package agentadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
)

// --- fake acpx helper process (no live agents) --------------------------------

const (
	acpxHelperEnvVar  = "GO_WANT_ACPX_BRIDGE_HELPER_PROCESS"
	acpxHelperModeEnv = "ACPX_BRIDGE_HELPER_MODE"
	acpxHelperOK      = "end-turn"
	acpxHelperArgFile = "ACPX_BRIDGE_HELPER_ARGFILE"

	// reviewFixturePath is a tracked regular file used as audited-path
	// material for the real snapshot discipline, mirroring the acpadapter
	// fixtures.
	reviewFixturePath = "internal/agentadapter/effective.go"
)

// TestHelperProcess is the fake acpx child for the factory-wiring contract
// tests. The parent runs this test binary as the acpx launcher; the helper
// records its argument tail when asked and emits a canned end_turn transcript
// whose message chunk text is asserted downstream.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(acpxHelperEnvVar) != "1" {
		t.Skip("helper process only")
	}
	args := os.Args
	dashdash := -1
	for i, arg := range args {
		if arg == "--" {
			dashdash = i
			break
		}
	}
	var tail []string
	if dashdash >= 0 {
		tail = args[dashdash+1:]
	}
	if argFile := os.Getenv(acpxHelperArgFile); argFile != "" {
		if err := os.WriteFile(argFile, []byte(strings.Join(tail, "\n")), 0o600); err != nil {
			os.Exit(4)
		}
	}
	switch os.Getenv(acpxHelperModeEnv) {
	case acpxHelperOK:
		fmt.Print(strings.Join([]string{
			`{"jsonrpc":"2.0","id":0,"result":{"configOptions":[{"id":"model","currentValue":"observed-model-x"},{"id":"effort","currentValue":"observed-effort-x"}]}}`,
			`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"BRIDGE PARITY OUTPUT"}}}}`,
			`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`,
			"",
		}, "\n"))
	default:
		os.Exit(3)
	}
	os.Exit(0)
}

// helperBridge builds an AcpxBridge over the fake helper child, matching the
// production composition (factory builds the inner adapter; the bridge adds
// this package's structural surface).
func helperBridge(t *testing.T, mutate func(*acpadapter.Config)) *AcpxBridge {
	t.Helper()
	cfg := acpadapter.Config{
		Launcher: []string{os.Args[0], "-test.run=^TestHelperProcess$", "--"},
		Agent:    "claude",
		Model:    "configured-model",
		Effort:   "high",
		ChildEnv: []string{
			acpxHelperEnvVar + "=1",
			acpxHelperModeEnv + "=" + acpxHelperOK,
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	inner, err := acpadapter.NewAcpx(cfg)
	if err != nil {
		t.Fatalf("helper bridge construction failed: %v", err)
	}
	return &AcpxBridge{AcpxAdapter: inner, commitLanguage: "en"}
}

// chdirToRepoRoot pins the process to the worktree root, matching production
// snapshot usage.
func chdirToRepoRoot(t *testing.T) {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("resolve worktree root: %v", err)
	}
	t.Chdir(strings.TrimSpace(string(out)))
}

func headSha(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("resolve HEAD for snapshot fixture: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// --- factory wiring -------------------------------------------------------------

// TestFactoryWiringPinsAdapterFamilies is the compatibility table: absence of
// kind builds exactly the historical *CLIAdapter; kind acpx builds an
// AcpxBridge over a constructed AcpxAdapter carrying the configured echo;
// bad declarations fail construction before any launch.
func TestFactoryWiringPinsAdapterFamilies(t *testing.T) {
	cases := []struct {
		name    string
		agents  map[string]config.AgentConfig
		select_ string
		wantErr bool
		check   func(*testing.T, AgentAdapter)
	}{
		{
			name:    "legacy entry keeps building CLIAdapter",
			agents:  map[string]config.AgentConfig{"claude": {Model: "m", ReasoningEffort: "low"}},
			select_: "claude",
			check: func(t *testing.T, ad AgentAdapter) {
				cli, ok := ad.(*CLIAdapter)
				if !ok {
					t.Fatalf("legacy agent produced %T, want *CLIAdapter unchanged", ad)
				}
				if cli.baseName() != "claude" || cli.Config.Model != "m" || cli.Timeout != 0 {
					t.Errorf("CLIAdapter = %+v, want historical construction unchanged", cli)
				}
			},
		},
		{
			name: "acpx entry builds AcpxBridge over AcpxAdapter",
			agents: map[string]config.AgentConfig{
				"claude-acpx": {Kind: config.AgentKindACPX, ACPAgent: "claude", Model: "cfg-model", ReasoningEffort: "high"},
			},
			select_: "claude-acpx",
			check: func(t *testing.T, ad AgentAdapter) {
				bridge, ok := ad.(*AcpxBridge)
				if !ok || bridge.AcpxAdapter == nil {
					t.Fatalf("acpx agent produced %T, want *AcpxBridge over a constructed adapter", ad)
				}
				id := bridge.EffectiveIdentity()
				if id.Binary != "npx:claude" {
					t.Errorf("identity.Binary = %q, want default launcher base plus agent token", id.Binary)
				}
				if id.Model != "cfg-model" || id.Effort != "high" {
					t.Errorf("identity echo = %q/%q, want configured model/effort verbatim", id.Model, id.Effort)
				}
			},
		},
		{
			name:    "missing agent token fails construction",
			agents:  map[string]config.AgentConfig{"acpx-no-token": {Kind: config.AgentKindACPX}},
			select_: "acpx-no-token",
			wantErr: true,
		},
		{
			name: "unknown enforcement fails construction",
			agents: map[string]config.AgentConfig{
				"acpx-bad": {Kind: config.AgentKindACPX, ACPAgent: "codex", Enforcement: "docker"},
			},
			select_: "acpx-bad",
			wantErr: true,
		},
		{
			name:    "unknown kind fails construction",
			agents:  map[string]config.AgentConfig{"docked": {Kind: "docker", ACPAgent: "claude"}},
			select_: "docked",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{ActiveAgent: tc.select_, Agents: tc.agents}
			ad, err := newAdapter(cfg, tc.select_, "")
			if tc.wantErr {
				if err == nil || ad != nil {
					t.Fatalf("newAdapter = %T, %v; want explicit construction error", ad, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("newAdapter returned error: %v", err)
			}
			tc.check(t, ad)
		})
	}
}

// TestProfilePathMapsReviewTimeoutToACPXRuntime verifies that the profile
// resolution path maps review.timeout onto the acpx --timeout budget exactly
// as the CLI family receives Timeout.
func TestProfilePathMapsReviewTimeoutToACPXRuntime(t *testing.T) {
	cfg := config.Config{
		ActiveAgent: "b",
		Review:      config.ReviewConfig{Timeout: 45 * time.Second},
		Agents: map[string]config.AgentConfig{
			"b": {Kind: config.AgentKindACPX, ACPAgent: "opencode", Model: "m"},
		},
	}
	ad, err := NewAdapterWithProfile(cfg, config.ResolvedProfile{Name: "normal"})
	if err != nil {
		t.Fatalf("NewAdapterWithProfile returned error: %v", err)
	}
	bridge, ok := ad.(*AcpxBridge)
	if !ok {
		t.Fatalf("profile path produced %T, want *AcpxBridge", ad)
	}
	args := bridge.AcpxAdapter.Args("probe")
	if i := slices.Index(args, "--timeout"); i < 0 || i+1 >= len(args) || args[i+1] != "45" {
		t.Errorf("args = %v, want --timeout 45 mapped from review.timeout", args)
	}
}

// TestChainMixesAdapterFamilies verifies the auto path can carry both
// families side by side, each keeping its own type.
func TestChainMixesAdapterFamilies(t *testing.T) {
	cfg := config.Config{
		ActiveAgent: "auto",
		Agents: map[string]config.AgentConfig{
			"legacy-cli":  {Model: "m", ReasoningEffort: "low"},
			"claude-acpx": {Kind: config.AgentKindACPX, ACPAgent: "claude", Model: "cfg-model"},
		},
	}
	chain, err := buildChain(cfg, []string{"legacy-cli", "claude-acpx"}, "")
	if err != nil {
		t.Fatalf("buildChain returned error: %v", err)
	}
	if len(chain.adapters) != 2 {
		t.Fatalf("chain length = %d, want 2", len(chain.adapters))
	}
	if _, ok := chain.adapters[0].(*CLIAdapter); !ok {
		t.Errorf("chain[0] = %T, want *CLIAdapter", chain.adapters[0])
	}
	if _, ok := chain.adapters[1].(*AcpxBridge); !ok {
		t.Errorf("chain[1] = %T, want *AcpxBridge", chain.adapters[1])
	}
}

// --- structural parity ------------------------------------------------------------

type restrictedReviewer interface {
	RunReview(prompt, sha string, paths []string) (string, error)
}

type contextualReviewer interface {
	ReviewWithContext(ctx context.Context, prompt, sha string, paths []string) (string, error)
}

type policyRestrictedReviewer interface {
	ReviewWithPolicy(prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error)
}

type treeProvider interface {
	OwnedTree() *process.Tree
}

// Compile-time pins for the runs-abort wiring: embedding must keep exposing
// the context-carrying prompt path and tree discovery on the bridge, so
// `sentinel runs abort` reaches the acpx child without per-call plumbing.
var (
	_ interface {
		RunPromptWithContext(context.Context, string) (string, error)
	} = (*AcpxBridge)(nil)
	_ interface{ OwnedTree() *process.Tree } = (*AcpxBridge)(nil)
)

// TestAdapterFamiliesShareStructuralContracts asserts, per contract, that a
// CLIAdapter and an acpx-built adapter satisfy the SAME structural surface,
// so callers need zero changes when an agent switches family.
func TestAdapterFamiliesShareStructuralContracts(t *testing.T) {
	factoryBridge := func(t *testing.T) any {
		t.Helper()
		ad, err := newAdapter(config.Config{
			ActiveAgent: "b",
			Agents: map[string]config.AgentConfig{
				"b": {Kind: config.AgentKindACPX, ACPAgent: "opencode"},
			},
		}, "b", "")
		if err != nil {
			t.Fatalf("bridge construction failed: %v", err)
		}
		return ad
	}
	cases := []struct {
		name string
		hold func(adapters any) bool
	}{
		{name: "commit-message surface (AgentAdapter)", hold: func(a any) bool { _, ok := a.(AgentAdapter); return ok }},
		{name: "arbitrary prompt (PromptAdapter)", hold: func(a any) bool { _, ok := a.(PromptAdapter); return ok }},
		{name: "micro-diff commit messages (AdapterWithDiff)", hold: func(a any) bool { _, ok := a.(AdapterWithDiff); return ok }},
		{name: "legacy restricted review", hold: func(a any) bool { _, ok := a.(restrictedReviewer); return ok }},
		{name: "contextual review", hold: func(a any) bool { _, ok := a.(contextualReviewer); return ok }},
		{name: "owned tree provider", hold: func(a any) bool { _, ok := a.(treeProvider); return ok }},
		{name: "effective-agent attribution", hold: func(a any) bool { _, ok := a.(ReportsEffectiveAgent); return ok }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli := &CLIAdapter{BinaryName: "echo"}
			if !tc.hold(cli) {
				t.Errorf("CLIAdapter lost the contract; the parity premise is broken")
			}
			if !tc.hold(factoryBridge(t)) {
				t.Errorf("AcpxBridge does not satisfy the shared contract")
			}
		})
	}
}

func TestSemanticReviewPolicyAdmitsCLIProvidersAndRejectsACPBridge(t *testing.T) {
	for _, binary := range []string{"opencode", "claude"} {
		if _, ok := any(&CLIAdapter{BinaryName: binary}).(policyRestrictedReviewer); !ok {
			t.Errorf("%s CLI adapter does not implement the semantic policy contract", binary)
		}
	}
	if _, ok := any(helperBridge(t, nil)).(policyRestrictedReviewer); ok {
		t.Fatal("ACP bridge implements the semantic policy contract; ACP/acpx must remain outside semantic review")
	}
}

// --- behavioral parity ---------------------------------------------------------------

// TestBridgePromptPassthroughReturnsOutputText proves the prompt shape: one
// call, the normalized assistant text back, no wrapper artifacts.
func TestBridgePromptPassthroughReturnsOutputText(t *testing.T) {
	bridge := helperBridge(t, nil)
	out, err := bridge.RunPrompt("say PROBE")
	if err != nil {
		t.Fatalf("RunPrompt returned error: %v", err)
	}
	if out != "BRIDGE PARITY OUTPUT" {
		t.Errorf("RunPrompt = %q, want normalized chunk text", out)
	}
}

// TestBridgeStringDeclaresEnforcement pins the chain-diagnostic format: the
// enforcement declaration rides in every fallback/log line so operators can
// see which containment backend was declared for the run, not only which
// binary answered.
func TestBridgeStringDeclaresEnforcement(t *testing.T) {
	bridge := helperBridge(t, nil)
	want := "acpx(" + bridge.EffectiveIdentity().Binary + "|enforcement=" + acpadapter.EnforcementNone + ")"
	if got := bridge.String(); got != want {
		t.Errorf("String() = %q, want %q (identity plus declaration)", got, want)
	}
}

// TestBridgeRunPromptWithContextRoutesOutput proves the context-carrying
// prompt shape routes the same normalized output as the legacy entry point,
// so context-preferring callers (the durable-runs controller) lose nothing.
func TestBridgeRunPromptWithContextRoutesOutput(t *testing.T) {
	bridge := helperBridge(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, err := bridge.RunPromptWithContext(ctx, "say PROBE")
	if err != nil {
		t.Fatalf("RunPromptWithContext returned error: %v", err)
	}
	if out != "BRIDGE PARITY OUTPUT" {
		t.Errorf("RunPromptWithContext = %q, want normalized chunk text", out)
	}
}

// TestBridgeRevisionRunsUnderSnapshotDiscipline proves the revision shape
// equals CLIAdapter's: the provider runs with --cwd pointed at the published
// shared snapshot for the audited SHA — a usable review snapshot carrying the
// committed audited fixture — BEFORE the agent token, and the caller-owned
// cleanup only releases the lease, so the validated snapshot remains on disk
// for reuse.
func TestBridgeRevisionRunsUnderSnapshotDiscipline(t *testing.T) {
	chdirToRepoRoot(t)
	argFile := filepath.Join(t.TempDir(), "argv.txt")
	bridge := helperBridge(t, func(cfg *acpadapter.Config) {
		cfg.ChildEnv = append(cfg.ChildEnv, acpxHelperArgFile+"="+argFile)
	})

	auditedSha := headSha(t)
	out, err := bridge.RunReview("review SNAPSHOT", auditedSha, []string{reviewFixturePath})
	if err != nil {
		t.Fatalf("RunReview returned error: %v", err)
	}
	if out != "BRIDGE PARITY OUTPUT" {
		t.Errorf("RunReview = %q, want normalized chunk text", out)
	}

	data, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatalf("helper did not record its arguments: %v", err)
	}
	got := strings.Split(string(data), "\n")
	cwdIdx := slices.Index(got, "--cwd")
	if cwdIdx < 0 {
		t.Fatalf("revision mode must pass --cwd; recorded args %v", got)
	}
	if cwdIdx+3 >= len(got) || got[cwdIdx+2] != "claude" || got[cwdIdx+3] != "exec" {
		t.Errorf("--cwd must precede the agent token; recorded args %v", got)
	}
	snapshotDir := got[cwdIdx+1]
	// --cwd is the published shared snapshot FOR THE AUDITED SHA, not the
	// live repository: its name is the SHA storage key and it lives directly
	// under the shared snapshot store root, whose directory name carries the
	// vas-sentinel-snapshots prefix on every supported platform (per-UID
	// suffixed on Linux, plain under the user's temp location on Windows).
	if base := filepath.Base(snapshotDir); base != "sha-"+auditedSha {
		t.Errorf("--cwd %q is not the published snapshot for the audited SHA %s", snapshotDir, auditedSha)
	}
	storeRoot := filepath.Dir(snapshotDir)
	if parent := filepath.Dir(storeRoot); parent != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(storeRoot), "vas-sentinel-snapshots") {
		t.Errorf("--cwd %q is not under the shared snapshot store root", snapshotDir)
	}
	// Cleanup is an idempotent lease release, never a per-call deletion: the
	// published snapshot for the SHA is retained after the turn, and the
	// stale reaper owns its removal.
	info, statErr := os.Stat(snapshotDir)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("snapshot directory %q did not survive the turn: %v", snapshotDir, statErr)
	}
	// The cwd is a usable review snapshot: it carries the committed audited
	// fixture the revision was addressed to.
	if fixture, readErr := os.ReadFile(filepath.Join(snapshotDir, reviewFixturePath)); readErr != nil || len(fixture) == 0 {
		t.Fatalf("snapshot %q does not carry the audited fixture: %v", snapshotDir, readErr)
	}
}

// TestEffectiveIdentityJSONTagsMatchAgentadapterConventions pins the identity
// wire shape: acpadapter.EffectiveAgent marshals under the exact same JSON
// keys as agentadapter.EffectiveAgent (agent/model/effort), so upper layers
// persist both families identically.
func TestEffectiveIdentityJSONTagsMatchAgentadapterConventions(t *testing.T) {
	jsonKeys := func(t *testing.T, v any) []string {
		t.Helper()
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %T: %v", v, err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal %T payload: %v", v, err)
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return keys
	}
	agentKeys := jsonKeys(t, EffectiveAgent{Binary: "b", Model: "m", Effort: "e"})
	acpxKeys := jsonKeys(t, acpadapter.EffectiveAgent{Binary: "b", Model: "m", Effort: "e"})
	if !slices.Equal(agentKeys, acpxKeys) {
		t.Fatalf("JSON keys diverge: EffectiveAgent=%v EffectiveAgent=%v, want identical agent/model/effort tags", agentKeys, acpxKeys)
	}
	if !slices.Equal(agentKeys, []string{"agent", "effort", "model"}) {
		t.Fatalf("keys = %v, want the agentadapter conventions agent/model/effort", agentKeys)
	}
}

// TestBridgeAttributionReportsOnlyWireObservedIdentity pins the producer rule
// this phase depends on: attribution reports what the provider actually
// reported, never the configured declaration. Before any turn nothing was
// observed, so model and effort stay empty even though both are configured;
// after a completed turn the model is the one the wire announced. Without
// this separation a fallback answer would be attributed to the requested
// model instead of the agent that served it.
func TestBridgeAttributionReportsOnlyWireObservedIdentity(t *testing.T) {
	bridge := helperBridge(t, nil)
	if id := bridge.EffectiveIdentity(); id.Model != "configured-model" || id.Effort != "high" {
		t.Fatalf("configured echo = %q/%q, want the configured declaration", id.Model, id.Effort)
	}

	effective, ok := bridge.EffectiveAgent()
	if !ok {
		t.Fatal("EffectiveAgent() reported no identity")
	}
	if effective.Model != "" || effective.Effort != "" {
		t.Errorf("attribution before any turn = %q/%q, want empty: nothing was observed on the wire", effective.Model, effective.Effort)
	}
	if effective.Binary == "" {
		t.Error("attribution lost the launcher-resolved binary")
	}

	if _, err := bridge.RunPrompt("say PROBE"); err != nil {
		t.Fatalf("RunPrompt returned error: %v", err)
	}

	effective, ok = bridge.EffectiveAgent()
	if !ok {
		t.Fatal("EffectiveAgent() reported no identity after a completed turn")
	}
	if effective.Model != "observed-model-x" {
		t.Errorf("observed model = %q, want the model announced on the wire", effective.Model)
	}
	if effective.Effort != "observed-effort-x" {
		t.Errorf("observed effort = %q, want the effort announced on the wire, not the configured %q", effective.Effort, "high")
	}
}
