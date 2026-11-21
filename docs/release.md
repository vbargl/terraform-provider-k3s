# Release preparation

The Registry source address is `vbargl/k3s`. GoReleaser builds protocol 6
binaries, ZIP archives and a checksum file covering the archives and versioned
Registry manifest. The manual Publish workflow builds these unsigned
artifacts.

Dispatch the workflow on `main` with a version such as `0.1.0`. After the
build succeeds, the workflow creates the matching Git tag and a GitHub
Release. The publishing job
checks the downloaded assets against their SHA-256 sums and attaches them
to the release. The tag targets the exact commit that built the assets.
The workflow refuses an existing tag or release. If an interrupted upload
leaves a draft, inspect its assets before resuming it manually.

GitHub assets from this workflow are unsigned.

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
