package exec

import (
	"bytes"
	"io"
	"os"
	osexec "os/exec"
	"runtime"
	"strings"
)

// termuxLinkerWrap works around Android 10+ W^X SELinux restrictions on
// exec'ing app-data binaries when running inside Termux. termux-exec's
// LD_PRELOAD wrapper normally rewrites every exec of such a binary to
// invoke it through /system/bin/linker64 (or /system/bin/linker on 32-bit
// arches), which is the escape hatch SELinux permits. Go's syscall.Exec
// bypasses libc and goes straight to the kernel, so the LD_PRELOAD shim
// never fires — without this wrap, exec returns EACCES ("permission
// denied") for ssh, mosh, git, and every other Termux package binary.
//
// Returns (name, args) unchanged unless all of the following hold:
//   - $TERMUX_VERSION is set, or $PREFIX is under /com.termux/
//   - the resolved binary lives under $PREFIX
//   - a matching linker exists at /system/bin/linker{,64}
//
// When rewritten, the new argv is [linker, resolvedBinary, originalArgs…].
// The Android linker shifts argv on entry, so the target binary still sees
// its own path as argv[0].
//
// Script shebangs are handled by wrapping the interpreter instead of the
// script: e.g. Termux's mosh is a Perl script under $PREFIX/bin/mosh whose
// shebang points to $PREFIX/bin/perl; passing the script to the linker
// fails with "bad ELF magic", so we rewrite to
// [linker, perl, script, originalArgs…] when the interpreter itself is
// under $PREFIX (and thus also needs the W^X escape hatch).
func termuxLinkerWrap(name string, args []string) (string, []string) {
	return linkerWrap(name, args, termuxPrefix(), termuxLinker(), osexec.LookPath, fileExists, readShebang)
}

func linkerWrap(
	name string,
	args []string,
	prefix, linker string,
	lookPath func(string) (string, error),
	exists func(string) bool,
	shebang func(string) (string, string, bool),
) (string, []string) {
	if prefix == "" || linker == "" || !exists(linker) {
		return name, args
	}
	resolved, err := lookPath(name)
	if err != nil {
		return name, args
	}
	prefixSlash := strings.TrimRight(prefix, "/") + "/"
	if !strings.HasPrefix(resolved, prefixSlash) {
		return name, args
	}
	// Script under $PREFIX: wrap the interpreter, not the script itself.
	// If the interpreter lives outside $PREFIX (e.g. /system/bin/sh), leave
	// the invocation alone — the kernel's shebang handler will re-exec the
	// interpreter, and exec'ing something outside $PREFIX doesn't trip the
	// W^X check that termux-exec's LD_PRELOAD shim works around.
	if interp, interpArg, ok := shebang(resolved); ok {
		if !strings.HasPrefix(interp, prefixSlash) {
			return name, args
		}
		newArgs := make([]string, 0, len(args)+3)
		newArgs = append(newArgs, interp)
		if interpArg != "" {
			newArgs = append(newArgs, interpArg)
		}
		newArgs = append(newArgs, resolved)
		newArgs = append(newArgs, args...)
		return linker, newArgs
	}
	newArgs := make([]string, 0, len(args)+1)
	newArgs = append(newArgs, resolved)
	newArgs = append(newArgs, args...)
	return linker, newArgs
}

// readShebang returns (interp, arg, true) if path begins with a "#!" line.
// Kernel-style: any whitespace-separated tail is passed as a single
// argument, even if it contains embedded spaces.
func readShebang(path string) (string, string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", "", false
	}
	buf = buf[:n]
	if len(buf) < 2 || buf[0] != '#' || buf[1] != '!' {
		return "", "", false
	}
	line := buf[2:]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = bytes.TrimLeft(line, " \t")
	if i := bytes.IndexAny(line, " \t"); i >= 0 {
		return string(line[:i]), strings.TrimSpace(string(line[i+1:])), true
	}
	return string(line), "", true
}

func termuxPrefix() string {
	prefix := os.Getenv("PREFIX")
	if os.Getenv("TERMUX_VERSION") == "" && !strings.Contains(prefix, "/com.termux/") {
		return ""
	}
	return prefix
}

func termuxLinker() string {
	switch runtime.GOARCH {
	case "arm64", "amd64":
		return "/system/bin/linker64"
	case "arm", "386":
		return "/system/bin/linker"
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
