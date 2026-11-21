# Release preparation

The Registry source address is `vbargl/k3s`. GoReleaser builds protocol 6
binaries, ZIP archives and a checksum file covering the archives and versioned
Registry manifest. The manual Publish workflow builds these unsigned
artifacts.

Dispatch the workflow on `main` with a version such as `0.1.0`. After the
build succeeds, the workflow creates the matching Git tag and a GitHub
Release. The publishing job verifies SHA-256 checksums and signs the checksum
file with the release OpenPGP key. The job verifies the signature against
the primary fingerprint `76963B622D6FE389EB3F1C94EDEF61DC66116445` before
creating the tag on the exact build commit. The release includes the binary
detached signature as `terraform-provider-k3s_VERSION_SHA256SUMS.sig`.
An existing tag, draft or release stops publication.

Only the signing step of the manually dispatched `main` workflow receives
`GPG_PRIVATE_KEY` (ASCII-armored OpenPGP export) and `GPG_PASSPHRASE`.
The step imports the key into a temporary keyring and removes it after
verification. Build artifacts and PR checks never receive these secrets.

If publishing fails after tag creation, use the artifacts from that workflow
run. Verify their checksums and confirm the tag points to that run's commit.
Sign the checksum file with the approved key in the release operator's
environment. Verify the binary signature and inspect any draft and its
assets before completing it. If there is no draft, create the release from
the verified tag with `gh release create --verify-tag` and attach the
verified assets, including the signature.

The Terraform Registry checks release signatures against the public key
registered for the namespace. Registration of the provider and public key
is a separate operator action. GitHub release `v0.1.0` predates automatic
signing.
