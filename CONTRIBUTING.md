# Contributing to kueue-hero-workload-controller

Thank you for considering contributing to kueue-hero-workload-controller! This document provides guidelines and instructions for contributing.

## 📋 Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [Development Workflow](#development-workflow)
- [Commit Guidelines](#commit-guidelines)
- [Pull Request Process](#pull-request-process)
- [Coding Standards](#coding-standards)
- [Testing](#testing)

## 📜 Code of Conduct

By participating in this project, you agree to maintain a respectful and inclusive environment for all contributors.

## 🚀 Getting Started

1. **Fork the repository** on GitHub.

   If you run this controller in production, run it from your own fork. That
   way you control your own infrastructure and release cadence, and can ship
   a fix to your fork immediately. Once the fix is stable, upstream it here
   through a pull request (see [Pull Request Process](#pull-request-process)).

2. **Clone your fork** locally and add this repo as `upstream`:
   ```bash
   git clone https://github.com/<your-org>/kueue-hero-workload-controller.git
   cd kueue-hero-workload-controller
   git remote add upstream https://github.com/coreweave/kueue-hero-workload-controller.git
   ```
3. **Install dependencies** as described in the [README](./README.md)

## 🔄 Development Workflow

1. **Create a feature branch** from `main`:
   ```bash
   git checkout -b feature/your-feature-name
   ```
   
   Branch naming conventions:
   - `feature/` - New features
   - `fix/` - Bug fixes
   - `docs/` - Documentation changes
   - `refactor/` - Code refactoring
   - `test/` - Test additions or modifications

2. **Make your changes** following the [coding standards](#coding-standards)

3. **Test your changes** thoroughly

4. **Commit your changes** following the [commit guidelines](#commit-guidelines)

5. **Keep your branch updated** with upstream `main`:
   ```bash
   git fetch upstream
   git rebase upstream/main
   ```

6. **Push your branch** to your fork and open a pull request against
   `coreweave/kueue-hero-workload-controller` `main`:
   ```bash
   git push origin feature/your-feature-name
   ```

## 📝 Commit Guidelines

### CLA & DCO

All commits submitted must have a Git `Signed-off-by` trailer, signifying
agreement to the terms of the CLA & DCO.

Contributors must agree to the [CoreWeave CLA](./CLA.md) when pushing code
to this project.

Agreement with the CoreWeave CLA must signified by including a
`Signed-Off-By` trailer in every submitted Git commit to this repository.
By signing off, you certify that you have the right to submit the
contribution and that you agree to and are bound by the CoreWeave
Contributor License Agreement in effect at the date of your submission,
found as [`CLA.md`](./CLA.md) in the root of this repository, which governs
your submission. If you are contributing on behalf of an entity, you
further certify that you are authorized to bind that entity to the CLA.

Individual commits can be signed using the `--signoff` option to
[`git commit`](https://git-scm.com/docs/git-commit#Documentation/git-commit.txt---signoff);
or a repo as a whole can use the `commit.signoff` configuration option.

### Licensing (REUSE)

This project is [REUSE](https://reuse.software/)-compliant and licensed
under Apache-2.0 (see [LICENSE](./LICENSE)). Licensing metadata lives in
[REUSE.toml](./REUSE.toml): its aggregate annotation covers every file by
default, so new files need no SPDX header. If you add material under a
different license or copyright, declare it with an inline SPDX header (the
template is in `.reuse/templates/`) or a `REUSE.toml` annotation, and
verify with `reuse lint`.

We follow [Conventional Commits](https://www.conventionalcommits.org/) specification:

### Commit Message Format

```
<type>(<scope>): <subject>

<body>

<footer>
```

### Types

- `feat`: A new feature
- `fix`: A bug fix
- `docs`: Documentation only changes
- `style`: Changes that don't affect code meaning (formatting, whitespace)
- `refactor`: Code change that neither fixes a bug nor adds a feature
- `perf`: Performance improvements
- `test`: Adding or updating tests
- `chore`: Changes to build process or auxiliary tools
- `ci`: Changes to CI configuration

### Examples

```bash
feat(api): add user authentication endpoint

fix(database): resolve connection pool timeout issue

docs(readme): update installation instructions
```

### Breaking Changes

Include `BREAKING CHANGE:` in the footer for changes that break backward compatibility:

```bash
feat(api): redesign user authentication

BREAKING CHANGE: authentication now requires OAuth2 tokens instead of API keys
```

## 🔀 Pull Request Process

1. **Ensure your PR**:
   - Has a clear title and description
   - Links to related Jira tickets or issues
   - Includes tests for new functionality
   - Updates documentation as needed
   - Follows [SemVer](https://semver.org/) principles
   - Passes all CI checks

2. **Fill out the PR template** completely

3. **Request review** from the CoreWeave maintainers. Every change to this
   repository lands through a pull request, and a CoreWeave maintainer must
   approve it before it merges. This applies to fixes upstreamed from forks
   as well.

4. **Address feedback** promptly and professionally

5. **Squash commits** if requested before merging

### PR Title Format

Follow the same format as commit messages:
```
<type>(<scope>): <description>
```

## 💻 Coding Standards

### General Guidelines

- Write clear, readable, and maintainable code
- Follow the existing code style and patterns
- Add comments for complex logic
- Keep functions small and focused
- Use descriptive variable and function names
- Avoid deep nesting

### Language-Specific Standards

[Add language-specific coding standards for your project, e.g.:]

- **Python**: Follow PEP 8
- **JavaScript/TypeScript**: Follow Airbnb style guide
- **Go**: Follow Effective Go guidelines

### Code Review Checklist

- [ ] Code is clean and well-organized
- [ ] No unnecessary complexity
- [ ] Error handling is appropriate
- [ ] Security considerations are addressed
- [ ] Performance implications are considered
- [ ] Code is properly documented

## 🧪 Testing

### Writing Tests

- Write tests for all new functionality
- Maintain or improve code coverage
- Include unit, integration, and e2e tests as appropriate
- Test edge cases and error conditions

### Running Tests

```bash
# Add project-specific test commands
[command to run tests]
```

### Test Coverage

Aim for high test coverage, but focus on meaningful tests rather than achieving a specific percentage.

## 🐛 Reporting Issues

When reporting issues, please include:

- Clear description of the problem
- Steps to reproduce
- Expected vs actual behavior
- Environment details (OS, versions, etc.)
- Relevant logs or error messages
- Screenshots if applicable

## 📞 Questions?

If you have questions or need help:

- Check existing issues and pull requests
- Open a GitHub issue
- Contact the maintainers

## 🙏 Thank You!

Your contributions help make kueue-hero-workload-controller better for everyone. We appreciate your time and effort!

