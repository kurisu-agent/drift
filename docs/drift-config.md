# drift client config

Path: `$XDG_CONFIG_HOME/drift/config.yaml` (default: `~/.config/drift/config.yaml`).
Written at `0600` — may reference SSH identities. `drift init` / `drift circuit add` manage this file; hand edits round-trip unchanged.

## Shape

```yaml
default_circuit: lab                       # used when -c is not set
manage_ssh_config: true                    # default true; false skips writes to ~/.ssh/config
circuits:
  lab:
    host: dev@lab.example.com              # required: ssh destination
    ssh_args:                              # optional: extra flags forwarded to ssh
      - "-i"
      - "~/.ssh/lab_ed25519"
      - "-o"
      - "IdentitiesOnly=yes"
  edge:
    host: dev@edge.example.com:2222        # port in the host, or via ssh_args
```

### `circuits.<name>.ssh_args`

Extra flags spliced into the ssh invocation whenever drift connects to a kart or the circuit's host. Use for one-off overrides that don't belong in `~/.ssh/config`.

- **ssh path**: args slot between `-A` and the target, so they apply to the connection — not the remote command.
- **mosh path**: args are wrapped into `--ssh="ssh <quoted args>"` so mosh's bootstrap ssh picks them up.
- **Tilde expansion**: leading `~/` and bare `~` resolve against `$HOME` at use-time. `~user/...` is left untouched.

You can also pass args on the command line after `--`. CLI args append after config args, matching ssh's "last wins for `-p`/`-o …`, accumulate for `-i`" rules:

```sh
drift connect mykart -- -p 2222 -o StrictHostKeyChecking=no
```

Works on `drift connect`, `drift kart connect`, and `drift circuit connect`.

### `manage_ssh_config`

When `true` (default), drift writes `Host drift.<circuit>[.<kart>]` blocks into `~/.config/drift/ssh_config` and includes that file from `~/.ssh/config`. Set `false` to opt out — drift will pass `user@host` directly to ssh/mosh on every connect, losing ControlMaster speedup but keeping `~/.ssh/config` untouched.

### `default_circuit`

Used when neither `-c/--circuit` nor a positional circuit argument is supplied. Must name a circuit defined under `circuits:` — load-time validation rejects dangling references.
