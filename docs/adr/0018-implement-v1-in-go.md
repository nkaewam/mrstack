# Implement v1 in Go

V1 will be implemented in Go and distributed as a CGO-free single executable for macOS and Linux on ARM64 and AMD64; Windows is outside the initial support matrix. Go fits the tool's subprocess-heavy `git` and `glab` orchestration, structured JSON contract, SQLite journal, cancellation needs, and testability while avoiding a runtime dependency on the user's relatively old system Python.
