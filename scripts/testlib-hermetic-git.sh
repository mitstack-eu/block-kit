#!/usr/bin/env bash
# testlib-hermetic-git.sh — isolate a shell test's scratch git repository from
# whatever git environment the caller happens to be running under (MIT-1838).
#
# Usage: source this file, then call `hermetic_git_env` once before creating any
# scratch repository:
#
#   . "$(dirname "${BASH_SOURCE[0]}")/testlib-hermetic-git.sh"
#   hermetic_git_env
#
# Why this exists. A suite that runs `git init` in a `mktemp -d` looks isolated
# and is not. Git reads a dozen environment variables that redirect where a
# repository keeps its objects, index and config, and a CI runner sets several
# of them — the checkout action also installs `includeIf.gitdir` rules in the
# global config. When object storage is redirected out from under a fresh repo,
# a blob written by `git add` is unreadable when the commit tries to build its
# tree, which surfaces as:
#
#   error: invalid object 100644 <sha> for 'path'
#   error: Error building trees
#
# go-coverage-floor_test.sh failed exactly that way on CI on 2026-08-14 while
# passing twelve of twelve locally, and it blocked a pull request whose diff
# touched neither coverage nor that test. A result that depends on where it runs
# is not a result.
#
# This is the fifth instance of the family MIT-1495 collected — tests that
# quietly depend on ambient environment, /tmp markers, or git config. The helper
# is shared rather than inlined so the next suite that needs a scratch repo
# inherits the isolation instead of rediscovering the failure.
#
# `check-hermetic-git-tests.sh` fails the build if a suite creates a repository
# without calling this, so the guarantee cannot quietly lapse.

# hermetic_git_env clears every variable that could point git at storage or
# config outside the repository it is about to create, and pins the global and
# system config to empty files. Safe to call more than once.
hermetic_git_env() {
  unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR \
        GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES \
        GIT_CEILING_DIRECTORIES GIT_NAMESPACE GIT_INDEX_VERSION \
        GIT_ATTR_NOSYSTEM GIT_CONFIG GIT_CONFIG_COUNT

  # /dev/null rather than unset: an unset GIT_CONFIG_GLOBAL falls back to
  # ~/.gitconfig, which is where a runner's includeIf rules live.
  export GIT_CONFIG_GLOBAL=/dev/null
  export GIT_CONFIG_SYSTEM=/dev/null
  export GIT_CONFIG_NOSYSTEM=1

  # Commits in a scratch repo must not depend on the caller having an identity
  # configured, nor on a signing key that will not be present on a runner.
  export GIT_AUTHOR_NAME="${GIT_AUTHOR_NAME:-mitstack test}"
  export GIT_AUTHOR_EMAIL="${GIT_AUTHOR_EMAIL:-test@mitstack.invalid}"
  export GIT_COMMITTER_NAME="${GIT_COMMITTER_NAME:-mitstack test}"
  export GIT_COMMITTER_EMAIL="${GIT_COMMITTER_EMAIL:-test@mitstack.invalid}"
}
