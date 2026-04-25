// Package sshconf manages drift's slice of the user's SSH setup: the
// drift-owned `~/.config/drift/ssh_config` file (written in full) and a
// single `Include` line prepended to `~/.ssh/config`. Nothing else in the
// user's SSH setup is touched.
//
// All filesystem paths are passed in explicitly — the package never reads $HOME.
package sshconf

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/name"
)

// IncludeDirective must match byte-for-byte so [EnsureInclude] can detect
// an already-present include and avoid duplicate writes.
const IncludeDirective = "Include ~/.config/drift/ssh_config"

// WildcardHost only matches `drift.<circuit>.<kart>`; OpenSSH's first-match
// rule leaves literal `Host drift.<circuit>` blocks winning for bare aliases.
const WildcardHost = "drift.*.*"

type Options struct {
	// Manage: when false, every mutating call on Manager becomes a no-op.
	Manage bool
}

type Paths struct {
	UserSSHConfig    string
	ManagedSSHConfig string
	SocketsDir       string
}

type Manager struct {
	Paths   Paths
	Options Options
}

func New(paths Paths, opts Options) *Manager {
	return &Manager{Paths: paths, Options: opts}
}

type HostBlock struct {
	Name string
	Body []string
}

// managedFile.Header holds any comment/blank lines preceding the first Host
// block. The parser preserves them so a hand-edited banner survives, even
// though the canonical file has none.
type managedFile struct {
	Header []string
	Blocks []HostBlock
}

func parseManaged(path string) (*managedFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &managedFile{}, nil
		}
		return nil, fmt.Errorf("sshconf: read %s: %w", path, err)
	}
	return parseManagedBytes(data), nil
}

func parseManagedBytes(data []byte) *managedFile {
	mf := &managedFile{}
	var cur *HostBlock
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if keyword, rest, ok := splitDirective(trimmed); ok && strings.EqualFold(keyword, "Host") {
			if cur != nil {
				mf.Blocks = append(mf.Blocks, *cur)
			}
			cur = &HostBlock{Name: rest}
			continue
		}
		if cur == nil {
			mf.Header = append(mf.Header, line)
			continue
		}
		cur.Body = append(cur.Body, line)
	}
	if cur != nil {
		mf.Blocks = append(mf.Blocks, *cur)
	}
	// Strip trailing blank lines from each block body so re-serialization is
	// stable across rewrites.
	for i := range mf.Blocks {
		mf.Blocks[i].Body = trimTrailingBlank(mf.Blocks[i].Body)
	}
	mf.Header = trimTrailingBlank(mf.Header)
	return mf
}

// splitDirective accepts both `Keyword value` and `Keyword=value` forms.
func splitDirective(line string) (keyword, rest string, ok bool) {
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	idx := strings.IndexAny(line, " \t=")
	if idx < 0 {
		return line, "", true
	}
	keyword = line[:idx]
	rest = strings.TrimLeft(line[idx:], " \t=")
	return keyword, rest, true
}

func trimTrailingBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// bytes serializes with a blank line between blocks and no trailing blank
// line, so repeated round-trips are byte-stable.
func (mf *managedFile) bytes() []byte {
	var buf bytes.Buffer
	for _, h := range mf.Header {
		buf.WriteString(h)
		buf.WriteByte('\n')
	}
	if len(mf.Header) > 0 && len(mf.Blocks) > 0 {
		buf.WriteByte('\n')
	}
	for i, b := range mf.Blocks {
		if i > 0 {
			buf.WriteByte('\n')
		}
		fmt.Fprintf(&buf, "Host %s\n", b.Name)
		for _, line := range b.Body {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
}

func (mf *managedFile) upsertBlock(b HostBlock) {
	for i := range mf.Blocks {
		if mf.Blocks[i].Name == b.Name {
			mf.Blocks[i] = b
			return
		}
	}
	mf.Blocks = append(mf.Blocks, b)
}

func (mf *managedFile) removeBlock(name string) bool {
	for i := range mf.Blocks {
		if mf.Blocks[i].Name == name {
			mf.Blocks = append(mf.Blocks[:i], mf.Blocks[i+1:]...)
			return true
		}
	}
	return false
}

func (mf *managedFile) findBlock(name string) *HostBlock {
	for i := range mf.Blocks {
		if mf.Blocks[i].Name == name {
			return &mf.Blocks[i]
		}
	}
	return nil
}

func CircuitHostName(circuit string) string {
	return "drift." + circuit
}

func (m *Manager) WriteCircuitBlock(name, host, user string, ssh map[string]string) error {
	if !m.Options.Manage {
		return nil
	}
	if name == "" {
		return errors.New("sshconf: circuit name is required")
	}
	if host == "" {
		return errors.New("sshconf: host is required")
	}
	if err := ensureParentDir(m.Paths.ManagedSSHConfig); err != nil {
		return err
	}
	mf, err := parseManaged(m.Paths.ManagedSSHConfig)
	if err != nil {
		return err
	}

	block := circuitBlock(name, host, user, ssh)
	kartBlock := kartWildcardBlock(name, ssh)
	// Keep the global wildcard block at the end so OpenSSH's first-match
	// rule favors the literal Host drift.<name> and the per-circuit kart
	// wildcard Host drift.<name>.* before the catchall drift.*.*.
	var wildcard *HostBlock
	if found := mf.findBlock(WildcardHost); found != nil {
		dup := HostBlock{
			Name: found.Name,
			Body: append([]string(nil), found.Body...),
		}
		wildcard = &dup
	}
	mf.removeBlock(WildcardHost)
	mf.upsertBlock(block)
	mf.upsertBlock(kartBlock)
	if wildcard != nil {
		mf.Blocks = append(mf.Blocks, *wildcard)
	}
	return config.WriteFileAtomic(m.Paths.ManagedSSHConfig, mf.bytes(), 0o600)
}

func (m *Manager) RemoveCircuitBlock(name string) error {
	if !m.Options.Manage {
		return nil
	}
	if name == "" {
		return errors.New("sshconf: circuit name is required")
	}
	mf, err := parseManaged(m.Paths.ManagedSSHConfig)
	if err != nil {
		return err
	}
	removed := mf.removeBlock(CircuitHostName(name))
	if mf.removeBlock(KartWildcardHost(name)) {
		removed = true
	}
	if !removed {
		return nil
	}
	return config.WriteFileAtomic(m.Paths.ManagedSSHConfig, mf.bytes(), 0o600)
}

func (m *Manager) EnsureWildcardBlock() error {
	if !m.Options.Manage {
		return nil
	}
	if err := ensureParentDir(m.Paths.ManagedSSHConfig); err != nil {
		return err
	}
	mf, err := parseManaged(m.Paths.ManagedSSHConfig)
	if err != nil {
		return err
	}
	want := wildcardBlock()
	if existing := mf.findBlock(WildcardHost); existing != nil && bodyEqual(existing.Body, want.Body) {
		if mf.Blocks[len(mf.Blocks)-1].Name == WildcardHost {
			return nil
		}
	}
	mf.removeBlock(WildcardHost)
	mf.Blocks = append(mf.Blocks, want)
	return config.WriteFileAtomic(m.Paths.ManagedSSHConfig, mf.bytes(), 0o600)
}

// ListCircuits returns circuit short-names (no `drift.` prefix). Missing
// file yields nil, not an error.
func (m *Manager) ListCircuits() ([]string, error) {
	mf, err := parseManaged(m.Paths.ManagedSSHConfig)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, b := range mf.Blocks {
		if b.Name == WildcardHost {
			continue
		}
		// Per-circuit kart wildcards (Host drift.<c>.*) are not
		// circuits themselves — skip them so callers that do
		// "configured circuits" don't double-count.
		if strings.HasSuffix(b.Name, ".*") {
			continue
		}
		if strings.HasPrefix(b.Name, "drift.") {
			out = append(out, strings.TrimPrefix(b.Name, "drift."))
		}
	}
	return out, nil
}

func bodyEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// EnsureInclude prepends the drift Include directive to the file at path if
// it isn't already the first non-comment non-blank line. Creates the file
// with mode 0600 if absent. No other line in the file is edited.
func (m *Manager) EnsureInclude(path string) error {
	if !m.Options.Manage {
		return nil
	}
	if path == "" {
		return errors.New("sshconf: ssh config path is required")
	}
	if err := ensureParentDir(path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return config.WriteFileAtomic(path, []byte(IncludeDirective+"\n"), 0o600)
	case err != nil:
		return fmt.Errorf("sshconf: read %s: %w", path, err)
	}
	if hasIncludeAtTop(data) {
		return nil
	}
	var buf bytes.Buffer
	buf.WriteString(IncludeDirective)
	buf.WriteByte('\n')
	buf.Write(data)
	return config.WriteFileAtomic(path, buf.Bytes(), 0o600)
}

func hasIncludeAtTop(data []byte) bool {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line == IncludeDirective
	}
	return false
}

// CircuitSpec is the minimal data needed to materialize a drift.<name>
// Host block: the fields come straight from the client config
// (circuits.<name>.host and .ssh).
type CircuitSpec struct {
	Circuit string
	Host    string
	User    string
	SSH     map[string]string
}

// Reconcile makes the managed ssh_config reflect the given circuit
// specs. Used by the client's pre-dispatch hook so hand-edits to
// ~/.config/drift/config.yaml (add an ssh_args entry, change the host)
// take effect on the next drift invocation without re-running
// `drift circuit add`. No-op when Options.Manage is false or when every
// circuit's on-disk block already matches.
//
// Each spec's block is upserted; blocks for circuits no longer in the
// config are left alone (drift circuit rm already removes them, and we
// don't want to prune hand-authored aliases sharing the file).
func (m *Manager) Reconcile(userSSHConfigPath string, specs []CircuitSpec) error {
	if !m.Options.Manage {
		return nil
	}
	if len(specs) == 0 {
		return nil
	}
	if err := ensureParentDir(m.Paths.ManagedSSHConfig); err != nil {
		return err
	}
	mf, err := parseManaged(m.Paths.ManagedSSHConfig)
	if err != nil {
		return err
	}
	changed := false
	// Pull the wildcard aside so upserts stay before it; re-append at the end.
	var wildcard *HostBlock
	if found := mf.findBlock(WildcardHost); found != nil {
		dup := HostBlock{Name: found.Name, Body: append([]string(nil), found.Body...)}
		wildcard = &dup
		mf.removeBlock(WildcardHost)
	}
	for _, s := range specs {
		if s.Circuit == "" || s.Host == "" {
			continue
		}
		block := circuitBlock(s.Circuit, s.Host, s.User, s.SSH)
		if existing := mf.findBlock(block.Name); existing == nil || !blocksEqual(*existing, block) {
			mf.upsertBlock(block)
			changed = true
		}
		// Per-circuit kart wildcard: `Host drift.<c>.*` with User drifter
		// and the circuit's authentication-relevant ssh map entries.
		kartBlock := kartWildcardBlock(s.Circuit, s.SSH)
		if existing := mf.findBlock(kartBlock.Name); existing == nil || !blocksEqual(*existing, kartBlock) {
			mf.upsertBlock(kartBlock)
			changed = true
		}
	}
	if wildcard != nil {
		mf.Blocks = append(mf.Blocks, *wildcard)
	} else {
		// No prior wildcard: install one now so the Include chain resolves
		// `drift.<circuit>.<kart>` aliases via ssh-proxy on first use.
		mf.upsertBlock(wildcardBlock())
		changed = true
	}
	if !changed {
		return nil
	}
	if err := m.EnsureSocketsDir(); err != nil {
		return err
	}
	// Users who hand-edit ~/.config/drift/config.yaml (without ever
	// running `drift circuit add`) have no Include line in ~/.ssh/config,
	// so the drift.<circuit> alias never resolves. EnsureInclude is
	// idempotent (byte-match skip) so this is a no-op on the common
	// path. Opting out of drift managing the ssh setup is the
	// `manage_ssh_config: false` knob — we guard the whole Reconcile
	// call on ManagesSSHConfig() upstream.
	if err := m.EnsureInclude(userSSHConfigPath); err != nil {
		return err
	}
	return config.WriteFileAtomic(m.Paths.ManagedSSHConfig, mf.bytes(), 0o600)
}

func blocksEqual(a, b HostBlock) bool {
	if a.Name != b.Name || len(a.Body) != len(b.Body) {
		return false
	}
	for i := range a.Body {
		if a.Body[i] != b.Body[i] {
			return false
		}
	}
	return true
}

// InstallCircuit is the "full install" fan-out used by the CLI init/circuit
// flows: prepend the Include line to the user's ssh_config, make sure the
// sockets dir exists, (re)write the drift.<circuit> Host block, and ensure
// the trailing wildcard block is present. No-ops when m.Options.Manage is
// false — matches the other mutating Manager methods.
func (m *Manager) InstallCircuit(userSSHConfigPath, circuit, host, user string, ssh map[string]string) error {
	if err := m.EnsureInclude(userSSHConfigPath); err != nil {
		return err
	}
	if err := m.EnsureSocketsDir(); err != nil {
		return err
	}
	if err := m.WriteCircuitBlock(circuit, host, user, ssh); err != nil {
		return err
	}
	return m.EnsureWildcardBlock()
}

func (m *Manager) EnsureSocketsDir() error {
	if !m.Options.Manage {
		return nil
	}
	path := m.Paths.SocketsDir
	if path == "" {
		return errors.New("sshconf: sockets dir path is required")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("sshconf: create %s: %w", path, err)
	}
	// MkdirAll does not chmod an existing directory; force 0700 so a
	// pre-existing 0755 dir is tightened — ssh refuses group/world-writable
	// control sockets.
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // G302: path is a directory; 0700 is the intended mode.
		return fmt.Errorf("sshconf: chmod %s: %w", path, err)
	}
	return nil
}

func circuitBlock(circuitName, host, user string, ssh map[string]string) HostBlock {
	// Accept host as either "example.com", "example.com:2222", "[::1]",
	// or "[::1]:2222"; the explicit `Port` directive matters because
	// OpenSSH does not parse a colon-port inside HostName. name.SplitHostPort
	// strips the IPv6 brackets from hostname; re-wrap them on emit so the
	// HostName value stays unambiguous for OpenSSH.
	hostname, port, err := name.SplitHostPort(host)
	if err != nil {
		// Fall back to the raw input — a malformed host will surface at
		// connect time rather than corrupting the managed file.
		hostname = host
		port = ""
	}
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	body := []string{
		"  HostName " + hostname,
	}
	if port != "" {
		body = append(body, "  Port "+port)
	}
	if user != "" {
		body = append(body, "  User "+user)
	}
	// ssh comes straight from the client config's `ssh:` map. Each entry
	// maps an ssh_config directive name to its value — IdentityFile,
	// Port, ForwardAgent, and so on. Emitted in sorted key order so
	// repeated reconciles produce byte-identical output.
	for _, k := range sortedKeys(ssh) {
		if v := strings.TrimSpace(ssh[k]); v != "" {
			body = append(body, "  "+k+" "+v)
		}
	}
	body = append(body,
		"  ControlMaster auto",
		"  ControlPath ~/.config/drift/sockets/cm-%r@%h:%p",
		"  ControlPersist 10m",
		"  ServerAliveInterval 30",
		"  ServerAliveCountMax 3",
	)
	return HostBlock{Name: CircuitHostName(circuitName), Body: body}
}

func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func wildcardBlock() HostBlock {
	return HostBlock{
		Name: WildcardHost,
		Body: []string{
			"  ProxyCommand drift ssh-proxy %h %p",
			"  ControlMaster auto",
			"  ControlPath ~/.config/drift/sockets/cm-%r@%h:%p",
			"  ControlPersist 10m",
		},
	}
}

// DriftSSHAlias is the stable login name every `Host drift.<c>.<k>`
// block uses. Must match internal/kart's DriftSSHAlias — lakitu
// installs the same-UID /etc/passwd alias inside each kart, and the
// workstation's ssh auths as this name. Duplicated here so sshconf
// has no dependency on the larger kart package.
const DriftSSHAlias = "drifter"

// KartWildcardHost is the per-circuit kart wildcard pattern. One
// `Host drift.<circuit>.*` block per configured circuit lets ssh
// inherit the right IdentityFile (and any other per-circuit ssh map
// keys) without forcing callers to hand-write a block per kart.
func KartWildcardHost(circuit string) string { return "drift." + circuit + ".*" }

// kartWildcardBlock builds the `Host drift.<circuit>.*` block that
// fronts every kart on the circuit. It pins User=drifter so ssh auth
// lands on the lakitu-installed login alias regardless of which
// upstream image the kart uses, carries the circuit's
// authentication-relevant ssh map entries (IdentityFile,
// IdentitiesOnly, CertificateFile, PreferredAuthentications) so the
// same key that works for the circuit block works here too, and
// routes through drift ssh-proxy so the stdio tunnel is established
// the same way drift connect does it.
func kartWildcardBlock(circuitName string, ssh map[string]string) HostBlock {
	body := []string{
		"  User " + DriftSSHAlias,
		"  ProxyCommand drift ssh-proxy %h %p",
	}
	// Only forward authentication-relevant ssh map entries. Host /
	// HostName / Port are per-circuit (the proxy command reaches the
	// kart, not the circuit) and would produce wrong behaviour here.
	for _, k := range sortedKeys(ssh) {
		if !isAuthDirective(k) {
			continue
		}
		if v := strings.TrimSpace(ssh[k]); v != "" {
			body = append(body, "  "+k+" "+v)
		}
	}
	body = append(body,
		// devpod's helper ssh-server regenerates its host key on every
		// `devpod ssh --stdio` invocation — the same way devpod's own
		// `~/.ssh/config` entries handle it: `no` + `/dev/null`. That's
		// not a security downgrade here because the real authentication
		// already happened one hop up in the ProxyCommand — the stdio
		// tunnel only exists for workstations that can ssh into the
		// circuit as `dev`. An attacker who can tamper with the kart's
		// host key has already compromised the circuit user.
		//
		// `LogLevel error` silences the "Permanently added … to list
		// of known hosts" warnings that would otherwise fire on every
		// master start.
		"  StrictHostKeyChecking no",
		"  UserKnownHostsFile /dev/null",
		"  LogLevel error",
		"  ControlMaster auto",
		"  ControlPath ~/.config/drift/sockets/cm-%r@%h:%p",
		"  ControlPersist 10m",
	)
	return HostBlock{Name: KartWildcardHost(circuitName), Body: body}
}

// isAuthDirective names the ssh_config keys that should flow from the
// circuit's `ssh:` map into the kart wildcard block. Everything not
// on this list is intentionally dropped — we don't want per-circuit
// HostName / Port (wrong target) or User (forced to drifter).
func isAuthDirective(key string) bool {
	switch strings.ToLower(key) {
	case "identityfile", "identitiesonly", "certificatefile",
		"preferredauthentications", "pubkeyauthentication":
		return true
	}
	return false
}

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("sshconf: create %s: %w", dir, err)
	}
	return nil
}
