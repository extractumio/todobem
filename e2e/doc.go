// Package e2e is the release test: real binaries of a published release, downloaded from
// GitHub the way a user gets them, run as real processes — install, a hub and an agent paired
// over TLS, an upgrade from the previous release, a migration, a rollback and the refusals. No
// mocked HTTP, no test hooks in the binary. Each case runs in its own HOME, so it touches
// nothing of the account it runs under. The release orchestrator compiles it with
// `go test -c -tags e2e` and runs it on every target before a release is promoted:
//
//	./e2e.test -test.v -test.timeout=30m -release v0.1.1 -previous v0.1.0 -repo owner/name -dist DIR
//
// DIR holds next/ and broken/ (archive + SHA256SUMS each): a build of the same commit one patch
// ahead with a test migration, and an archive whose "binary" cannot run.
package e2e
