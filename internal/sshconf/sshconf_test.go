package sshconf

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newTestManager returns a Manager rooted under t.TempDir so tests never
// touch the real `$HOME`.
func newTestManager(t *testing.T, manage bool) (*Manager, Paths) {
	t.Helper()
	root := t.TempDir()
	paths := Paths{
		UserSSHConfig:    filepath.Join(root, "ssh", "config"),
		ManagedSSHConfig: filepath.Join(root, "drift", "ssh_config"),
		SocketsDir:       filepath.Join(root, "drift", "sockets"),
	}
	return New(paths, Options{Manage: manage}), paths
}

func TestWriteCircuitBlock_CreatesFileWithCanonicalFormat(t *testing.T) {
	m, paths := newTestManager(t, true)
	if err := m.WriteCircuitBlock("my-server", "my-server.example.com", "dev"); err != nil {
		t.Fatalf("WriteCircuitBlock: %v", err)
	}
	got := readFile(t, paths.ManagedSSHConfig)
	want := strings.Join([]string{
		"Host drift.my-server",
		"  HostName my-server.example.com",
		"  User dev",
		"  ControlMaster auto",
		"  ControlPath ~/.config/drift/sockets/cm-%r@%h:%p",
		"  ControlPersist 10m",
		"  ServerAliveInterval 30",
		"  ServerAliveCountMax 3",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("managed ssh_config mismatch.\n got:\n%s\nwant:\n%s", got, want)
	}
	mode := statMode(t, paths.ManagedSSHConfig)
	if mode.Perm() != 0o600 {
		t.Fatalf("managed ssh_config mode = %o, want 0600", mode.Perm())
	}
}

func TestWriteCircuitBlock_OmitsUserWhenEmpty(t *testing.T) {
	m, paths := newTestManager(t, true)
	if err := m.WriteCircuitBlock("srv", "srv.example.com", ""); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, paths.ManagedSSHConfig)
	if strings.Contains(got, "User ") {
		t.Fatalf("expected no User directive, got:\n%s", got)
	}
	if !strings.Contains(got, "HostName srv.example.com") {
		t.Fatalf("missing HostName, got:\n%s", got)
	}
}

func TestWriteCircuitBlock_IdempotentReRun(t *testing.T) {
	m, paths := newTestManager(t, true)
	if err := m.WriteCircuitBlock("srv", "srv.example.com", "dev"); err != nil {
		t.Fatal(err)
	}
	first := readFile(t, paths.ManagedSSHConfig)
	if err := m.WriteCircuitBlock("srv", "srv.example.com", "dev"); err != nil {
		t.Fatal(err)
	}
	second := readFile(t, paths.ManagedSSHConfig)
	if first != second {
		t.Fatalf("re-run not byte-identical:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

func TestWriteCircuitBlock_ReplacesExistingBlockInPlace(t *testing.T) {
	m, paths := newTestManager(t, true)
	if err := m.WriteCircuitBlock("a", "a.example.com", "olduser"); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteCircuitBlock("b", "b.example.com", "bob"); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteCircuitBlock("a", "a.example.com", "newuser"); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, paths.ManagedSSHConfig)
	if strings.Contains(got, "User olduser") {
		t.Fatalf("old user should have been replaced, got:\n%s", got)
	}
	if !strings.Contains(got, "User newuser") {
		t.Fatalf("new user not found, got:\n%s", got)
	}
	// Order should be preserved: a first, b second.
	aIdx := strings.Index(got, "Host drift.a")
	bIdx := strings.Index(got, "Host drift.b")
	if aIdx < 0 || bIdx < 0 || aIdx > bIdx {
		t.Fatalf("block ordering not preserved: aIdx=%d bIdx=%d\n%s", aIdx, bIdx, got)
	}
}

func TestWriteCircuitBlock_KeepsWildcardAtEnd(t *testing.T) {
	m, paths := newTestManager(t, true)
	if err := m.EnsureWildcardBlock(); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteCircuitBlock("srv", "srv.example.com", "dev"); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, paths.ManagedSSHConfig)
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "  ControlPersist 10m") {
		t.Fatalf("wildcard block should be last, got:\n%s", got)
	}
	wildcardIdx := strings.Index(got, "Host drift.*.*")
	srvIdx := strings.Index(got, "Host drift.srv")
	if wildcardIdx < 0 || srvIdx < 0 || srvIdx > wildcardIdx {
		t.Fatalf("ordering wrong: srv=%d wildcard=%d\n%s", srvIdx, wildcardIdx, got)
	}
}

func TestRemoveCircuitBlock_Idempotent(t *testing.T) {
	m, paths := newTestManager(t, true)
	if err := m.WriteCircuitBlock("srv", "srv.example.com", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveCircuitBlock("srv"); err != nil {
		t.Fatal(err)
	}
	// Second remove is a no-op and must not error.
	if err := m.RemoveCircuitBlock("srv"); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, paths.ManagedSSHConfig)
	if strings.Contains(got, "drift.srv") {
		t.Fatalf("block still present:\n%s", got)
	}
}

func TestRemoveCircuitBlock_MissingFileNoError(t *testing.T) {
	m, _ := newTestManager(t, true)
	if err := m.RemoveCircuitBlock("nope"); err != nil {
		t.Fatalf("removing from nonexistent file should be a no-op: %v", err)
	}
}

func TestEnsureWildcardBlock_AppendedOnceAcrossReRuns(t *testing.T) {
	m, paths := newTestManager(t, true)
	for i := 0; i < 5; i++ {
		if err := m.EnsureWildcardBlock(); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	got := readFile(t, paths.ManagedSSHConfig)
	count := strings.Count(got, "Host drift.*.*")
	if count != 1 {
		t.Fatalf("wildcard block appears %d times, want 1:\n%s", count, got)
	}
}

func TestAddReAddRmRoundTrip(t *testing.T) {
	m, paths := newTestManager(t, true)
	// Start from an empty managed file.
	if err := m.EnsureWildcardBlock(); err != nil {
		t.Fatal(err)
	}
	baseline := readFile(t, paths.ManagedSSHConfig)

	// Add → re-add → rm.
	if err := m.WriteCircuitBlock("srv", "srv.example.com", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteCircuitBlock("srv", "srv.example.com", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveCircuitBlock("srv"); err != nil {
		t.Fatal(err)
	}
	final := readFile(t, paths.ManagedSSHConfig)
	if baseline != final {
		t.Fatalf("round-trip not byte-identical:\n--- baseline ---\n%s\n--- final ---\n%s", baseline, final)
	}
}

func TestEnsureInclude_CreatesFileIfMissing(t *testing.T) {
	m, paths := newTestManager(t, true)
	if err := m.EnsureInclude(paths.UserSSHConfig); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, paths.UserSSHConfig)
	want := IncludeDirective + "\n"
	if got != want {
		t.Fatalf("ssh config mismatch.\n got: %q\nwant: %q", got, want)
	}
	mode := statMode(t, paths.UserSSHConfig)
	if runtime.GOOS != "windows" && mode.Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", mode.Perm())
	}
}

func TestEnsureInclude_PrependsWhenAbsent(t *testing.T) {
	m, paths := newTestManager(t, true)
	original := "Host myhost\n  HostName example.com\n  User alice\n"
	writeFileHelper(t, paths.UserSSHConfig, original, 0o600)

	if err := m.EnsureInclude(paths.UserSSHConfig); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, paths.UserSSHConfig)
	want := IncludeDirective + "\n" + original
	if got != want {
		t.Fatalf("ssh config mismatch.\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEnsureInclude_NoOpWhenAlreadyFirstDirective(t *testing.T) {
	m, paths := newTestManager(t, true)
	original := IncludeDirective + "\nHost myhost\n  HostName example.com\n"
	writeFileHelper(t, paths.UserSSHConfig, original, 0o600)

	if err := m.EnsureInclude(paths.UserSSHConfig); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, paths.UserSSHConfig)
	if got != original {
		t.Fatalf("file was modified unexpectedly.\n got:\n%s\nwant:\n%s", got, original)
	}
}

func TestEnsureInclude_SkipsLeadingCommentsAndBlanks(t *testing.T) {
	m, paths := newTestManager(t, true)
	original := "# my ssh config\n\n" + IncludeDirective + "\nHost other\n"
	writeFileHelper(t, paths.UserSSHConfig, original, 0o600)

	if err := m.EnsureInclude(paths.UserSSHConfig); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, paths.UserSSHConfig)
	if got != original {
		t.Fatalf("comments/blanks should not cause re-prepend:\n%s", got)
	}
}

func TestEnsureInclude_PreservesUnrelatedLinesByteForByte(t *testing.T) {
	m, paths := newTestManager(t, true)
	// A representative busy config — includes comments, multiple Host blocks,
	// trailing whitespace, a Match block, and a trailing newline.
	original := strings.Join([]string{
		"# global defaults",
		"Host *",
		"  ServerAliveInterval 60",
		"  IdentityFile ~/.ssh/id_rsa",
		"",
		"Host bastion",
		"  HostName bastion.example.com",
		"  User ops",
		"",
		"Match host *.internal",
		"  ProxyJump bastion",
		"",
	}, "\n")
	writeFileHelper(t, paths.UserSSHConfig, original, 0o600)

	if err := m.EnsureInclude(paths.UserSSHConfig); err != nil {
		t.Fatal(err)
	}

	got := readFile(t, paths.UserSSHConfig)
	want := IncludeDirective + "\n" + original
	if got != want {
		t.Fatalf("unrelated lines modified.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestEnsureSocketsDir_CreatesWith0700(t *testing.T) {
	m, paths := newTestManager(t, true)
	if err := m.EnsureSocketsDir(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(paths.SocketsDir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("sockets path is not a directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("sockets dir mode = %o, want 0700", info.Mode().Perm())
	}
}

func TestEnsureSocketsDir_TightensExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only")
	}
	m, paths := newTestManager(t, true)
	if err := os.MkdirAll(paths.SocketsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureSocketsDir(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(paths.SocketsDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode not tightened, got %o", info.Mode().Perm())
	}
}

func TestManageFalse_ProducesZeroFilesystemWrites(t *testing.T) {
	m, paths := newTestManager(t, false)

	if err := m.EnsureInclude(paths.UserSSHConfig); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureWildcardBlock(); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureSocketsDir(); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteCircuitBlock("srv", "srv.example.com", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveCircuitBlock("srv"); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{paths.UserSSHConfig, paths.ManagedSSHConfig, paths.SocketsDir} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Manage=false wrote to %s (err=%v)", p, err)
		}
	}
	// Parent dirs must also not have been eagerly created.
	for _, p := range []string{
		filepath.Dir(paths.UserSSHConfig),
		filepath.Dir(paths.ManagedSSHConfig),
	} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Manage=false created parent dir %s (err=%v)", p, err)
		}
	}
}

func TestParseManaged_RoundTripsUserBlock(t *testing.T) {
	// A hand-written file (perhaps from a prior drift version) should parse
	// and re-serialize without losing content. The canonical serializer may
	// normalize whitespace (trailing blanks, leading header blanks) but
	// directive text must survive.
	input := strings.Join([]string{
		"# drift-managed",
		"",
		"Host drift.alpha",
		"  HostName alpha.example.com",
		"  User dev",
		"  ControlMaster auto",
		"",
		"Host drift.beta",
		"  HostName beta.example.com",
		"  User dev",
		"",
		"Host drift.*.*",
		"  ProxyCommand drift ssh-proxy %h %p",
		"  ControlMaster auto",
		"",
	}, "\n")
	mf := parseManagedBytes([]byte(input))
	if len(mf.Blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(mf.Blocks))
	}
	if mf.Blocks[0].Name != "drift.alpha" || mf.Blocks[2].Name != "drift.*.*" {
		t.Fatalf("block names wrong: %+v", mf.Blocks)
	}
	// Round-trip: serialize, parse again, expect identical block set.
	out := mf.bytes()
	mf2 := parseManagedBytes(out)
	if len(mf2.Blocks) != len(mf.Blocks) {
		t.Fatalf("round-trip lost blocks: %d vs %d", len(mf2.Blocks), len(mf.Blocks))
	}
	for i := range mf.Blocks {
		if mf.Blocks[i].Name != mf2.Blocks[i].Name {
			t.Fatalf("block %d name diverged: %q vs %q", i, mf.Blocks[i].Name, mf2.Blocks[i].Name)
		}
		if !bodyEqual(mf.Blocks[i].Body, mf2.Blocks[i].Body) {
			t.Fatalf("block %d body diverged:\n%v\n%v", i, mf.Blocks[i].Body, mf2.Blocks[i].Body)
		}
	}
}

func TestListCircuits(t *testing.T) {
	m, _ := newTestManager(t, true)
	if err := m.WriteCircuitBlock("alpha", "a.example.com", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteCircuitBlock("beta", "b.example.com", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureWildcardBlock(); err != nil {
		t.Fatal(err)
	}
	got, err := m.ListCircuits()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "beta"}
	if !equalStrings(got, want) {
		t.Fatalf("ListCircuits = %v, want %v", got, want)
	}
}

func TestAddThenRmRestoresUserSSHConfigByteIdentical(t *testing.T) {
	// Simulates the full user-visible lifecycle: ensure include → add circuit
	// → re-add (idempotent) → rm circuit. At the end, the drift-managed file
	// is what it was after EnsureWildcardBlock alone, and ~/.ssh/config is
	// whatever EnsureInclude produced — both must match their baselines.
	m, paths := newTestManager(t, true)

	// Pre-populate ~/.ssh/config with user content so we verify it is not
	// mutated beyond the prepended Include line.
	userOriginal := "Host personal\n  HostName personal.example.com\n"
	writeFileHelper(t, paths.UserSSHConfig, userOriginal, 0o600)

	if err := m.EnsureInclude(paths.UserSSHConfig); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureWildcardBlock(); err != nil {
		t.Fatal(err)
	}
	userAfterInclude := readFile(t, paths.UserSSHConfig)
	managedBaseline := readFile(t, paths.ManagedSSHConfig)

	// add → re-add → rm
	if err := m.WriteCircuitBlock("srv", "srv.example.com", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteCircuitBlock("srv", "srv.example.com", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveCircuitBlock("srv"); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, paths.UserSSHConfig); got != userAfterInclude {
		t.Fatalf("~/.ssh/config changed across add/rm:\n got:\n%s\nwant:\n%s", got, userAfterInclude)
	}
	if got := readFile(t, paths.ManagedSSHConfig); got != managedBaseline {
		t.Fatalf("managed ssh_config not restored after rm:\n got:\n%s\nwant:\n%s", got, managedBaseline)
	}
}

// ---- helpers ---------------------------------------------------------------

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func writeFileHelper(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}

func equalStrings(a, b []string) bool {
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
