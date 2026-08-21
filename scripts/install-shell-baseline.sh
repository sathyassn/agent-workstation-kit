#!/usr/bin/env bash
# shellcheck disable=SC2034

set -Eeuo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(cd "$script_dir/.." && pwd)
# Path is resolved from this script at runtime.
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"

antidote_ref=''
while (($#)); do
  case "$1" in
    --apply) APPLY_CHANGES=true; shift ;;
    --antidote-ref) antidote_ref=${2:?Missing Antidote release/tag}; shift 2 ;;
    --help|-h)
      printf 'Usage: install-shell-baseline.sh --antidote-ref TAG [--apply]\n'
      exit 0
      ;;
    *) die "Unknown option: $1" ;;
  esac
done

[[ ${EUID:-$(id -u)} -ne 0 ]] || die 'Run as the target non-root account.'
[[ -n "$antidote_ref" ]] || die 'Specify a reviewed Antidote release with --antidote-ref.'
[[ "$antidote_ref" =~ ^[A-Za-z0-9._/-]+$ ]] || die 'Invalid Antidote ref.'

if [[ -e "$HOME/.antidote" && ! -d "$HOME/.antidote/.git" ]]; then
  die "$HOME/.antidote exists but is not the expected Git checkout."
fi

antidote_tmp=''
cleanup_antidote_tmp() {
  local exit_status=$?
  trap - EXIT
  if [[ "$APPLY_CHANGES" == true && -n "$antidote_tmp" && -d "$antidote_tmp" ]]; then
    rm -rf -- "$antidote_tmp"
  fi
  exit "$exit_status"
}
trap cleanup_antidote_tmp EXIT

if [[ ! -d "$HOME/.antidote/.git" ]]; then
  if [[ "$APPLY_CHANGES" == true ]]; then
    antidote_tmp=$(mktemp -d "$HOME/.antidote.tmp.XXXXXX")
  else
    antidote_tmp="$HOME/.antidote.tmp.REVIEW"
  fi
  run git clone --filter=blob:none --no-checkout https://github.com/mattmc3/antidote.git "$antidote_tmp"
  run git -C "$antidote_tmp" fetch --depth 1 origin "$antidote_ref"
  run git -C "$antidote_tmp" checkout --detach FETCH_HEAD
  run mv -- "$antidote_tmp" "$HOME/.antidote"
  antidote_tmp=''
else
  if git -C "$HOME/.antidote" rev-parse --verify --quiet "$antidote_ref^{commit}" >/dev/null; then
    expected_commit=$(git -C "$HOME/.antidote" rev-parse "$antidote_ref^{commit}")
    current_commit=$(git -C "$HOME/.antidote" rev-parse HEAD)
    [[ "$current_commit" == "$expected_commit" ]] || \
      die "Existing Antidote checkout is $current_commit, not approved commit $expected_commit. Review the upgrade manually."
  else
    die "Existing Antidote checkout cannot resolve approved ref $antidote_ref. Fetch and review it manually."
  fi
  log "Existing Antidote checkout matches approved commit $current_commit."
fi

declare -A managed_sources=(
  ["$HOME/.zshrc"]="$repo_dir/dotfiles/zshrc.managed"
  ["$HOME/.zsh_plugins.txt"]="$repo_dir/dotfiles/zsh_plugins.txt"
)
for destination in "${!managed_sources[@]}"; do
  if [[ -e "$destination" ]] && ! cmp -s "${managed_sources[$destination]}" "$destination"; then
    if [[ "$APPLY_CHANGES" == true ]]; then
      die "$destination already exists and differs from the managed baseline. Merge it, then rerun."
    fi
    warn "$destination exists and differs; apply will stop until it is reviewed and merged."
  fi
done

if [[ ! -e "$HOME/.zshrc" ]]; then
  run install -m 0644 "$repo_dir/dotfiles/zshrc.managed" "$HOME/.zshrc"
fi
if [[ ! -e "$HOME/.zsh_plugins.txt" ]]; then
  run install -m 0644 "$repo_dir/dotfiles/zsh_plugins.txt" "$HOME/.zsh_plugins.txt"
fi

log 'Open a new Zsh session and verify completion, suggestions, highlighting, history, and mise activation.'
