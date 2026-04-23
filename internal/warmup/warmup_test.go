package warmup

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// newFakeDeps builds a Deps whose config is held in memory and whose RPC
// calls are recorded. The caller mutates probeInfos / probeErrs before
// Run is invoked. probeInfos is keyed by the full SSH target string
// (["alice@host"] joined by spaces) so tests can route different user@host
// inputs to different canned responses.
type fakeState struct {
	cfg *config.Client

	// probeInfos: result keyed by the SSH target string (e.g. "alice@lab.example").
	probeInfos     map[string]*wire.ServerInfo
	probeInfoErrs  map[string]error
	probeInfoCalls []string

	probes     map[string]*ProbeResult
	probeErrs  map[string]error
	probeCalls []string

	sshBlocks [][3]string // (circuit, host, user)

	calls []rpcCall
	// callRoutes lets tests supply canned responses per method.
	callRoutes map[string]callRoute

	// mu serializes the record-keeping slice appends. runSummary fans out
	// per-circuit calls across goroutines, so Probe/Call callbacks race on
	// probeCalls/calls without this guard (-race in CI catches it).
	mu sync.Mutex
}

type rpcCall struct {
	circuit string
	method  string
	params  any
}

type callRoute struct {
	out any
	err error
}

func (s *fakeState) deps() Deps {
	return Deps{
		LoadClientConfig: func() (*config.Client, error) {
			return s.cfg, nil
		},
		SaveClientConfig: func(c *config.Client) error {
			s.cfg = c
			return nil
		},
		WriteSSHBlock: func(circuit, host, user string) error {
			s.sshBlocks = append(s.sshBlocks, [3]string{circuit, host, user})
			return nil
		},
		Probe: func(_ context.Context, circuit string) (*ProbeResult, error) {
			s.mu.Lock()
			s.probeCalls = append(s.probeCalls, circuit)
			s.mu.Unlock()
			if err, ok := s.probeErrs[circuit]; ok {
				return nil, err
			}
			if pr, ok := s.probes[circuit]; ok {
				return pr, nil
			}
			return &ProbeResult{Version: "v0.1.0", API: 1, LatencyMS: 7}, nil
		},
		ProbeInfo: func(_ context.Context, sshArgs []string) (*wire.ServerInfo, error) {
			target := strings.Join(sshArgs, " ")
			s.probeInfoCalls = append(s.probeInfoCalls, target)
			if err, ok := s.probeInfoErrs[target]; ok {
				return nil, err
			}
			if info, ok := s.probeInfos[target]; ok {
				return info, nil
			}
			// Default response — derive a name from the first SSH dest part
			// after the '@' so tests that don't pre-register still work.
			host := target
			if i := strings.LastIndex(host, "@"); i >= 0 {
				host = host[i+1:]
			}
			if i := strings.IndexByte(host, ':'); i >= 0 {
				host = host[:i]
			}
			if i := strings.IndexByte(host, '.'); i >= 0 {
				host = host[:i]
			}
			if host == "" || host == " " {
				host = "lab"
			}
			return &wire.ServerInfo{Name: host, Version: "v0.1.0", API: 1}, nil
		},
		Call: func(_ context.Context, circuit, method string, params, out any) error {
			s.mu.Lock()
			s.calls = append(s.calls, rpcCall{circuit, method, params})
			s.mu.Unlock()
			if r, ok := s.callRoutes[method]; ok {
				// Let tests populate character.list output via route.out.
				if out != nil && r.out != nil {
					// Reflect-free shallow copy: tests pass the same pointer
					// type the production code hands in, so we just reassign
					// via pointer-to-pointer when possible. Simpler: tests
					// exercising `out` use the character.list path and pass
					// a *struct; we encode/decode via a small helper below.
					copyJSONLike(out, r.out)
				}
				return r.err
			}
			return nil
		},
		Now: func() time.Time { return time.Unix(0, 0) },
	}
}

// copyJSONLike copies src into dst when both are pointers to the same type,
// used only by the character.list test path.
func copyJSONLike(dst, src any) {
	type list struct {
		Characters []struct {
			Name string `json:"name"`
		} `json:"characters"`
	}
	d, ok1 := dst.(*struct {
		Characters []struct {
			Name string `json:"name"`
		} `json:"characters"`
	})
	s, ok2 := src.(list)
	if ok1 && ok2 {
		d.Characters = s.Characters
	}
}

func TestRun_NonTTY_ReturnsUserError(t *testing.T) {
	s := &fakeState{cfg: &config.Client{}}
	err := Run(context.Background(), Options{IsTTY: false}, s.deps(), strings.NewReader(""), &bytes.Buffer{})
	var re *rpcerr.Error
	if !errors.As(err, &re) {
		t.Fatalf("want *rpcerr.Error, got %T: %v", err, err)
	}
	if re.Code != rpcerr.CodeUserError {
		t.Fatalf("want code %d, got %d", rpcerr.CodeUserError, re.Code)
	}
	if !strings.Contains(re.Message, "TTY") {
		t.Fatalf("error message should mention TTY: %q", re.Message)
	}
}

func TestRun_FirstRun_CircuitOnlySkipsCharacters(t *testing.T) {
	s := &fakeState{
		cfg: &config.Client{},
		probeInfos: map[string]*wire.ServerInfo{
			"alice@lab.example": {Name: "lab", Version: "v0.1.0", API: 1},
		},
	}
	in := strings.NewReader(strings.Join([]string{
		"y",                 // add circuit?
		"alice@lab.example", // ssh target — no more name prompt; server advertises it
		"n",                 // add another circuit?
	}, "\n") + "\n")
	var out bytes.Buffer
	err := Run(context.Background(), Options{IsTTY: true, SkipCharacters: true}, s.deps(), in, &out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := s.cfg.Circuits["lab"]; !ok {
		t.Fatalf("circuit lab not saved; cfg=%+v", s.cfg)
	}
	if s.cfg.DefaultCircuit != "lab" {
		t.Fatalf("want default=lab, got %q", s.cfg.DefaultCircuit)
	}
	if len(s.sshBlocks) != 1 || s.sshBlocks[0] != [3]string{"lab", "lab.example", "alice"} {
		t.Fatalf("ssh blocks = %v", s.sshBlocks)
	}
	if len(s.probeInfoCalls) != 1 || s.probeInfoCalls[0] != "alice@lab.example" {
		t.Fatalf("probeInfo calls = %v", s.probeInfoCalls)
	}
	outStr := out.String()
	for _, want := range []string{"== Circuits ==", "probe ok", "== Summary ==", "next: drift new"} {
		if !strings.Contains(outStr, want) {
			t.Errorf("output missing %q:\n%s", want, outStr)
		}
	}
}

func TestRun_ProbeFailure_PrintsInstallHints(t *testing.T) {
	s := &fakeState{
		cfg:           &config.Client{},
		probeInfoErrs: map[string]error{"alice@lab.example": errors.New("ssh: no route")},
	}
	in := strings.NewReader(strings.Join([]string{
		"y", "alice@lab.example",
		"n",
	}, "\n") + "\n")
	var out bytes.Buffer
	err := Run(context.Background(), Options{IsTTY: true, SkipCharacters: true}, s.deps(), in, &out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	outStr := out.String()
	for _, want := range []string{"probe failed", "install", "bootstrap"} {
		if !strings.Contains(outStr, want) {
			t.Errorf("output missing %q:\n%s", want, outStr)
		}
	}
}

func TestRun_NoProbe_StillRunsIdentityProbe(t *testing.T) {
	// --no-probe skips the DEEPER server.verify devpod check but NOT the
	// identity probe — we can't write SSH blocks without knowing the
	// server's canonical name.
	s := &fakeState{
		cfg: &config.Client{},
		probeInfos: map[string]*wire.ServerInfo{
			"alice@lab.example": {Name: "lab", Version: "v0.1.0", API: 1},
		},
	}
	in := strings.NewReader(strings.Join([]string{
		"y", "alice@lab.example",
		"n",
	}, "\n") + "\n")
	var out bytes.Buffer
	err := Run(context.Background(), Options{IsTTY: true, NoProbe: true, SkipCharacters: true}, s.deps(), in, &out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// server.verify must not be called under --no-probe.
	for _, c := range s.calls {
		if c.method == "server.verify" {
			t.Errorf("expected no server.verify calls, got %+v", s.calls)
		}
	}
}

func TestRun_SkipCircuits_GoesStraightToCharacters(t *testing.T) {
	s := &fakeState{
		cfg: &config.Client{
			DefaultCircuit: "lab",
			Circuits: map[string]config.ClientCircuit{
				"lab": {Host: "alice@lab.example"},
			},
		},
	}
	in := strings.NewReader(strings.Join([]string{
		"y",                 // add a character?
		"me",                // character name
		"Alice",             // git name
		"alice@example.com", // git email
		"",                  // github user (optional)
		"",                  // ssh key path (optional)
		"n",                 // stage PAT?
		"y",                 // set as default?
		"n",                 // add another?
	}, "\n") + "\n")
	var out bytes.Buffer
	err := Run(context.Background(), Options{IsTTY: true, SkipCircuits: true, NoProbe: true}, s.deps(), in, &out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var sawAdd, sawConfigSet bool
	for _, c := range s.calls {
		switch c.method {
		case "character.new":
			sawAdd = true
			if p, ok := c.params.(map[string]any); !ok || p["name"] != "me" || p["git_email"] != "alice@example.com" {
				t.Errorf("character.new params wrong: %+v", c.params)
			}
		case "config.set":
			sawConfigSet = true
		}
	}
	if !sawAdd {
		t.Errorf("character.new not called; calls=%+v", s.calls)
	}
	if !sawConfigSet {
		t.Errorf("config.set not called; calls=%+v", s.calls)
	}
}

func TestRun_CharacterPATStagesChestSet(t *testing.T) {
	s := &fakeState{
		cfg: &config.Client{
			DefaultCircuit: "lab",
			Circuits:       map[string]config.ClientCircuit{"lab": {Host: "alice@lab"}},
		},
	}
	in := strings.NewReader(strings.Join([]string{
		"y",
		"me", "Alice", "alice@example.com", "", "",
		"y",         // stage PAT
		"gh_abcdef", // PAT value
		"n",         // set as default
		"n",         // add another
	}, "\n") + "\n")
	var out bytes.Buffer
	err := Run(context.Background(), Options{IsTTY: true, SkipCircuits: true, NoProbe: true}, s.deps(), in, &out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var chestCall, addCall *rpcCall
	for i := range s.calls {
		switch s.calls[i].method {
		case "chest.new":
			chestCall = &s.calls[i]
		case "character.new":
			addCall = &s.calls[i]
		}
	}
	if chestCall == nil {
		t.Fatalf("chest.new not called")
	}
	cp := chestCall.params.(map[string]any)
	if cp["name"] != "me-pat" || cp["value"] != "gh_abcdef" {
		t.Errorf("chest.new params: %+v", cp)
	}
	if addCall == nil {
		t.Fatalf("character.new not called")
	}
	ap := addCall.params.(map[string]any)
	if ap["pat_secret"] != "chest:me-pat" {
		t.Errorf("want pat_secret=chest:me-pat, got %v", ap["pat_secret"])
	}
}

func TestRun_ServerReturnsInvalidName_Aborts(t *testing.T) {
	// Name shape is enforced server-side in production, but the client
	// still defends so a misconfigured server can't inject garbage into
	// ~/.config/drift/ssh_config.
	s := &fakeState{
		cfg: &config.Client{},
		probeInfos: map[string]*wire.ServerInfo{
			"alice@lab": {Name: "Has Spaces", Version: "v0.1.0", API: 1},
		},
	}
	in := strings.NewReader(strings.Join([]string{
		"y", "alice@lab",
		"n",
	}, "\n") + "\n")
	var out bytes.Buffer
	err := Run(context.Background(), Options{IsTTY: true, SkipCharacters: true}, s.deps(), in, &out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(s.cfg.Circuits) != 0 {
		t.Errorf("invalid server-reported name should not persist: %+v", s.cfg.Circuits)
	}
	if !strings.Contains(out.String(), "invalid circuit name") {
		t.Errorf("expected invalid-name message, got:\n%s", out.String())
	}
}

func TestRun_SkipsCharacterPhase_WhenServerHasDefault(t *testing.T) {
	s := &fakeState{
		cfg: &config.Client{},
		probeInfos: map[string]*wire.ServerInfo{
			"alice@lab.example": {Name: "lab", Version: "v0.1.0", API: 1, DefaultCharacter: "kurisu"},
		},
	}
	in := strings.NewReader(strings.Join([]string{
		"y", "alice@lab.example",
		"n", // add another circuit
		// Character phase should auto-skip and not read further input.
	}, "\n") + "\n")
	var out bytes.Buffer
	err := Run(context.Background(), Options{IsTTY: true}, s.deps(), in, &out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "already have a default_character") {
		t.Errorf("expected skip message, got:\n%s", out.String())
	}
	// No character.new call should have been made.
	for _, c := range s.calls {
		if c.method == "character.new" {
			t.Errorf("character.new called but a default was already set: %+v", s.calls)
		}
	}
}
