#!/bin/bash
# Engram — Shared helpers for Codex hooks
# WARNING: Do not read from stdin here — scripts source this before reading their hook input.

# Detect project name from git remote, with fallbacks.
# Priority: git remote origin repo name > git root basename > cwd basename
detect_project() {
  local dir="$1"

  # Try git remote origin URL
  local url
  url=$(git -C "$dir" remote get-url origin 2>/dev/null)
  if [ -n "$url" ]; then
    # Handles both SSH (git@github.com:user/repo.git) and HTTPS (https://github.com/user/repo.git)
    local name
    name=$(echo "$url" | sed 's/\.git$//' | sed 's|.*[/:]||' | tr '[:upper:]' '[:lower:]')
    if [ -n "$name" ]; then
      echo "$name"
      return
    fi
  fi

  # Fallback: git root directory name (works in worktrees)
  local root
  root=$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null)
  if [ -n "$root" ]; then
    basename "$root" | tr '[:upper:]' '[:lower:]'
    return
  fi

  # Final fallback: cwd basename (current behavior)
  basename "$dir" | tr '[:upper:]' '[:lower:]'
}
