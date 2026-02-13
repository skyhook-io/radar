# Contributing to Radar

Thank you for your interest in contributing to Radar! This document provides guidelines and instructions for contributing.

## Code of Conduct

By participating in this project, you agree to abide by our [Code of Conduct](CODE_OF_CONDUCT.md).

## How to Contribute

### Reporting Bugs

Before creating a bug report, please check existing issues to avoid duplicates. When creating a bug report, include:

- A clear, descriptive title
- Steps to reproduce the issue
- Expected behavior vs actual behavior
- Your environment (OS, Go version, Node version, Kubernetes version)
- Relevant logs or screenshots

### Suggesting Features

Feature requests are welcome! Please include:

- A clear description of the feature
- The problem it solves or use case it enables
- Any alternative solutions you've considered

### Pull Requests

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Make your changes
4. Run tests and ensure they pass
5. Commit your changes (see commit message guidelines below)
6. Push to your fork
7. Open a Pull Request

## Development Setup

For detailed architecture, API reference, and release process, see [DEVELOPMENT.md](DEVELOPMENT.md).

### Prerequisites

- Go 1.25+
- Node.js 20+
- npm
- Access to a Kubernetes cluster (minikube, kind, or remote)

### Getting Started

```bash
# Clone your fork
git clone https://github.com/YOUR_USERNAME/radar.git
cd radar

# Install frontend dependencies
cd web
npm install
cd ..

# Run in development mode
# Terminal 1: Backend
go run ./cmd/explorer --dev --no-browser

# Terminal 2: Frontend with hot reload
cd web
npm run dev
```

### Running Tests

```bash
# Backend tests
go test ./...

# Frontend type check
cd web
npm run tsc
```

### Building

```bash
# Full build (frontend + embedded binary)
make build

# Backend only
go build -o radar ./cmd/explorer

# Frontend only
cd web
npm run build
```

## Project Structure

```
├── cmd/explorer/       # CLI entry point
├── internal/
│   ├── k8s/           # Kubernetes client and caching
│   ├── server/        # HTTP server, REST API, SSE
│   └── topology/      # Graph construction logic
├── web/               # React frontend
│   ├── src/
│   │   ├── api/       # API client and hooks
│   │   ├── components/# React components
│   │   └── utils/     # Utilities
│   └── package.json
└── deploy/            # Docker, Helm, Krew configs
```

## Coding Standards

### Go

- Follow standard Go conventions and `gofmt`
- Use meaningful variable and function names
- Add comments for exported functions
- Handle errors explicitly (no silent failures)

### TypeScript/React

- Use TypeScript strict mode
- Prefer functional components with hooks
- Use meaningful component and prop names

### Commits

We follow conventional commits:

```
type(scope): description

[optional body]
```

Types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`

Examples:
- `feat(topology): add support for StatefulSets`
- `fix(sse): handle reconnection on network errors`
- `docs: update installation instructions`

## Review Process

1. All PRs require at least one approval
2. CI checks must pass (tests, build)
3. Keep PRs focused - one feature or fix per PR
4. Respond to review feedback promptly

## Getting Help

- Open an issue for bugs or feature discussions
- Check existing issues and PRs for context

## License

By contributing, you agree that your contributions will be licensed under the Apache 2.0 License.
