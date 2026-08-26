# Security policy

## Supported versions

Security fixes are provided for the latest release.

## Reporting a vulnerability

Use GitHub private vulnerability reporting. Include the affected version, reproduction steps, and impact without attaching production payloads or credentials.

## Deployment boundary

Webhook Workbench binds to localhost by default. Bearer authentication does not provide encryption; use a trusted TLS reverse proxy for traffic crossing a network. Stored request bodies may contain sensitive application data, so protect the data file or Docker volume and choose retention conservatively.
