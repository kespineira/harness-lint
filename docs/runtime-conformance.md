# Claude Code / Codex runtime conformance matrix

As of 2026-08-14, this matrix describes the current adapters and the
synthetic hook fixtures in this repository. It is a versioned conformance
record, not a claim that Claude Code and Codex expose equivalent telemetry.

The source-of-truth release references are the [Claude Code hooks
reference](https://code.claude.com/docs/en/hooks) and the [Codex hooks
reference](https://learn.chatgpt.com/docs/hooks). The Codex page is the
release-behavior reference: it explicitly warns that schemas on the `main`
branch may contain fields that are ahead of a released Codex build. A field
described by a source page is not automatically a field observed by this
adapter.

## How to read the matrix

Each cell starts with one of these evidence values:

- `observed` means the current parser or adapter returns the stated
  metadata, and a fixture or existing focused test demonstrates it.
- `partial` means only a documented subset or one evidence path is covered.
- `inferred` means the adapter classifies a documented shape or naming pattern;
  it is not independent runtime telemetry.
- `unavailable` means the source or current parser intentionally does not
  expose the signal.
- `unknown` means there is not enough trustworthy evidence to make a claim.

Notes use `docs`, `adapter`, and `fixture` to distinguish documented
semantics, implementation behavior, and synthetic demonstrations. The
matrix describes metadata-only observations. It does not retain prompts,
tool arguments, tool results, transcript bodies, or command output.

## Conformance matrix

| Signal | Claude Code | Codex | Evidence and boundary |
| --- | --- | --- | --- |
| Direct hook envelope and common identity fields | observed — `session_id`, `cwd`, and `hook_event_name` are required by the direct parser; `PostToolUse` and `PostToolUseFailure` fixtures parse | observed — `session_id`, `cwd`, `hook_event_name`, and `PostToolUse` parse; `turn_id` is accepted when present | **docs:** both references document session/cwd/event fields. **adapter/fixture:** direct parsers hash identifiers and use a fixed injected receive time. |
| Built-in/local tool invocation | partial — `PostToolUse` and `PostToolUseFailure` classify a non-empty `tool_name`; `PreToolUse` is documented but outside the direct usage parser | observed — `PostToolUse` covers Bash, `apply_patch`, and other local function tools; fixtures cover `Bash`, `update_plan`, and `apply_patch` | **docs:** Claude documents more tool lifecycle events; Codex documents broad local-function coverage. **fixture:** parser acceptance is one hook event, not complete runtime coverage. |
| MCP tool invocation | observed — names matching `mcp__<server>__<tool>` become `mcp_tool`; `v1-post-tool-use-mcp.json` demonstrates this | observed — the same documented name shape becomes `mcp_tool`; `valid_mcp.json` demonstrates this | **docs:** MCP tools appear as regular tool events in both references. **adapter:** malformed MCP names are not silently treated as valid MCP tools. |
| Skill invocation | observed — model-selected `Skill` PostToolUse requires `tool_input.skill`; direct slash-command expansion is recorded as a `command`, not guessed as a Skill | partial — `tool_name: "Skill"` is accepted only as a generic `tool`; there is no documented Codex Skill-specific hook identity in the current release reference | **fixture:** Claude’s `v1-post-tool-use-skill-model-selected.json` proves the Skill identity path. Codex’s `skill_not_proven.json` intentionally proves only a generic tool call. |
| Skill installed | observed — discovery inventories user/project `SKILL.md` files | observed — discovery inventories configured skill files under its supported roots | **adapter:** an installed file is inventory evidence only. It does not prove that a model received or used the skill. |
| Skill advertised | partial — discovery can report fully advertised, name-only, not advertised, or unknown based on frontmatter and settings overrides | partial — configured skill metadata can be reported, but hook evidence does not prove what was advertised in a live model context | **adapter:** advertisement is a configured/exposure measurement, not a model-context capture. **docs:** Claude’s direct slash-command path is documented as `UserPromptExpansion`; Codex’s hook page does not define a Skill event. |
| Skill loaded/injected | partial — a `UserPromptExpansion` hook can be documented context injection, but the usage event does not prove the loaded body; no loaded event is normalized | unavailable — no current direct hook parser event identifies Skill loading or injected skill content | **docs:** Claude documents `additionalContext` for expansion and session/subagent start; Codex documents `additionalContext` for several lifecycle hooks. **adapter:** neither adapter treats context injection as a Skill-loaded event. |
| Agent/subagent activity | partial — `Agent`/`Task` PostToolUse becomes an `agent` invocation; documented `SubagentStart`/`SubagentStop` payloads are lifecycle evidence and are rejected by the direct usage parser | partial — `spawn_agent` and the documented `Agent` matcher alias become an `agent` invocation; documented `SubagentStart`/`SubagentStop` are not normalized by the direct parser | **docs:** both references document subagent lifecycle fields. **fixture:** Claude subagent lifecycle fixtures and no Codex lifecycle fixture are explicit unsupported/partial boundaries; neither proves injected subagent context. |
| Stable invocation identity | partial — `tool_use_id` is scoped by `session_id`, hashed, and stabilizes success/failure retries; without it, direct-hook identity is unavailable and fallback is conservative | partial — `tool_use_id` is scoped by `session_id` and `turn_id`, hashed, and stabilizes duplicate deliveries; without it, fallback is conservative | **docs:** both references document `tool_use_id`. **fixture:** each manifest has same-ID and distinct-ID entries; the deterministic test requires equal fingerprints for same identity and different fingerprints for distinct identity. |
| Source occurrence timestamp | unavailable for direct hooks — current Claude hook common fields do not document a timestamp, so direct events leave `SourceTimestamp` nil | unavailable for direct hooks — current Codex hook fields do not document a timestamp, so direct events leave `SourceTimestamp` nil | **docs:** neither current release reference lists a direct hook timestamp. **adapter:** transcript/file-import paths may use an explicit source timestamp, but that is a different provenance path and is not promoted into direct-hook evidence. |
| Local receive/observation timestamp | observed — caller-injected `ObservedAt` is the local receive time | observed — caller-injected `ObservedAt` is the local receive time | **adapter/fixture:** tests use a fixed UTC clock; no live clock, `HOME`, or installed runtime is required. |
| Session ID | observed — documented `session_id` is required, SHA-256 normalized, and returned as metadata-only `SessionID` | observed — documented `session_id` is required, SHA-256 normalized, and returned as metadata-only `SessionID`; Codex docs say subagent hooks use the parent session ID | **docs/adapter/fixture:** identity is preserved only as a one-way hash; raw fixture identifiers are asserted absent from normalized output. |
| Project/cwd signal | observed — documented `cwd` is required and SHA-256 normalized as `ProjectID` | observed — documented `cwd` is required and SHA-256 normalized as `ProjectID` | **docs/adapter:** this is a project/cwd signal, not a verified repository identity. Transcript import can use configured project fallback when source context is absent. |
| Invocation origin (model-selected vs user-explicit) | partial — `PostToolUse` is labeled model-selected by current adapter semantics; slash-command `UserPromptExpansion` is labeled user-explicit; the source does not prove all user/model distinctions | unavailable — current direct Codex parser intentionally returns `unknown` for invocation origin | **adapter/fixture:** these labels are normalized classifications, not a runtime audit field. Do not treat them as parity. |
| Claude hook/config locations | partial — discovery reads `~/.claude/settings.json`, project `.claude/settings.json`, and `.claude/settings.local.json`; inventory also reads supported user/project skills, commands, agents, instructions, `.mcp.json`, and `.claude.json` paths | unavailable — not a Codex configuration signal | **docs:** Claude also documents plugin hooks, session hooks, and built-in hooks. **adapter:** those additional sources are not claimed as exhaustively discovered here, and direct parser trust is not inferred from a file. |
| Codex hook/config locations | unavailable — not a Claude configuration signal | partial — discovery reads configured user `hooks.json`/`config.toml` and project `.codex/hooks.json`/`config.toml` paths; plugin/managed sources are not exhaustively observed | **docs:** Codex lists `~/.codex/hooks.json`, `~/.codex/config.toml`, `<repo>/.codex/hooks.json`, `<repo>/.codex/config.toml`, plus plugin and managed sources. **adapter:** injected roots make tests deterministic and do not inspect live `HOME`. |
| Trust behavior | partial — Claude documents workspace trust gating for hooks; this adapter reports configured definitions and never executes them, so runtime trust state is not proven | partial — Codex documents review/trust by current hook-definition hash and that untrusted project hooks are skipped; this adapter reports configuration but does not perform trust review or execution | **docs/adapter:** trust is a runtime decision, not equivalent to installed, advertised, loaded, or invoked. |
| Transcript fallback stability | partial — configured JSONL transcripts provide historical tool identities when explicit timestamps exist; source/session/project fallbacks and tool-use IDs are used conservatively, and file-captured hooks are labeled `import` | partial — configured session records and file-captured PostToolUse records are parsed only at the outer record level; explicit timestamps and known context fields are required, with file/path fallbacks when context is absent | **adapter:** transcript evidence is separate from direct hook evidence. Missing timestamps, ambiguous nested prompt/response objects, and missing tool identities are skipped rather than fabricated. |
| Unsupported or unobservable cases | unavailable/unknown — prompt/response contents, `PreToolUse` as a usage event, MCP prompt expansion, lifecycle-as-invocation, final injected context, and hosted/non-tool signals are not normalized | unavailable/unknown — prompt/response contents, `UserPromptSubmit`, `Stop`, `SubagentStart`/`SubagentStop` as usage events, hosted tools, trust outcomes, and future fields ahead of the release are not normalized | **docs:** event schemas are broader than the adapter’s usage contract. **adapter:** rejection or omission is intentional when identity or occurrence evidence is not trustworthy. |

## Installed, advertised, loaded, invoked are different states

The adapter keeps these states separate:

| State | Meaning in this repository |
| --- | --- |
| Installed | A supported file or configuration definition was discovered. |
| Advertised | Configuration/frontmatter indicates that a capability may be exposed to a model or user; it is not a context dump. |
| Loaded/injected | A runtime event or hook output indicates that context was put into a session/subagent; this is usually partial or unavailable here. |
| Invoked | A supported tool-use event or transcript record names the capability as having run. |

For Claude, installation and configured advertisement are observable for
supported inventory roots, and Skill/Agent invocation is observable when the
documented identity is present. Loading is not inferred from installation or
invocation. For Codex, the adapter can observe generic/local/MCP and agent
tool calls, but the current hook reference and fixtures do not establish a
Codex Skill-specific advertisement, load, or invocation contract. In
particular, `Skill` appearing as a tool name is not promoted to a Skill
capability.

## Versioned synthetic fixtures

The fixture manifests are:

- `testdata/claude/hooks/manifest.json`
- `testdata/codex/hooks/v1/manifest.json`

Each manifest records the runtime, `runtime-conformance/hooks-v1` schema
label, direct official documentation URL, as-of date, release-behavior note,
and policies for `valid`, `malformed`, `unknown_event`, `additive`,
`optional`, and `duplicate` fixtures. Duplicate groups state whether
identity is `same` or `distinct`; this keeps retries separate from legitimate
invocations while making the current fallback policy explicit.

Fixtures contain synthetic identifiers, paths, and `SENTINEL_*` values only.
They contain no user prompts, proprietary code, live transcript content, or
installed-runtime output. The sentinel values are test-only privacy guards:
the conformance test rejects any parser error or normalized event that echoes
them.

`TestRuntimeConformanceFixtureManifests` enumerates every JSON fixture listed
by both manifests and verifies, without reading `HOME` or invoking either
runtime, that:

- accepted fixtures parse and validate as metadata-only events;
- malformed and unknown-event fixtures reject safely;
- additive fields are tolerated and do not change normalized metadata;
- optional fields remain optional and missing identity stays conservative;
- same-ID duplicate/retry fixtures have equal identity/fingerprint while
  distinct-ID fixtures remain distinct.

Existing runtime package tests provide the deeper discovery and transcript
coverage; this manifest test is deliberately narrow and only locks the
versioned direct-hook conformance boundary.
