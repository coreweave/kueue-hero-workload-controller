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

1. **Clone the repository** locally:
   ```bash
   git clone https://github.com/coreweave/kueue-hero-workload-controller.git
   cd kueue-hero-workload-controller
   ```
2. **Install dependencies** as described in the [README](./README.md)

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

5. **Keep your branch updated**:
   ```bash
   git fetch origin
   git rebase origin/main
   ```

6. **Push your branch**:
   ```bash
   git push origin feature/your-feature-name
   ```

## 📝 Commit Guidelines

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

3. **Request review** from appropriate team members

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
- Reach out on Slack [#channel-name]
- Contact the maintainers

## 🙏 Thank You!

Your contributions help make kueue-hero-workload-controller better for everyone. We appreciate your time and effort!

