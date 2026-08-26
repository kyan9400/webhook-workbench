# Contributing

1. Fork the repository and create a focused branch.
2. Make the smallest coherent change.
3. Run `gofmt -w cmd internal`, `go vet ./...`, and `go test -race ./...`.
4. Build the container when changing runtime or UI behavior.
5. Open a pull request describing the problem, approach, and verification.

Do not use real webhook secrets or customer payloads in examples, tests, screenshots, or issues. New stored fields must be considered for redaction and backward compatibility.
