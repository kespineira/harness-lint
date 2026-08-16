# Security policy

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting for
[`kespineira/harness-lint`](https://github.com/kespineira/harness-lint/security/advisories/new).
Do not open a public issue for an exploitable vulnerability or include
secrets, private configuration, or an unredacted database in a report. If the
private-reporting link is unavailable, use the repository Security tab to
start a private vulnerability report.

Include the affected version or commit, impact, reproduction steps using
sanitized or synthetic data, and any proposed mitigation. The maintainer may
ask for additional details through GitHub's private advisory channel.

## Supported versions

This project is pre-1.0. Security support is best effort, with no promise of
backports or a fixed response time. Please test and report against the latest
published `v0.x` release (and mention whether current `main` is affected);
older pre-1.0 releases should be upgraded before investigation whenever
possible.

## Local data sensitivity

`harness-lint` is local and metadata-only, but its SQLite database, reports,
runtime configuration, and hook files can reveal local paths, capability
names, enabled/advertised state, hashes, timing, and other environment
metadata. Treat them as sensitive. The tool is designed not to retain or
upload prompts, responses, source text, tool arguments/results, or MCP
payloads, and it has no telemetry service; this does not make local files
safe to publish.

When sharing a diagnostic, prefer a synthetic fixture and redact paths,
project names, capability names, configuration contents, and database files.
