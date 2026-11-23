# arch_ratchet

`make arch-ratchet` fails when a change grows code past the repository's size and
complexity limits. It compares the working tree, including uncommitted and untracked
files, with its merge base: existing over-limit code may shrink or move but never
grow, and new code must meet the limits. The limits mirror the weave qa gate
(clippy's `cognitive_complexity`, `too_many_lines` and `too_many_arguments` defaults,
and its guard's file limit). It also ratchets the `proxy.Service` surface, string
matching on model or provider names, and production-Go comments.

Measurement rules (pinned in `ratchet_test.go`):

- Generated files are not measured.
- A function is keyed by package directory, receiver and name, so moving it between
  files of one package keeps its identity. Helpers in an external test package
  (`foo_test`) are qualified so they cannot reuse the tested package's keys. When a key
  occurs more than once across base and head (build-tag variants in different files),
  each occurrence is qualified with its file so no variant overwrites another.
- Function literals at every depth, including literals nested in other literals, are
  measured as their own scope and keyed by a hash of their rendered source, so
  inserting or editing one literal never re-keys another, and editing a literal's body
  counts as a move. Base and head are measured the same way, so a nested literal that
  moves with its closure into a named function is covered by its base copy. Package-level `var` and `const` literals
  are keyed `<dir>.<var>.func<hash>`.
- Nesting counts an `else if` chain as one level. Lines count only lines holding a
  non-comment token.
- A function new to the diff passes as moved when it claims a distinct removed
  function at least as large on every metric it exceeds, preferring one with the same
  name, otherwise the smallest that covers it. Each removed function covers at most
  one move. Exact moves (same name, same body, same metrics) are paired first, so a
  larger unrelated addition cannot take a moved function's match.
- File totals are summed over the changed files, so moving a line between files is
  neutral and adding one is not.
- The proxy surface counts `proxy.Service`'s fields and methods and the distinct keys
  `internal/proxy` stores with `context.WithValue`, including through an aliased or dot
  import of `context`.
- Model matching counts comparisons of a model- or provider-named value against a
  string literal, the pattern that hard-codes model quirks outside catalog data.
- Baseline growth (`baseline_growth`) compares the architecture debt records at the
  merge base and in the working tree: the atomization ratchet baseline (per ratchet
  and package), the inference-boundary baseline (exception count per `kind`), and the
  `envReadAllowlist`, `proxyImportAllowlist`, `strategyFromContextSites`,
  `strategyRouterWrappers` and `strategyRouteCallSites` literals
  in any Go file of `internal/architecture` (a `map[string]int` contributes its
  values; any other map or slice counts one per entry; keys and counts must be
  self-contained Go constants, or the run fails). A raised count or a new key fails unless `ratchet_exceptions.json`
  holds a `baseline_growth` exception for the subject `<file>#<group>/<key>` (the Go
  allowlists use `internal/architecture#<variable>/<key>`, so moving one between
  files keeps it checked) with a `max` at or above the new count and a reason. A group absent at the merge base is a new
  ratchet recording today's debt and is not compared. Comment-ban budgets grow only
  with a `comment ban budget` exception for the package.
- Paths are read from git with `-z`, because git otherwise quotes and escapes
  non-ASCII names.
