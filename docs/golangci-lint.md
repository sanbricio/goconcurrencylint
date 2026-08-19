# Running goconcurrencylint under golangci-lint

goconcurrencylint ships as a [golangci-lint module
plugin](https://golangci-lint.run/plugins/module-plugins/), so a project can run
it from the golangci-lint it already has instead of installing, configuring and
version-pinning a second binary.

A module plugin is compiled into golangci-lint from source. That needs no
approval from upstream and none of the `-buildmode=plugin` fragility of the
older Go plugin system: the result is one static binary that behaves like stock
golangci-lint with this linter added.

Requires golangci-lint v2.

## 1. Declare the plugin

Create `.custom-gcl.yml` next to your `.golangci.yml`:

```yaml
version: v2.12.2 # the golangci-lint version to build on
name: custom-gcl # the binary name
destination: ./bin

plugins:
  - module: github.com/sanbricio/goconcurrencylint
    import: github.com/sanbricio/goconcurrencylint/pkg/golangci
    version: v0.5.0
```

`import` is needed because the plugin lives in a subpackage of the module; it
defaults to `module` otherwise. Pin `version` to a released tag — the plugin
exists from v0.5.0 on.

## 2. Build the binary

```bash
golangci-lint custom
```

This produces `./bin/custom-gcl`. Commit `.custom-gcl.yml`, not the binary, and
rebuild it in CI — the step is a cached `go build`:

```yaml
- uses: actions/setup-go@v6
- run: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
- run: golangci-lint custom
- run: ./bin/custom-gcl run ./...
```

`golangci-lint custom` shells out to the Go toolchain, so the job needs Go on
the runner and a module cache to be worth caching.

## 3. Enable the linter

In `.golangci.yml`:

```yaml
version: "2"

linters:
  enable:
    - goconcurrencylint
  settings:
    custom:
      goconcurrencylint:
        type: module
        description: Detects misuse of sync primitives and channels.
        settings:
          checks:
            - all
            - -GCL5001
```

Then run `./bin/custom-gcl run ./...` where you would have run `golangci-lint
run ./...`.

Listing the linter under `enable` is not strictly required: a module plugin
joins the `standard` group, so with golangci-lint's default `linters.default`
merely configuring it turns it on. Under `linters.default: none` the `enable`
entry is required, and it is worth keeping either way — it makes the linter
visible where a reader looks for the list of what runs.

## Settings

| Key      | Type              | Default        | Meaning                                             |
| -------- | ----------------- | -------------- | --------------------------------------------------- |
| `checks` | list of strings   | every check    | Which checks to run, as an ordered list             |

`checks` is the [`-checks` flag](../README.md#selecting-checks) with one entry
per element. The list is processed in order starting from the empty set: a plain
entry adds what it matches, a `-` prefix removes it, and `all` matches
everything. Entries are canonical codes (`GCL1001`), legacy slugs
(`lock-without-unlock`) or code prefixes (`GCL1*`).

| On the command line             | In `.golangci.yml`                     |
| ------------------------------- | -------------------------------------- |
| `-checks "all,-GCL5001"`        | `checks: [all, -GCL5001]`              |
| `-checks "GCL1*"`               | `checks: [GCL1*]`                      |
| `-checks "all,-GCL2*,GCL2001"`  | `checks: [all, -GCL2*, GCL2001]`       |

A YAML list rather than the flag's comma-separated string, because that is how
golangci-lint writes every other list setting, `staticcheck.checks` included.
Passing the string form is an error that prints the list to write instead.

Omit the key to run every check. Do not write `checks: []`: an absent key
already means "everything", so an empty list is not asking for that, and it is
rejected rather than read as "nothing".

## Failing loudly

Every way of getting the configuration wrong fails the run with exit code 3.
None of them silently disables a check:

```
# a mistyped key
Error: build linters: plugin(goconcurrencylint): newPlugin decoding settings: json: unknown field "chekcs"

# the flag's string form
Error: build linters: plugin(goconcurrencylint): newPlugin decoding settings: "checks" must be a list, one entry per check: write ["all", "-GCL5001"] instead of "all,-GCL5001"

# a list that asks for nothing
Error: build linters: plugin(goconcurrencylint): BuildAnalyzers setting "checks" is an empty list; remove the key to run every check

# a check that does not exist
Error: build linters: plugin(goconcurrencylint): BuildAnalyzers setting "checks": unknown check "GCL9999"; see "goconcurrencylint explain" for the catalogue
```

This is deliberately stricter than golangci-lint's built-in linters. `golangci-lint
config verify` validates first-class settings against a JSON schema, but plugin
settings reach it as an opaque blob, so it cannot check ours — and a check id
that matches nothing is accepted by `staticcheck.checks` in any case. Validating
at load time is the only place a plugin can make a typo fail, and a linter that
quietly stops running is worse than one that refuses to start.

## Things golangci-lint already handles

- **Test files.** Use `run.tests: false`; there is no setting of ours for it.
- **Severity.** `analysis.Diagnostic` carries no severity, so it is a
  golangci-lint concern. Diagnostics start with their code, which
  `severity.rules` can match:

  ```yaml
  severity:
    rules:
      - linters: [goconcurrencylint]
        text: "^GCL5001:"
        severity: info
  ```

- **Path exclusions.** `linters.exclusions.rules`, as for any other linter. The
  inline `// goconcurrencylint:ignore` directive still works and is the right
  tool for one-off exceptions.

## Developing against a local checkout

`path` replaces `version` and points the build at a working tree, so a change
can be tried before it is tagged:

```yaml
plugins:
  - module: github.com/sanbricio/goconcurrencylint
    import: github.com/sanbricio/goconcurrencylint/pkg/golangci
    path: /path/to/goconcurrencylint
```
