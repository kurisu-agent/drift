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
	"strings"

	"github.com/kurisu-agent/drift/internal/config"
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
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
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

func (m *Manager) WriteCircuitBlock(name, host, user string) error {
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

	block := circuitBlock(name, host, user)
	// Keep the wildcard block at the end so OpenSSH's first-match rule
	// favors the literal Host drift.<name>.
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
	if !mf.removeBlock(CircuitHostName(name)) {
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
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line == IncludeDirective
	}
	return false
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

func circuitBlock(name, host, user string) HostBlock {
	// Accept host as either "example.com" or "example.com:2222"; the
	// explicit `Port` directive matters because OpenSSH does not parse a
	// colon-port inside HostName.
	hostname, port := splitHostPort(host)
	body := []string{
		"  HostName " + hostname,
	}
	if port != "" {
		body = append(body, "  Port "+port)
	}
	if user != "" {
		body = append(body, "  User "+user)
	}
	body = append(body,
		"  ControlMaster auto",
		"  ControlPath ~/.config/drift/sockets/cm-%r@%h:%p",
		"  ControlPersist 10m",
		"  ServerAliveInterval 30",
		"  ServerAliveCountMax 3",
	)
	return HostBlock{Name: CircuitHostName(name), Body: body}
}

// splitHostPort is a tiny local variant so sshconf doesn't import internal/name.
// Bracketed IPv6 (`[::1]:22`) preserves the brackets in HostName; bare IPv6
// with multiple colons is left intact with no port extraction.
func splitHostPort(host string) (hostname, port string) {
	if strings.HasPrefix(host, "[") {
		end := strings.Index(host, "]")
		if end < 0 {
			return host, ""
		}
		hostname = host[:end+1]
		rest := host[end+1:]
		if strings.HasPrefix(rest, ":") {
			return hostname, rest[1:]
		}
		return hostname, ""
	}
	if strings.Count(host, ":") > 1 {
		return host, ""
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		return host[:i], host[i+1:]
	}
	return host, ""
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
