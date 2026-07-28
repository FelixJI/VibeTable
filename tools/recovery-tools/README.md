# Recovery tool build lock

This module is the single source of truth for the exact Kopia and age command
versions shipped in the Windows release. `scripts/build_next.py` builds the
three commands from this module with `-mod=readonly`, verifies the committed
module sums and embedded Go build metadata, and records the result in the
published recovery-tool provenance.

Update `go.mod` and `go.sum` together. A recovery-tool upgrade requires a
release review of the resulting binaries, SBOM, licenses, package size and
interoperability tests.
