# Development guidelines

- Follow the assignment and my instructions. Make focused changes; do not invent requirements, redesign APIs, or add speculative features. Ask when an ambiguity materially affects behavior.
- Write idiomatic Go supported by the toolchain. Prefer modern standard-library helpers such as `slices.Clone`; remember these are shallow copies.
- Keep code readable: logical blank lines, multiline literals where helpful, clear names, focused functions, and `gofmt`. Avoid walls of code and unnecessary abstractions.
- Separate CLI wiring, API helpers, entity-specific fetching, polling, and storage. Validate configuration in constructors, not fetching methods. Inject dependencies.
- Put `context.Context` first and propagate cancellation through requests, pagination, limiter waits, and retries. Preserve cancellation errors during body decoding.
- Keep single-attempt fetching separate from bounded retries. Every attempt must use the shared limiter.
- Protect shared state; copy snapshots while locked, then unlock before I/O. Publish files atomically. Never overwrite good data with incomplete results.
- Write clear, deterministic tests for behavior, failures, cancellation, and regressions. Use synthetic credentials and local servers.
- Review changes before finishing. Run formatting, vet, tests, race tests when relevant, and build. Use the go-verify skill for that.
- Never log tokens. Keep documentation concise and accurate. Summarize changes and verification briefly.
