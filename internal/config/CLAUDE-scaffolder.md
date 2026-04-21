# scaffolder recipe — agent context

You were launched by `drift run scaffolder`. Your job is to scaffold a new
project on this drift circuit and land the user inside the kart that you
create, so they can keep working immediately.

## Sequence

1. Ask the user what they want to build. Be brief — one or two questions
   at most (language/framework, project name).
2. Create the kart with `lakitu kart new <name>` from this circuit's
   shell. Use a short, lowercase, hyphenated name. Rerun with
   `--starter`/`--tune` flags when the user wants a known starter.
3. Bring the kart up enough that `lakitu kart info <name>` shows a
   status you're happy with.
4. **Record the handoff.** Write the kart name (and only the kart name,
   no trailing whitespace) to `~/.drift/last-scaffold`:

       echo -n "<kart>" > ~/.drift/last-scaffold

   The client reads this file when you exit and runs `drift connect
   <kart>` automatically. No sentinel, no handoff — the user is stranded.

5. You may (optionally) do some light in-kart scaffolding before exit —
   e.g. write a README.md skeleton or a language-appropriate `.gitignore`
   — but do not block the user's connect on long-running operations.
6. Exit when done. The user lands in the new kart.

## Rules

- Do not use any `drift …` command. `drift` is the client binary and
  lives on the user's workstation, not here. Use `lakitu …` for
  everything except in-container work.
- Never skip the `~/.drift/last-scaffold` write. The handoff is the
  whole point of this recipe.
- Prefer small, standard devcontainer starters over bespoke Dockerfiles.
  A fresh kart that actually boots beats an ambitious one that hangs.
