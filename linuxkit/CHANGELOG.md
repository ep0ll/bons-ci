# Changelog

## [Unreleased]
### Added
- Initial release of bonnie-cicd LinuxKit image
- Linux 6.6 LTS kernel with 200+ hardened Kconfig options
- nydus-snapshotter (shared-daemon mode, 20 GiB blob cache)
- stargz-snapshotter (eStargz/SOCI, 16 MiB prefetch chunks)
- QEMU binfmt for arm64, riscv64, s390x, ppc64, mips cross-arch builds
- buildkitd (rootless-capable, 10 GiB GC limit)
- bonnie proprietary build system integration (gRPC + Prometheus)
- node-exporter with CPU/mem/disk/net/hugepages/PSI collectors
- Full security stack: KASLR, PTI, Retpoline, CFI, SCS, INIT_ON_FREE
- Lockdown=integrity, Landlock, Yama, BPF LSM
- AppArmor profiles for bonnie and containerd
- seccomp allowlist (runtime/seccomp-bonnie.json)
- OCI spec hardening baseline (runtime/oci-spec-patch.json)
- IMA measurement + appraisal policy (runtime/ima-policy)
- BBR + FQ TCP stack tuning (sysctl drop-ins)
- All images pinned by sha256 digest
- SBOM generation and build-provenance attestation
- cosign keyless signing via Sigstore
- Boot waterfall benchmark (make benchmark)
- In-VM security posture checker (make verify)
- Digest pinning helper (make pin / scripts/pin-digests.sh)
- GitHub Actions 6-job CI pipeline (lint, build, smoke, scan, publish, release)
