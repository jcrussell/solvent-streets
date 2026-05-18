# Security Policy

## Reporting a Vulnerability

Please report suspected security vulnerabilities privately, **not** via
public GitHub issues, discussions, or pull requests.

Preferred channels, in order:

1. [GitHub private vulnerability reporting](https://github.com/jcrussell/solvent-streets/security/advisories/new)
   — the "Report a vulnerability" button on the repository's Security tab.
2. Email **joncrussell@gmail.com** with the subject prefix `[pvmt security]`.
   PGP is not required; if you need an encrypted channel, mention it in
   your first message and we'll arrange one.

When reporting, please include:

- The affected version (release tag or commit SHA).
- A description of the vulnerability and its impact.
- Steps to reproduce, ideally as a minimal `pvmt` invocation or test
  case.
- Whether the issue is already public anywhere.

## Disclosure Expectations

- You will receive an acknowledgement within **5 business days** of your
  report.
- We aim to provide an initial assessment (accept / need-more-info /
  decline) within **14 days**, and to ship a fix or mitigation within
  **90 days** of acknowledgement for accepted reports.
- We coordinate disclosure with the reporter. Public advisories are
  published via [GitHub Security Advisories](https://github.com/jcrussell/solvent-streets/security/advisories)
  once a fix is released, and credit the reporter unless they request
  anonymity.
- Please give us a reasonable window to ship a fix before disclosing
  publicly. If we miss the 90-day target without an agreed extension,
  you are free to disclose.

## Scope

In scope:

- The `pvmt` CLI and the Go modules under this repository.
- The static site generator (`pvmt export`) and the live server
  (`pvmt serve`), including the embedded WASM forecast.

Out of scope:

- Vulnerabilities in third-party data sources (OpenStreetMap, ArcGIS
  feature servers, GeoJSON files supplied to `pvmt ingest`). Report
  those upstream.
- Issues that require a malicious `pvmt.toml` or local SQLite database
  controlled by the attacker; `pvmt` trusts its own configuration and
  on-disk state by design.
- Denial of service from oversized inputs to local commands run by the
  invoking user.
