# drift client config

Path: `$XDG_CONFIG_HOME/drift/config.yaml` (default: `~/.config/drift/config.yaml`).
Written at `0600` since it may reference SSH identities. `drift init` / `drift circuit add` manage this file; hand edits round-trip unchanged, and drift re-syncs `~/.config/drift/ssh_config` with the yaml on every invocation so edits take effect immediately.

## Shape

```yaml
default_circuit: lab                       # used when -c is not set
manage_ssh_config: true                    # default true; false skips writes to ~/.ssh/config
circuits:
  lab:
    host: dev@lab.example.com              # required: ssh destination
    ssh:                                   # optional: extra ssh_config directives for this host
      IdentityFile: "~/.ssh/lab_ed25519"
      IdentitiesOnly: "yes"
  edge:
    host: dev@edge.example.com
    ssh:
      Port: "2222"                         # or set the port in `host:` as dev@edge.example.com:2222
      ForwardAgent: "yes"
```

### `circuits.<name>.ssh`

A map of ssh_config directive names to values. Each entry is written as a `<Key> <Value>` line in the generated `Host drift.<circuit>` block in `~/.config/drift/ssh_config`, so lakitu RPCs (which always dial `ssh drift.<circuit>`) pick them up.

Keys use ssh_config's native names (`IdentityFile`, `Port`, `ForwardAgent`, `ProxyJump`, `IdentitiesOnly`, …). Values are not pre-expanded: `~/foo` stays literal on disk and resolves at ssh-use time.

For one-off overrides not worth persisting, pass raw ssh flags after `--`:

```sh
drift connect mykart -- -p 2222 -o StrictHostKeyChecking=no
```

Works on `drift connect`, `drift kart connect`, and `drift circuit connect`. The passthrough goes directly to the ssh argv; config-side `ssh:` entries already live in ssh_config, so OpenSSH's last-wins rule lets CLI flags override.

### `manage_ssh_config`

When `true` (default), drift writes `Host drift.<circuit>[.<kart>]` blocks into `~/.config/drift/ssh_config` and includes that file from `~/.ssh/config`. Set `false` to opt out: drift will pass `user@host` directly to ssh/mosh on every connect, losing ControlMaster speedup but keeping `~/.ssh/config` untouched. `ssh:` map entries are only emitted when this is `true`.

### `default_circuit`

Used when neither `-c/--circuit` nor a positional circuit argument is supplied. Must name a circuit defined under `circuits:`; load-time validation rejects dangling references.
