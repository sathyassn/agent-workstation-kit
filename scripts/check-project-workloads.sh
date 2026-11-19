#!/usr/bin/env bash
# Operate within the actual agent-owned graphical session, never sudo -u.
set -Eeuo pipefail
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck disable=SC1091
source "$script_dir/lib/common.sh"
agent_account=''
project=''
image=''
start_orbstack=false
check_only=false
engine_timeout=60
while (($#)); do
  case "$1" in
    --agent) agent_account=${2:?Missing agent}; shift 2 ;;
    --project) project=${2:?Missing project}; shift 2 ;;
    --container-image) image=${2:?Missing pinned image}; shift 2 ;;
    --start-orbstack) start_orbstack=true; shift ;;
    --apply) APPLY_CHANGES=true; shift ;;
    --check) check_only=true; shift ;;
    --engine-timeout) engine_timeout=${2:?Missing timeout}; shift 2 ;;
    --help|-h)
      printf '%s\n' 'Usage: check-project-workloads.sh --agent USER [--project DIR] [--container-image NAME@sha256:DIGEST] [--start-orbstack] [--engine-timeout SECONDS] [--check|--apply]'
      exit 0 ;;
    *) die "Unknown option: $1" ;;
  esac
done
host_os=$(uname -s)
[[ "$host_os" == Darwin || "$host_os" == Linux ]] || die 'Ubuntu/macOS project workloads only.'
validate_unix_name "$agent_account"
[[ ${EUID:-$(id -u)} -ne 0 && $(id -un) == "$agent_account" ]] || die 'Run directly as the declared agent account; never root or sudo -u into another GUI session.'
[[ "$check_only" != true || "$APPLY_CHANGES" != true ]] || die '--check and --apply are exclusive.'
[[ "$engine_timeout" =~ ^[0-9]{1,3}$ ]] || die 'Engine timeout must be from 0 to 300 seconds.'
engine_timeout=$((10#$engine_timeout))
((engine_timeout <= 300)) || die 'Engine timeout must be from 0 to 300 seconds.'
if [[ -n "$image" ]]; then
  [[ "$image" =~ ^[a-zA-Z0-9][a-zA-Z0-9._:/-]*@sha256:[a-f0-9]{64}$ ]] || die 'Select a reviewed container image pinned by sha256 digest.'
fi
if [[ -n "$project" ]]; then
  [[ -d "$project" && -x "$project/node_modules/.bin/playwright" ]] || die 'Project must already contain its locked Playwright dependency.'
  [[ -f "$project/package-lock.json" || -f "$project/pnpm-lock.yaml" || -f "$project/yarn.lock" || -f "$project/bun.lock" || -f "$project/bun.lockb" ]] || die 'A project dependency lock is required.'
  project=$(cd "$project" && pwd)
fi
if [[ "$host_os" == Darwin ]]; then
  [[ -d '/Applications/OrbStack.app' ]] || die 'Install the selected OrbStack cask through the packages scope first.'
  command_exists docker || die 'Docker CLI is unavailable; finish the selected OrbStack setup as this user.'
  engine=(docker --context orbstack)
  if [[ "$start_orbstack" == true ]]; then
    [[ $(stat -f %Su /dev/console) == "$agent_account" ]] || die 'Start OrbStack only inside the intended active graphical user session.'
    run open -a OrbStack
  fi
else
  [[ "$start_orbstack" != true ]] || die '--start-orbstack applies only to macOS.'
  command_exists podman || die 'Rootless Podman is missing; complete the approved machine scope first.'
  engine=(podman --remote=false)
fi
if [[ "$check_only" == true || "$APPLY_CHANGES" == true ]]; then
  if [[ "$host_os" == Linux ]]; then
    rootless=$("${engine[@]}" info --format '{{.Host.Security.Rootless}}') || die 'Cannot inspect Podman; verify the agent account and subordinate ID ranges.'
    [[ "$rootless" == true ]] || die 'Container smoke requires rootless Podman; never use sudo or the Docker group.'
  else
    deadline=$SECONDS
    if [[ "$start_orbstack" == true ]]; then deadline=$((SECONDS + engine_timeout)); fi
    waiting=false
    until "${engine[@]}" info >/dev/null 2>&1; do
      ((SECONDS < deadline)) || die 'OrbStack engine is not ready. Complete its license/privacy prompts in this session, then resume. Use --start-orbstack only in the actual graphical owner session.'
      if [[ "$waiting" != true ]]; then log 'Waiting for OrbStack; finish any native license/privacy prompts in the active agent session.'; waiting=true; fi
      sleep 1
    done
  fi
else
  log 'PREVIEW verify the account-owned container engine after apply; global Docker context is preserved.'
fi
if [[ -n "$project" ]]; then
  if [[ "$check_only" != true ]]; then
    run "$project/node_modules/.bin/playwright" install chromium
  fi
  browser_probe='let p; for (const name of ["@playwright/test","playwright","playwright-core"]) { try { p=require(name); break; } catch(e) { if(e.code!=="MODULE_NOT_FOUND") throw e; } } if(!p) throw Error("project Playwright missing"); (async()=>{const b=await p.chromium.launch({headless:true}); try {const page=await b.newPage(); await page.setContent("<title>workstation-browser-ok</title>"); if(await page.title()!=="workstation-browser-ok") throw Error("browser smoke failed"); console.log("workstation-browser-ok");} finally {await b.close();}})().catch(e=>{console.error(e.message);process.exitCode=1;});'
  if [[ "$check_only" == true ]]; then
    (cd "$project" && node -e "$browser_probe")
  else
    (cd "$project" && run node -e "$browser_probe")
  fi
fi
if [[ -n "$image" && "$check_only" != true ]]; then
  run "${engine[@]}" run --pull=missing --rm --network none --read-only --cap-drop ALL \
    --security-opt no-new-privileges --entrypoint /bin/sh "$image" -c 'printf "workstation-container-ok\n"'
fi
log 'Project browser preview, headed tests, signing and privacy decisions remain separate acceptance checks.'
