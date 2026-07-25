# Use glab as the GitLab transport

V1 will access GitLab exclusively through the installed `glab` executable, using structured high-level commands where sufficient and `glab api` for exact REST or GraphQL fields, while never parsing human-formatted output. This delegates Agoda host selection, authentication, TLS, pagination, and self-managed compatibility to the existing tool instead of duplicating them in an SDK; an internal `GitLabClient` boundary keeps a future transport replacement possible.
