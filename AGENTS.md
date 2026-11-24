# Notes for coding agents

## Opening pull requests

- Open pull requests from a fork with **Allow edits from maintainers** enabled. With the GitHub API, pass `"maintainer_can_modify": true` when creating the pull request ([docs](https://docs.github.com/en/rest/pulls/pulls#create-a-pull-request)); with the GitHub CLI it is the default for `gh pr create` (just don't pass `--no-maintainer-edit`, [docs](https://cli.github.com/manual/gh_pr_create)). If it can't be enabled (for example the fork is owned by an organization), say so in the pull request description.
- Fill in `.github/pull_request_template.md`.
- See [CONTRIBUTING.md](CONTRIBUTING.md).
