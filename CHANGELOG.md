# Changelog

All notable changes to ClawdChan are documented here. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project aims to follow [Semantic Versioning](https://semver.org/)
once a v1.0.0 is cut.

## [Unreleased]

### Added
- Top-level `CHANGELOG.md`.
- `.editorconfig`, `.gitattributes`, and `.github/dependabot.yml` for
  consistent formatting and automated dependency bumps.
- Inlined Code of Conduct pointing at Contributor Covenant 2.1.
- README now shows `go install` as the primary install path and
  advertises `clawdchan doctor` for verification.

### Changed
- Go module path lowercased: `github.com/agents-first/clawdchan`
  (was `github.com/agents-first/ClawdChan`). GitHub resolves the repo
  URL case-insensitively, so existing `git clone` URLs keep working.
- `clawdchan setup -y` (and non-TTY runs) now default `-cc-mcp-scope`
  and `-cc-perm-scope` to `user` — scripted installs actually wire
  Claude Code instead of silently skipping. Pass `=skip` to opt out.
- Windows data directory defaults to `%AppData%\clawdchan` via
  `os.UserConfigDir` instead of the hidden `~/.clawdchan`.
- `clawdchan-mcp` no longer exits when config is missing. It serves a
  single stub tool that tells the agent to run `clawdchan setup`, so
  the failure mode is visible inside Claude Code instead of in a
  swallowed stderr stream.

### Fixed
- README footer no longer ends in a dangling `> Created by ` line.

## [0.1.0] — unreleased

Initial public slice. See commits from the `main` branch before the
first tag for implementation history.
