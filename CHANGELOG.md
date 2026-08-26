# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow [Semantic Versioning](https://semver.org/).

## [1.1.0] - 2026-08-26

### Added

- Server-side webhook replay with an in-app response preview.
- Explicit private-network opt-in for local development and self-hosted integrations.
- Replay coverage for request fidelity, sensitive-header filtering, and unsafe targets.

### Security

- Block private, loopback, link-local, credential-bearing, and non-HTTP replay targets by default.
- Revalidate DNS at connection time, ignore proxy environment variables, limit redirects, and cap response previews.

## [1.0.0] - 2026-08-26

### Added

- Multi-channel webhook capture with persistent bounded storage.
- Embedded live inspection UI with payload, header, and cURL views.
- Sensitive-header redaction, body limits, and binary-safe encoding.
- Optional bearer authentication and defensive HTTP security headers.
- Health checks, graceful shutdown, hardened container configuration, and release automation.

[1.1.0]: https://github.com/kyan9400/webhook-workbench/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/kyan9400/webhook-workbench/releases/tag/v1.0.0
