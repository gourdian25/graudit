# Security Policy

## Supported Versions

Security fixes are applied to the latest released minor version.

| Version | Supported |
|---------|-----------|
| 0.1.x   | ✅        |
| < 0.1   | ❌        |

## Reporting a Vulnerability

Please report suspected vulnerabilities privately via
[GitHub Security Advisories](https://github.com/gourdian25/graudit/security/advisories/new)
rather than opening a public issue.

Include:

- A description of the issue and its impact
- Steps or a proof-of-concept to reproduce
- Affected version(s)

You can expect an acknowledgment within a week. Once a fix is available, the
advisory will be published together with a patched release.

## Scope Notes

graudit is an append-only, hash-chained audit trail library. Read this
before relying on it for any compliance or security posture:

- **Hash-chaining proves internal consistency only.** `Verify()` detects
  whether any recorded entry was altered or removed after the fact — it
  does **not** prove *who* wrote a given entry beyond whatever `ActorID`
  the caller supplied, and it does **not** protect against a privileged
  attacker with direct database access regenerating the *entire* chain
  from scratch. A hash chain detects partial tampering, not wholesale
  regeneration by someone who controls the storage. Do not present
  "tamper-evident" as "cryptographically un-forgeable" to any auditor or
  compliance stakeholder relying on this library.
- **No cryptographic signatures.** There is no per-entry digital signature
  or true non-repudiation in v1 — see the roadmap in
  `docs/plan/graudit-plan.md`.
- **`AuditEvent.Payload` is not encrypted or redacted.** graudit does not
  inspect, encrypt, or redact `Payload`; do not put secrets there unless
  every downstream consumer of `Query()` results and the underlying
  database are trusted to handle them appropriately.
- **grevents publish is a side channel, not a security boundary.** A
  published `TopicAuditRecorded` event carries the full recorded
  `AuditEvent`, including `Payload` — anything subscribed to that topic
  (in-process, per grevents' own scope) receives the same data a direct
  `Query()` caller would.
- **The Postgres and Mongo backends require correct deployment
  configuration to provide their safety guarantees**: Postgres's
  `pg_advisory_xact_lock`-based serialization assumes all writers go
  through this library talking to the same database; Mongo's transaction-
  based serialization requires a replica-set deployment (construction
  fails fast otherwise) — running against a misconfigured deployment that
  bypasses these paths (e.g. direct inserts into `graudit_entries` from
  another tool) is indistinguishable from tampering as far as `Verify()`
  is concerned, which is the intended detection behavior, not a bug to
  work around.
