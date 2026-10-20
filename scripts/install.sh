#!/usr/bin/env bash
#
# Interactive first-time setup for macOS and Linux.
#
#   ./scripts/install.sh               check dependencies, write .env, build, start under PM2
#   ./scripts/install.sh --no-start    stop after writing .env
#   ./scripts/install.sh --reconfigure regenerate .env even if one exists (the old one is backed up)
#
# Re-running is safe: an existing .env is kept unless --reconfigure is given,
# and deployment is delegated to scripts/deploy.sh.

set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

min_go=1.26.9
min_node=22

start=1
reconfigure=0
for arg in "$@"; do
	case "$arg" in
	--no-start) start=0 ;;
	--reconfigure) reconfigure=1 ;;
	-h | --help)
		sed -n '3,10p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "install: unknown argument: $arg" >&2
		exit 2
		;;
	esac
done

if [[ -t 1 ]]; then
	bold=$'\033[1m' red=$'\033[31m' green=$'\033[32m' yellow=$'\033[33m' reset=$'\033[0m'
else
	bold='' red='' green='' yellow='' reset=''
fi

step() { printf '\n%s==> %s%s\n' "$bold" "$*" "$reset"; }
ok() { printf '  %s✓%s %s\n' "$green" "$reset" "$*"; }
warn() { printf '  %s!%s %s\n' "$yellow" "$reset" "$*"; }
die() {
	printf '%sinstall: %s%s\n' "$red" "$*" "$reset" >&2
	exit 1
}

# ask VAR "Prompt" [default] — reads from the terminal so the script also
# works when stdin is redirected.
ask() {
	local __var=$1 prompt=$2 default=${3-} reply
	if [[ -n "$default" ]]; then
		read -r -p "  $prompt [$default]: " reply </dev/tty
	else
		read -r -p "  $prompt: " reply </dev/tty
	fi
	printf -v "$__var" '%s' "${reply:-$default}"
}

ask_secret() {
	local __var=$1 prompt=$2 reply
	read -r -s -p "  $prompt: " reply </dev/tty
	echo
	printf -v "$__var" '%s' "$reply"
}

confirm() {
	local reply
	read -r -p "  $1 [Y/n]: " reply </dev/tty
	[[ -z "$reply" || "$reply" =~ ^[Yy] ]]
}

# version_ge A B — true when dotted version A >= B.
version_ge() {
	local IFS=.
	local -a a=($1) b=($2)
	local i
	for i in 0 1 2; do
		((${a[i]:-0} > ${b[i]:-0})) && return 0
		((${a[i]:-0} < ${b[i]:-0})) && return 1
	done
	return 0
}

# ---------------------------------------------------------------------------
step "Checking platform"

case "$(uname -s)" in
Darwin) os=macos ;;
Linux) os=linux ;;
*) die "unsupported platform $(uname -s); only macOS and Linux are supported" ;;
esac
ok "$os ($(uname -m))"

hint() {
	case "$1:$os" in
	go:macos) echo "brew install go   (or https://go.dev/dl/)" ;;
	go:linux) echo "download Go $min_go+ from https://go.dev/dl/ (distribution packages are usually too old)" ;;
	node:macos) echo "brew install node@$min_node" ;;
	node:linux) echo "install Node.js $min_node+ via https://github.com/nvm-sh/nvm or https://nodejs.org/" ;;
	nc:linux) echo "sudo apt install netcat-openbsd   (or: sudo dnf install nmap-ncat)" ;;
	git:macos) echo "xcode-select --install" ;;
	*) echo "install '$1' with your package manager" ;;
	esac
}

# ---------------------------------------------------------------------------
step "Checking dependencies"

missing=0
for command in git make go node npm nc; do
	if command -v "$command" >/dev/null 2>&1; then
		continue
	fi
	warn "$command not found — $(hint "$command")"
	missing=1
done
((missing == 0)) || die "install the missing tools above and re-run this script"

go_version=$(go env GOVERSION | sed 's/^go//')
version_ge "$go_version" "$min_go" || die "Go $go_version is too old; need $min_go+ — $(hint go)"
ok "go $go_version"

node_version=$(node --version | sed 's/^v//')
version_ge "$node_version" "$min_node" || die "Node.js $node_version is too old; need $min_node+ — $(hint node)"
ok "node $node_version, npm $(npm --version)"

if ! command -v pm2 >/dev/null 2>&1; then
	warn "pm2 not found"
	if confirm "Install pm2 globally with 'npm install -g pm2'?"; then
		if ! npm install -g pm2; then
			die "npm install -g pm2 failed; if it was a permissions error, configure a user-level npm prefix (https://docs.npmjs.com/resolving-eacces-permissions-errors-when-installing-packages-globally) or use nvm, then re-run"
		fi
		hash -r
		command -v pm2 >/dev/null 2>&1 || die "pm2 installed but is not on PATH; add \"$(npm prefix -g)/bin\" to PATH and re-run"
	else
		die "pm2 is required to run the services"
	fi
fi
ok "pm2 $(pm2 --version 2>/dev/null | tail -n 1)"

# ---------------------------------------------------------------------------
step "Configuring .env"

[[ -f .env.example ]] || die "missing $root/.env.example"

# set_env KEY VALUE — replaces the KEY= line in .env. Values are written in
# single quotes, which godotenv reads literally (no escapes, no ${} expansion).
set_env() {
	local key=$1 value=$2 line tmp
	[[ "$value" != *"'"* && "$value" != *$'\n'* ]] || die "$key must not contain a single quote or newline"
	if [[ -z "$value" ]]; then line="$key="; else line="$key='$value'"; fi
	tmp=$(mktemp "$root/.env.XXXXXX")
	LINE="$line" awk -v key="$key" '
		index($0, key "=") == 1 { print ENVIRON["LINE"]; found = 1; next }
		{ print }
		END { if (!found) print ENVIRON["LINE"] }
	' .env >"$tmp"
	chmod 600 "$tmp"
	mv "$tmp" .env
}

random_secret() {
	if command -v openssl >/dev/null 2>&1; then
		openssl rand -hex 32
	else
		LC_ALL=C tr -dc 'a-f0-9' </dev/urandom | head -c 64
	fi
}

detect_timezone() {
	local tz=''
	if command -v timedatectl >/dev/null 2>&1; then
		tz=$(timedatectl show -p Timezone --value 2>/dev/null || true)
	fi
	if [[ -z "$tz" && -L /etc/localtime ]]; then
		tz=$(readlink /etc/localtime | sed 's#.*zoneinfo/##')
	fi
	if [[ -z "$tz" && -f /etc/timezone ]]; then
		tz=$(cat /etc/timezone)
	fi
	echo "${tz:-UTC}"
}

if [[ -f .env && $reconfigure -eq 0 ]]; then
	ok "keeping existing .env (run with --reconfigure to regenerate it)"
else
	if [[ -f .env ]]; then
		backup=".env.backup.$(date +%Y%m%d%H%M%S)"
		cp -p .env "$backup"
		ok "backed up existing .env to $backup"
	fi
	(umask 077 && cp .env.example .env)
	chmod 600 .env

	echo
	echo "  ${bold}Login${reset} — the single account used to sign in to the web UI."
	ask username "Username" "admin"
	while :; do
		ask_secret password "Password (min. 8 characters)"
		if ((${#password} < 8)); then
			warn "password is too short"
			continue
		fi
		ask_secret password_again "Repeat password"
		[[ "$password" == "$password_again" ]] && break
		warn "passwords do not match"
	done
	set_env HEPHAESTUS_AUTH_USERNAME "$username"
	set_env HEPHAESTUS_AUTH_PASSWORD "$password"
	set_env HEPHAESTUS_JWT_SECRET "$(random_secret)"
	ok "generated a random JWT signing secret"

	echo
	echo "  ${bold}Model provider${reset} — a DeepSeek API key, an OpenAI-compatible endpoint, or both."
	ask deepseek_key "DeepSeek API key (leave empty to skip)"
	ask local_model_url "OpenAI-compatible endpoint URL (leave empty to skip)"
	while [[ -z "$deepseek_key" && -z "$local_model_url" ]]; do
		warn "at least one model provider is required"
		ask deepseek_key "DeepSeek API key"
		[[ -n "$deepseek_key" ]] || ask local_model_url "OpenAI-compatible endpoint URL"
	done
	set_env HEPHAESTUS_DEEPSEEK_API_KEY "$deepseek_key"
	if [[ -n "$local_model_url" ]]; then
		set_env HEPHAESTUS_LOCAL_MODEL_URL "$local_model_url"
		ask local_model_key "API key for that endpoint (leave empty if none)"
		set_env HEPHAESTUS_LOCAL_MODEL_API_KEY "$local_model_key"
	fi

	echo
	echo "  ${bold}Web fetch${reset} — Firecrawl (needs an API key) or a local headless Chrome/Chromium."
	ask firecrawl_key "Firecrawl API key (leave empty to use local Chrome)"
	if [[ -n "$firecrawl_key" ]]; then
		set_env HEPHAESTUS_WEB_FETCH_PROVIDER firecrawl
		set_env HEPHAESTUS_FIRECRAWL_API_KEY "$firecrawl_key"
	else
		set_env HEPHAESTUS_WEB_FETCH_PROVIDER local
		warn "web fetch will use a local Chrome/Chromium; make sure one is installed"
	fi

	echo
	echo "  ${bold}Location${reset} — used for weather and local time context."
	ask location "City or place name" ""
	while [[ -z "$location" ]]; do ask location "City or place name (required)"; done
	coordinate='^-?[0-9]+(\.[0-9]+)?$'
	while :; do
		ask latitude "Latitude (-90 to 90)"
		[[ "$latitude" =~ $coordinate ]] && awk -v v="$latitude" 'BEGIN { exit !(v >= -90 && v <= 90) }' && break
		warn "not a valid latitude"
	done
	while :; do
		ask longitude "Longitude (-180 to 180)"
		[[ "$longitude" =~ $coordinate ]] && awk -v v="$longitude" 'BEGIN { exit !(v >= -180 && v <= 180) }' && break
		warn "not a valid longitude"
	done
	ask timezone "IANA timezone" "$(detect_timezone)"
	set_env HEPHAESTUS_ENV_LOCATION "$location"
	set_env HEPHAESTUS_ENV_LATITUDE "$latitude"
	set_env HEPHAESTUS_ENV_LONGITUDE "$longitude"
	set_env HEPHAESTUS_ENV_TIMEZONE "$timezone"

	echo
	ask proxy_url "Outbound HTTP proxy URL (leave empty for none)"
	set_env HEPHAESTUS_PROXY_URL "$proxy_url"

	ok "wrote $root/.env (mode 600)"
	echo "  Optional features (QQ bot, search API keys, PostgreSQL, shell tool, ...) are"
	echo "  documented in .env — edit it and run 'make deploy' to apply changes."
fi

mkdir -p data

if ((start == 0)); then
	step "Done"
	echo "  Start the services later with: make deploy"
	exit 0
fi

# ---------------------------------------------------------------------------
step "Building and starting services"

# ecosystem.config.cjs reads the proxy settings from the environment, so
# export them from .env for this deployment.
set -a
# shellcheck disable=SC1091
source <(grep -E '^HEPHAESTUS_(PROXY_URL|NO_PROXY)=' .env || true)
set +a

./scripts/deploy.sh

# ---------------------------------------------------------------------------
step "Start on boot"

if confirm "Configure PM2 to start Hephaestus automatically at boot?"; then
	# pm2 startup prints a command (usually needing sudo) rather than running it.
	startup_command=$(pm2 startup 2>/dev/null | grep -E '^\s*sudo ' | tail -n 1 || true)
	if [[ -n "$startup_command" ]]; then
		echo "  Running: $startup_command"
		if eval "$startup_command"; then
			pm2 save
			ok "boot startup configured"
		else
			warn "could not configure boot startup; run 'pm2 startup' manually"
		fi
	else
		pm2 startup || warn "run 'pm2 startup' manually"
		pm2 save
	fi
fi

step "Hephaestus is running"
cat <<EOF
  Web UI:  http://127.0.0.1:5173
  API:     http://127.0.0.1:9016

  Useful commands:
    pm2 status                 show process state
    pm2 logs hephaestus-api    follow API logs
    make deploy                rebuild and restart after pulling or editing .env
EOF
