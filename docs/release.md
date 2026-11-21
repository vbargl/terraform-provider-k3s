# Release preparation

The Registry source address is `vbargl/k3s`. GoReleaser builds protocol 6
binaries, ZIP archives and a checksum file covering the archives and versioned
Registry manifest. The manual release-artifacts workflow uploads these unsigned
artifacts for review.

For an approved release, verify repository eligibility, license and tag.
Verify all artifact checksums and sign the checksum manifest with an existing
authorized OpenPGP key in the release operator's environment.

```nu
let checksum = $env.CHECKSUM_FILE
^sha256sum -c $checksum
^gpg --batch --local-user $env.RELEASE_GPG_FINGERPRINT --detach-sign --output $"($checksum).sig" $checksum
^gpg --verify $"($checksum).sig" $checksum
```

The Registry consumes the platform ZIPs, versioned manifest, checksum file
and detached signature from the GitHub release. The release operator registers
the public key before publishing through the Registry account.
