# Security Policy

## Supported Versions

The two most recent minor releases are supported, with older versions receiving updates subject to contributor discretion.
Please also report issues in master, but there are no guarantees of stability there.

## Reporting a Vulnerability

Please report vulnerabilities privately. Do not open a public issue, discussion or pull request for a security problem.

Report through GitHub's private vulnerability reporting: go to the [Security tab](https://github.com/anacrolix/torrent/security) and click **Report a vulnerability**, or open https://github.com/anacrolix/torrent/security/advisories/new directly.
This creates a draft security advisory that only you and the maintainers can see.

If you can't use GitHub's reporting, open an issue asking for a private contact, without any details of the vulnerability.

Useful things to include:

- The affected package(s) and version or commit.
- How an attacker triggers it (for example a malicious peer, tracker, webseed or metainfo file) and what they gain.
- A proof of concept, ideally as a Go test.

You should receive a response within a week, but more than likely a day or two.

## Providing a Fix

Fixes are very welcome, and sending one with your report is the fastest way to get a problem resolved. Please keep fixes private until an advisory is published. Pick whichever of these suits you:

1. **Attach a patch to the report.** Commit your fix locally and run `git format-patch -1` (or `git diff`), then paste or attach the output to the advisory. This needs no setup and is the simplest option.
2. **Open a pull request in the advisory's temporary private fork.** Say in your report that you'd like to push a fix. The maintainer will add you as a collaborator on the advisory and start a [temporary private fork](https://docs.github.com/en/code-security/security-advisories/working-with-repository-security-advisories/collaborating-in-a-temporary-private-fork-to-resolve-a-repository-security-vulnerability). Advisory collaborators can push branches to that fork and open pull requests against it. CI does not run in temporary private forks, so please run `go test ./...` locally before pushing.

Please don't open a fix pull request against the public repository before the advisory is published, since that discloses the vulnerability.

Once a fix is merged, the advisory is published and reporters are credited unless they ask not to be.
