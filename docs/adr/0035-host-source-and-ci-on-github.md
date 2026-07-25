# Host source and CI/CD on GitHub

The canonical source repository is `github.com/nkaewam/mrstack`, with the local checkout at `/Users/nonkaewampor/Developer/github.com/nkaewam/mrstack`. GitHub is the development and release host; GitLab remains the runtime system that `mrstack` observes and mutates through the user's local `glab` authentication.

GitHub Actions will run pull-request and main-branch verification and will build tagged release artifacts. Ordinary workflows use scripted GitLab adapters and local bare Git repositories and receive no Agoda GitLab credentials. Live self-managed GitLab contract tests run locally or only on an explicitly approved internal runner, never on an untrusted pull request or ordinary GitHub-hosted runner.

Workflow permissions are least-privilege, third-party actions are pinned to immutable commit SHAs, dependencies are updated through review, and release jobs publish CGO-free macOS/Linux ARM64/AMD64 archives, checksums, and provenance to GitHub Releases.
