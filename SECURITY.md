# Security policy

## Supported versions

graphmaster is pre-1.0. Security fixes go into the latest release (and
`main`); older releases are not patched.

## Reporting a vulnerability

Please report vulnerabilities privately through
[GitHub security advisories](https://github.com/pragmataW/graphmaster/security/advisories/new)
("Report a vulnerability" on the repository's Security tab). Do not open a
public issue for security problems.

Include the graphmaster version (`graphmaster --version`), your OS, the
affected command or agent integration, and steps to reproduce. You should get
an acknowledgement within a few days. We will agree on a disclosure date with
you once a fix is ready and credit you in the advisory unless you prefer
otherwise.

## Security model

graphmaster is a local tool. Knowing what it does and does not do helps when
judging a report:

- **No network access.** graphmaster makes no outbound network requests and
  sends no telemetry. (`install.sh` downloads a release archive from GitHub
  and verifies its SHA-256 checksum; the binary itself never downloads
  anything.)
- **One listener, local only.** `graphmaster ui` binds `127.0.0.1`, requires
  a random per-process token on every request, and rejects requests whose
  `Host` header does not name `localhost` or `127.0.0.1` (DNS rebinding).
  `graphmaster mcp` speaks only over stdin/stdout.
- **Repository data stays in the repository.** The graph, parse cache, hook
  state and hook log live in `<repo>/.graphmaster/`, which is excluded from
  git through `.git/info/exclude` and a `*` `.gitignore`. They contain
  symbol names, paths, signatures and line numbers of your code, so treat the
  directory like the source it was built from.
- **Configuration edits are limited and reversible.** Only `graphmaster
  install` and `uninstall` modify agent configuration files, only in
  user-level locations, with backups under
  `~/.config/graphmaster/backups/`. Hook commands use the absolute path of
  the installed binary.
- **Hooks only read.** Hook processes parse payloads and shell command lines
  but never execute them; their only effects are the agent decision and
  writes to `.graphmaster/`.
- **Untrusted input.** Source files, git metadata, agent payloads and config
  files are treated as untrusted input to parsers and must never cause code
  execution, writes outside `.graphmaster/` (other than install/uninstall), or
  crashes that make a hook block an agent. Reports in these areas are in
  scope.

Out of scope: an attacker who can already write to your home directory or
your agent configuration, and denial of service through intentionally huge
repositories (graphmaster bounds file sizes and output, but indexing a very
large tree still takes time).
