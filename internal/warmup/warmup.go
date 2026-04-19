// Package warmup is the `drift warmup` interactive wizard: register
// circuits, create characters, print a summary. Re-runnable, non-destructive.
// All external effects go through Deps so tests can exercise the flow
// without real SSH.
package warmup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	figure "github.com/common-nighthawk/go-figure"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

type Options struct {
	SkipCircuits   bool
	SkipCharacters bool
	NoProbe        bool
	// IsTTY is decided by the Kong wrapper (os.Stdin mode check). Non-TTY
	// stdin aborts with a user_error.
	IsTTY bool
}

type ProbeResult struct {
	Version   string
	API       int
	LatencyMS int64
}

type Deps struct {
	LoadClientConfig func() (*config.Client, error)
	SaveClientConfig func(*config.Client) error

	// WriteSSHBlock: nil skips SSH integration (the --no-ssh-config case).
	WriteSSHBlock func(circuit, hostPart, userPart string) error

	Probe func(ctx context.Context, circuit string) (*ProbeResult, error)
	Call  func(ctx context.Context, circuit, method string, params, out any) error
	Now   func() time.Time
}

func Run(ctx context.Context, opts Options, deps Deps, stdin io.Reader, stdout io.Writer) error {
	if !opts.IsTTY {
		return rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"drift warmup requires a TTY on stdin (scripted equivalents: drift circuit add, drift character add, drift chest set)")
	}

	p := style.For(stdout, false)
	writeBanner(stdout, p)

	br := bufio.NewReader(stdin)
	cfg, err := deps.LoadClientConfig()
	if err != nil {
		return fmt.Errorf("load client config: %w", err)
	}
	if cfg.Circuits == nil {
		cfg.Circuits = make(map[string]config.ClientCircuit)
	}

	probes := make(map[string]*ProbeResult)

	if !opts.SkipCircuits {
		if err := runCircuitPhase(ctx, opts, deps, br, stdout, cfg, probes); err != nil {
			return err
		}
	}

	if !opts.SkipCharacters {
		if err := runCharacterPhase(ctx, deps, br, stdout, cfg); err != nil {
			return err
		}
	}

	return runSummary(ctx, opts, deps, stdout, cfg, probes)
}

// writeBanner prints the one-time go-figure "drift" banner at wizard start
// when styling is enabled (real TTY, NO_COLOR unset). Disabled palettes
// skip it so tests and piped invocations don't see ASCII art.
func writeBanner(w io.Writer, p *style.Palette) {
	if p == nil || !p.Enabled {
		return
	}
	banner := figure.NewFigure("drift", "slant", true).String()
	fmt.Fprintln(w, p.Accent(banner))
}

// sectionHeader renders `== Title ==` (plain) or a thin bordered panel
// (styled). Plain form is a single line the existing tests assert against.
func sectionHeader(w io.Writer, p *style.Palette, title string) {
	if p == nil || !p.Enabled {
		fmt.Fprintln(w, "")
		fmt.Fprintf(w, "== %s ==\n", title)
		return
	}
	panel := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(0, 1).
		Bold(true).
		Render(title)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, panel)
}

func runCircuitPhase(ctx context.Context, opts Options, deps Deps, br *bufio.Reader, w io.Writer, cfg *config.Client, probes map[string]*ProbeResult) error {
	sectionHeader(w, style.For(w, false), "Circuits")
	if len(cfg.Circuits) > 0 {
		fmt.Fprintln(w, "already configured:")
		names := sortedKeys(cfg.Circuits)
		for _, n := range names {
			def := ""
			if n == cfg.DefaultCircuit {
				def = " (default)"
			}
			fmt.Fprintf(w, "  - %s → %s%s\n", n, cfg.Circuits[n].Host, def)
		}
	} else {
		fmt.Fprintln(w, "(none yet)")
	}

	for {
		more, err := promptYesNo(br, w, "Add a circuit?", len(cfg.Circuits) == 0)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
		if err := addOneCircuit(ctx, opts, deps, br, w, cfg, probes); err != nil {
			// Surface per-circuit errors inline; the loop continues so one
			// bad entry doesn't abort the whole wizard.
			fmt.Fprintf(w, "  error: %v\n", err)
		}
	}
}

func addOneCircuit(ctx context.Context, opts Options, deps Deps, br *bufio.Reader, w io.Writer, cfg *config.Client, probes map[string]*ProbeResult) error {
	circuitName, err := promptNonEmpty(br, w, "  circuit name: ")
	if err != nil {
		return err
	}
	if err := name.Validate("circuit", circuitName); err != nil {
		return err
	}
	host, err := promptNonEmpty(br, w, "  SSH target (user@host[:port]): ")
	if err != nil {
		return err
	}
	userPart, hostPart, err := name.SplitUserHost(host)
	if err != nil {
		return err
	}

	def := cfg.DefaultCircuit == ""
	if cfg.DefaultCircuit != "" && cfg.DefaultCircuit != circuitName {
		def, err = promptYesNo(br, w, "  set as default circuit?", false)
		if err != nil {
			return err
		}
	}

	cfg.Circuits[circuitName] = config.ClientCircuit{Host: host}
	if def || cfg.DefaultCircuit == "" {
		cfg.DefaultCircuit = circuitName
	}
	if err := deps.SaveClientConfig(cfg); err != nil {
		return err
	}
	if deps.WriteSSHBlock != nil && cfg.ManagesSSHConfig() {
		if err := deps.WriteSSHBlock(circuitName, hostPart, userPart); err != nil {
			return err
		}
		fmt.Fprintf(w, "  wrote SSH config block drift.%s\n", circuitName)
	}

	if !opts.NoProbe && deps.Probe != nil {
		pr, err := deps.Probe(ctx, circuitName)
		if err != nil {
			fmt.Fprintf(w, "  probe failed: %v\n", err)
			printInstallHints(w, circuitName)
			return nil
		}
		probes[circuitName] = pr
		fmt.Fprintf(w, "  probe ok — lakitu %s (api %d, %dms)\n", pr.Version, pr.API, pr.LatencyMS)

		// Deeper one-shot check with live devpod version info. Scoped to
		// setup rather than every RPC — kart lifecycle stays on the cheap
		// server.version probe.
		if deps.Call != nil {
			var vr struct {
				DevpodActual   string `json:"devpod_actual"`
				DevpodExpected string `json:"devpod_expected"`
				DevpodMatch    bool   `json:"devpod_match"`
				DevpodError    string `json:"devpod_error"`
			}
			if err := deps.Call(ctx, circuitName, wire.MethodServerVerify, struct{}{}, &vr); err != nil {
				fmt.Fprintf(w, "  devpod probe skipped: %v\n", err)
			} else {
				switch {
				case vr.DevpodError != "":
					fmt.Fprintf(w, "  devpod unreachable on circuit: %s\n", vr.DevpodError)
				case vr.DevpodExpected == "":
					fmt.Fprintf(w, "  devpod: %s (lakitu has no pin — dev build)\n", vr.DevpodActual)
				case vr.DevpodMatch:
					fmt.Fprintf(w, "  devpod: %s (matches pin)\n", vr.DevpodActual)
				default:
					fmt.Fprintf(w, "  devpod: %s — WARNING: lakitu expects %s\n",
						vr.DevpodActual, vr.DevpodExpected)
				}
			}
		}
	}
	return nil
}

func runCharacterPhase(ctx context.Context, deps Deps, br *bufio.Reader, w io.Writer, cfg *config.Client) error {
	sectionHeader(w, style.For(w, false), "Characters")
	if len(cfg.Circuits) == 0 {
		fmt.Fprintln(w, "no circuits configured; skipping characters")
		return nil
	}

	for {
		more, err := promptYesNo(br, w, "Add a character?", false)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
		if err := addOneCharacter(ctx, deps, br, w, cfg); err != nil {
			fmt.Fprintf(w, "  error: %v\n", err)
		}
	}
}

func addOneCharacter(ctx context.Context, deps Deps, br *bufio.Reader, w io.Writer, cfg *config.Client) error {
	circuit, err := pickCircuit(br, w, cfg)
	if err != nil {
		return err
	}

	charName, err := promptNonEmpty(br, w, "  character name: ")
	if err != nil {
		return err
	}
	if err := name.Validate("character", charName); err != nil {
		return err
	}
	gitName, err := promptNonEmpty(br, w, "  git name: ")
	if err != nil {
		return err
	}
	gitEmail, err := promptNonEmpty(br, w, "  git email: ")
	if err != nil {
		return err
	}
	githubUser, err := promptLine(br, w, "  github user (optional): ")
	if err != nil {
		return err
	}
	sshKeyPath, err := promptLine(br, w, "  ssh key path (optional): ")
	if err != nil {
		return err
	}

	params := map[string]any{
		"name":      charName,
		"git_name":  gitName,
		"git_email": gitEmail,
	}
	if githubUser != "" {
		params["github_user"] = githubUser
	}
	if sshKeyPath != "" {
		params["ssh_key_path"] = sshKeyPath
	}

	stagePAT, err := promptYesNo(br, w, "  stage a PAT via chest.set?", false)
	if err != nil {
		return err
	}
	if stagePAT {
		patValue, err := promptNonEmpty(br, w, "  PAT value (will be sent to chest.set): ")
		if err != nil {
			return err
		}
		chestName := charName + "-pat"
		if err := deps.Call(ctx, circuit, wire.MethodChestSet, map[string]any{
			"name":  chestName,
			"value": patValue,
		}, nil); err != nil {
			return fmt.Errorf("chest.set: %w", err)
		}
		params["pat_secret"] = "chest:" + chestName
	}

	if err := deps.Call(ctx, circuit, wire.MethodCharacterAdd, params, nil); err != nil {
		return fmt.Errorf("character.add: %w", err)
	}
	fmt.Fprintf(w, "  added character %q on %s\n", charName, circuit)

	setDefault, err := promptYesNo(br, w, "  set as circuit's default_character?", false)
	if err != nil {
		return err
	}
	if setDefault {
		if err := deps.Call(ctx, circuit, wire.MethodConfigSet, map[string]any{
			"key":   "default_character",
			"value": charName,
		}, nil); err != nil {
			return fmt.Errorf("config.set: %w", err)
		}
		fmt.Fprintf(w, "  set %s as default character on %s\n", charName, circuit)
	}
	return nil
}

func pickCircuit(br *bufio.Reader, w io.Writer, cfg *config.Client) (string, error) {
	names := sortedKeys(cfg.Circuits)
	if len(names) == 1 {
		return names[0], nil
	}
	fmt.Fprintln(w, "  circuits:")
	for i, n := range names {
		fmt.Fprintf(w, "    [%d] %s\n", i+1, n)
	}
	for {
		line, err := promptLine(br, w, "  pick circuit (number or name): ")
		if err != nil {
			return "", err
		}
		if line == "" {
			continue
		}
		for i, n := range names {
			if line == n || line == fmt.Sprintf("%d", i+1) {
				return n, nil
			}
		}
		fmt.Fprintln(w, "  no such circuit, try again")
	}
}

func runSummary(ctx context.Context, opts Options, deps Deps, w io.Writer, cfg *config.Client, probes map[string]*ProbeResult) error {
	sectionHeader(w, style.For(w, false), "Summary")
	names := sortedKeys(cfg.Circuits)
	if len(names) == 0 {
		fmt.Fprintln(w, "no circuits configured")
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "next: drift circuit add <name> --host user@host")
		return nil
	}
	for _, n := range names {
		pr := probes[n]
		if pr == nil && !opts.NoProbe && deps.Probe != nil {
			// Catches the case where the user skipped the probe earlier but
			// NoProbe is still off at the wizard level.
			if got, err := deps.Probe(ctx, n); err == nil {
				pr = got
				probes[n] = got
			}
		}
		def := ""
		if n == cfg.DefaultCircuit {
			def = " (default)"
		}
		fmt.Fprintf(w, "  circuit %s → %s%s\n", n, cfg.Circuits[n].Host, def)
		if pr != nil {
			fmt.Fprintf(w, "    lakitu %s (api %d, %dms)\n", pr.Version, pr.API, pr.LatencyMS)
		}
		listCharactersFor(ctx, deps, w, n)
	}

	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "next: drift new <name> --clone <git-url>")
	return nil
}

// listCharactersFor fetches characters and prints them. Failure is
// non-fatal — the summary should render even if a probe failed.
func listCharactersFor(ctx context.Context, deps Deps, w io.Writer, circuit string) {
	if deps.Call == nil {
		return
	}
	var out struct {
		Characters []struct {
			Name string `json:"name"`
		} `json:"characters"`
	}
	if err := deps.Call(ctx, circuit, wire.MethodCharacterList, nil, &out); err != nil {
		return
	}
	if len(out.Characters) == 0 {
		return
	}
	fmt.Fprint(w, "    characters:")
	for _, c := range out.Characters {
		fmt.Fprintf(w, " %s", c.Name)
	}
	fmt.Fprintln(w)
}

func printInstallHints(w io.Writer, circuit string) {
	fmt.Fprintf(w, "  lakitu may not be installed (or may be the wrong version) on %q.\n", circuit)
	fmt.Fprintln(w, "  install via the Nix module or the manual tarball — see the README for bootstrap instructions.")
	fmt.Fprintln(w, "  re-run `drift warmup` after installing to re-probe.")
}

// promptLine reads one line. EOF before any input surfaces io.EOF so the
// caller can unwind cleanly.
func promptLine(br *bufio.Reader, w io.Writer, prompt string) (string, error) {
	fmt.Fprint(w, prompt)
	line, err := br.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if line == "" && errors.Is(err, io.EOF) {
		return "", io.EOF
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func promptNonEmpty(br *bufio.Reader, w io.Writer, prompt string) (string, error) {
	for {
		s, err := promptLine(br, w, prompt)
		if err != nil {
			return "", err
		}
		s = strings.TrimSpace(s)
		if s != "" {
			return s, nil
		}
		fmt.Fprintln(w, "  (required)")
	}
}

func promptYesNo(br *bufio.Reader, w io.Writer, prompt string, dflt bool) (bool, error) {
	suffix := " [y/N]: "
	if dflt {
		suffix = " [Y/n]: "
	}
	for {
		s, err := promptLine(br, w, prompt+suffix)
		if err != nil {
			return false, err
		}
		s = strings.ToLower(strings.TrimSpace(s))
		switch s {
		case "":
			return dflt, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

func sortedKeys(m map[string]config.ClientCircuit) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
