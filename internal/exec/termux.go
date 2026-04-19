package exec

import (
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
func termuxLinkerWrap(name string, args []string) (string, []string) {
	return linkerWrap(name, args, termuxPrefix(), termuxLinker(), osexec.LookPath, fileExists)
}

func linkerWrap(
	name string,
	args []string,
	prefix, linker string,
	lookPath func(string) (string, error),
	exists func(string) bool,
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
	newArgs := make([]string, 0, len(args)+1)
	newArgs = append(newArgs, resolved)
	newArgs = append(newArgs, args...)
	return linker, newArgs
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
