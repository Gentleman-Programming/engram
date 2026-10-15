# Local project aliases

Aliases map a candidate key or working directory to a project for **future derivation only**. They do not enroll projects, grant permissions, rename existing records, rewrite history, claim historical work, or backfill delivery. Existing routing is not switched to inbox routing by these commands; that integration is a later slice. `resolve` is a read-only preview, not a delivery operation.

## Commands

```text
usage: engram projects alias set (--key <key>|--cwd <directory>) --project <project>
       engram projects alias list
       engram projects alias remove (--key <key>|--cwd <directory>)
       engram projects alias resolve [--designation <name>] [--cwd <directory>]
```

`engram projects alias --help` prints this help. Set and remove require exactly one selector. Unknown arguments, repeated flags, missing values and invalid project names are errors (stderr prefixed `project alias:`, exit 1). Rejected inputs do not change aliases. Key and target names use the existing strict project-name validation and canonicalization. Cwd selectors use absolute, cleaned paths; they do not guess symlinks or repository ownership. Relative cwd selectors are relative to the process working directory.

```sh
engram projects alias set --key old-name --project team-app
engram projects alias set --cwd /work/scratch --project team-app
engram projects alias list
engram projects alias resolve --designation old-name --cwd /work/scratch
engram projects alias remove --key old-name
engram projects alias remove --cwd /work/scratch
```

Successful set prints `Alias set: key old-name -> team-app` (or `cwd` and the normalized absolute directory). List prints tab-separated `kind`, `key`, `project` rows ordered by kind and key, without a header, or `No project aliases.`. Remove prints `Alias removed.` or `Alias not found.`. All successful commands exit 0 with empty stderr. Aliases persist locally across CLI invocations and are not exported or administered through HTTP/MCP.

## Read-only resolution

The explicit `--designation` takes precedence and skips Git-root observation. Without it, inspection observes only the actual current repository root (including when cwd is nested inside that repository), not the origin remote label, `.engram/config.json` project name, directory basename, historical ownership, or a fabricated designation. A missing or invalid private Git project binding does not block root observation; inspection neither initializes nor rewrites that binding. This root-only preview does not change general project detection elsewhere.

A cwd outside a Git repository has no Git candidate, even when it contains one or multiple child repositories. Children are not promoted and do not cause an ambiguity error in this preview. No candidate means source `unknown`, with an empty candidate key; basename is never a sync key. Cwd aliases still apply after this root-only step. No public `--git-root` flag exists. `--cwd` defaults to the process working directory.

```sh
engram projects alias resolve --cwd /work/current-repo
engram projects alias resolve --cwd /work/non-repo-with-child-repositories
engram projects alias resolve --designation chosen-name --cwd /work/current-repo
```

The first example derives the current repository root's key regardless of its configured project label or private binding. The second previews `unsorted` when no cwd alias exists. The third preserves the explicit designated name instead of deriving a Git key. These examples preview future derivation only; they do not deliver or claim work.

Resolve prints one JSON object followed by a newline, with fields in this order:

- `candidate`: `key`, `source`, original `directory`, original designation `raw_project`.
- `aliases`: resolved `project`, nullable `key_alias` and `cwd_alias` (each has `kind`, `key`, `project`), and `conflict`.
- `conflict`, `enrolled`, `disposition`, `project`.

One alias, or two agreeing aliases, supplies the mapped project. Enrollment is checked against actual local enrollment, without changing it. An enrolled match has disposition `enrolled` and its mapped project. Otherwise disposition is `inbox`, with the original candidate key as `project`, or `unsorted` when no candidate exists. This is a preview of future routing, not a promise that routing is already active.

If key and cwd aliases disagree, `aliases.project` is empty, `conflict` is true, `enrolled` is false, and disposition is `inbox`. Both alias records and raw candidate evidence remain in the output; neither owner is selected, even if one or both are enrolled. An unknown designation remains its designated name in the inbox preview.

The commands do not expose inbox list/claim/drop, delivery lanes, Cloud protocol changes, automatic claims, notifications, or historical reclassification.
