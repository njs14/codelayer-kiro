# codelayer-kiro

> **Fork notice**: This is a fork of [humanlayer/humanlayer](https://github.com/humanlayer/humanlayer) (CodeLayer) that adds [Kiro CLI](https://kiro.dev) as an alternative AI coding agent backend.

## What was added

This fork integrates Kiro CLI support alongside the existing Claude Code backend:

- **`kirocli-go/`** — New Go SDK for Kiro's Agent Communication Protocol (ACP). See [`kirocli-go/README.md`](./kirocli-go/README.md).
- **`hld/session/kiro_wrapper.go`** — Kiro session adapter in the daemon.
- **WUI + CLI** — Provider selection dropdown, Kiro model picker, and Kiro event rendering in `humanlayer-wui/` and `hlyr/`.
- **Integration tests** — Mock ACP server and e2e tests in `hld/e2e/`.

Users can switch between Claude Code (default) and Kiro via an environment variable or config file. See [CONTRIBUTING.md](./CONTRIBUTING.md) for details.

## Original project

For full documentation on CodeLayer and the HumanLayer SDK, see the [upstream repository](https://github.com/humanlayer/humanlayer).

## Quick Start

```bash
make setup

# Run with Claude Code (default)
make codelayer-dev

# Run with Kiro
CODELAYER_PROVIDER=kiro make codelayer-dev
```

## License

Apache 2. See the [upstream repository](https://github.com/humanlayer/humanlayer) for full license details.
