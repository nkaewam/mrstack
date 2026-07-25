# Publish restacks atomically

Every restack, including a one-branch restack, will publish in one `git push --atomic` transaction with an explicit `--force-with-lease=<ref>:<expected-sha>` for every affected branch. Either every rewritten layer is published against the captured snapshot or none is; v1 will treat missing server support for atomic pushes as a failed prerequisite rather than fall back to a different publication path.
