package drift

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/tailscale"
)

// tailscalePicker lists online tailnet peers, lets the user pick one, and
// prompts for the SSH user. Returns "user@host" on success. Blank input
// at the picker cancels with (_, false, nil).
func tailscalePicker(ctx context.Context, stdin io.Reader, stderr io.Writer) (string, bool, error) {
	peers, err := tailscale.Peers(ctx)
	if err != nil {
		return "", false, err
	}
	if len(peers) == 0 {
		fmt.Fprintln(stderr, "no online tailscale peers found")
		return "", false, nil
	}

	p := style.For(stderr, false)
	fmt.Fprintln(stderr, p.Bold("tailscale peers:"))
	rows := make([][]string, 0, len(peers))
	for i, pr := range peers {
		ip := pr.PrimaryIP()
		rows = append(rows, []string{
			fmt.Sprintf("[%d]", i+1),
			pr.DisplayName(),
			ip,
		})
	}
	writeTable(stderr, p, []string{"", "HOST", "IP"}, rows,
		func(_, col int, _ *style.Palette) lipgloss.Style {
			if col == 1 {
				return lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
			}
			return lipgloss.NewStyle()
		})

	br := bufio.NewReader(stdin)
	fmt.Fprint(stderr, "pick (number, empty to cancel): ")
	line, err := br.ReadString('\n')
	if err != nil && !strings.HasSuffix(err.Error(), "EOF") {
		return "", false, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false, nil
	}
	idx, err := strconv.Atoi(line)
	if err != nil || idx < 1 || idx > len(peers) {
		return "", false, fmt.Errorf("invalid pick %q (expected 1..%d)", line, len(peers))
	}
	picked := peers[idx-1]

	defaultUser := os.Getenv("USER")
	prompt := "ssh user"
	if defaultUser != "" {
		prompt += " [" + defaultUser + "]"
	}
	prompt += ": "
	fmt.Fprint(stderr, prompt)
	userLine, err := br.ReadString('\n')
	if err != nil && !strings.HasSuffix(err.Error(), "EOF") {
		return "", false, err
	}
	userLine = strings.TrimSpace(userLine)
	if userLine == "" {
		if defaultUser == "" {
			return "", false, fmt.Errorf("ssh user is required")
		}
		userLine = defaultUser
	}

	// Prefer the MagicDNS name over an IP — it's stable across reboots
	// and typically resolvable on any host that runs the tailscale daemon.
	target := picked.DisplayName()
	if target == "" {
		target = picked.PrimaryIP()
	}
	return userLine + "@" + target, true, nil
}
