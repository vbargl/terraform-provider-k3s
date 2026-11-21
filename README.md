# terraform-provider-k3s

Terraform provider for K3s cluster coordination in Console.

- [Provider documentation](docs/index.md)
- [Node resource](docs/resources/node.md)
- [Host setup and recovery](host/README.md)
- [Bootstrap example](examples/bootstrap/main.tf)

The executable lives in `cmd/terraform-provider-k3s`. The provider schema,
node lifecycle and SSH transport live in `internal/provider`. The executable
is built with `go build ./cmd/terraform-provider-k3s`.

Run `golangci-lint run`, `go test ./...`,
`nu --no-config-file host/test-coordinate.nu` and `nu --no-config-file tests/run-native.nu` for
local checks. The last command requires Terraform.

GoReleaser packages protocol 6 binaries and computes SHA-256 checksums
for the archives and Registry manifest. Run `goreleaser check` to validate
its configuration or `goreleaser release --snapshot --clean` to build
local packages in `dist/`. The manual workflow uploads unsigned review
artifacts. See the [release preparation guide](docs/release.md) for the
separate signing and publication steps.

See [integration testing](integration/README.md) for the disposable Incus
fixture.
