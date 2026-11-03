# Security Policy

## Supported Versions

The two most recent minor releases are supported, with older versions receiving updates subject to contributor discretion.
Please also report issues in master, but there are no guarantees of stability there.

## Reporting a Vulnerability

Please report vulnerabilities privately. Do not open a public issue, discussion or pull request for a security problem.

Report it in either of these ways:

- **GitHub private vulnerability reporting.** Go to the [Security tab](https://github.com/anacrolix/torrent/security) and click **Report a vulnerability**, or open https://github.com/anacrolix/torrent/security/advisories/new directly. This creates a draft security advisory that only you and the maintainers can see.
- **Email** the maintainer at anacrolix@gmail.com.

Useful things to include:

- The affected package(s) and version or commit.
- How an attacker triggers it (for example a malicious peer, tracker, webseed or metainfo file) and what they gain.
- A proof of concept, ideally as a Go test.

You should receive a response within a week, but more than likely a day or two.

## Providing a Fix

Fixes are very welcome, and sending one with your report is the fastest way to get a problem resolved. Please keep fixes private until an advisory is published.

The preferred way is to commit your fix (and ideally a test that reproduces the problem) on top of the latest `master`, then generate patches with `git format-patch`:

```sh
git format-patch origin/master   # one .patch file per commit
```

Attach the `.patch` files to your private report or email. The maintainer applies them with `git am`, which recreates each commit with your name, email, author date and commit message, so you're credited for the fix. Please send `git format-patch` output rather than a plain `git diff`, which loses that information.

If you'd rather work on GitHub, say so in your report. The maintainer can add you as a collaborator on the advisory and start a [temporary private fork](https://docs.github.com/en/code-security/security-advisories/working-with-repository-security-advisories/collaborating-in-a-temporary-private-fork-to-resolve-a-repository-security-vulnerability), where you can push branches and open pull requests. CI does not run in temporary private forks, so please run `go test ./...` locally before pushing.

Please don't open a fix pull request against the public repository before the advisory is published, since that discloses the vulnerability.

Once a fix is merged, the advisory is published and reporters are credited unless they ask not to be.
