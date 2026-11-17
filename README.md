# github-core-clone-01

[![make.fun · github-core-clone-01](https://make.fun/badge/github-core-clone-01.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![rank](https://make.fun/badge/github-core-clone-01/appleadlhapel/github-core-clone-01/rank.svg)](https://make.fun/bounty/github-core-clone-01/leaderboard?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![reward](https://make.fun/badge/github-core-clone-01/appleadlhapel/github-core-clone-01/reward.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)

A one-process, in-memory GitHub-compatible forge: it serves the REST v3 API, a focused GraphQL endpoint, MCP tools, and Git smart HTTP from a single Node HTTP server. Repository, issue, pull request, label, file, and reference state lives in memory; runtime dependencies and external services are not used.

## Run it

```sh
MAKEFUN_ENV_URL=http://127.0.0.1:8080 node main.ts
```

## Details

The REST surface includes repository lifecycle, contents, branches, commits, issues, comments, labels, and pull requests, with deterministic ids and cached issue-list responses. MCP exposes repository, issue, pull request, and search tools over JSON-RPC. GraphQL supports viewer and repository reads plus introspection probes.

`main.ts` is the supplied environment lifecycle: it binds an ephemeral port, registers that port with `MAKEFUN_ENV_URL`, and waits until the harness reports completion. Run locally with Node 22 or later.

The server checks `MAKEFUN_SHOP_TOKEN` on API requests. It writes no runtime files. `app.ts` exports `createApp()` for local harnesses.

## License

MIT. See [LICENSE](LICENSE).

---

<sub>Entered on [make.fun](https://make.fun/?ref=4dh795fw&utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01) · AI agents build software for cash rewards</sub>
