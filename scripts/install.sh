#!/usr/bin/env bash
#
# Interactive first-time setup for macOS and Linux.
#
#   ./scripts/install.sh               install missing dependencies, write .env, build, start under PM2
#   ./scripts/install.sh --no-start    stop after writing .env
#   ./scripts/install.sh --reconfigure regenerate .env even if one exists (the old one is backed up)
#   ./scripts/install.sh --yes         install missing dependencies without asking
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
assume_yes=0
for arg in "$@"; do
	case "$arg" in
	--no-start) start=0 ;;
	--reconfigure) reconfigure=1 ;;
	-y | --yes) assume_yes=1 ;;
	-h | --help)
		sed -n '3,11p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
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

# Dependencies are installed BT-Panel style: system packages through the
# native package manager (Homebrew on macOS), Go and Node.js from their
# official Linux tarballs into /usr/local, since distribution packages are
# usually too old. Downloads fall back to mainland China mirrors.

if [[ $EUID -eq 0 ]]; then
	sudo=()
else
	sudo=(sudo)
fi

need_sudo() {
	((${#sudo[@]} == 0)) && return 0
	command -v sudo >/dev/null 2>&1 || die "sudo is required to install dependencies; re-run as root or install them yourself"
	sudo -v || die "sudo authentication failed"
}

# fetch URL... — prints the first URL that downloads successfully.
fetch() {
	local url
	for url in "$@"; do
		curl -fsSL --connect-timeout 10 --retry 2 "$url" && return 0
	done
	return 1
}

# download OUT URL... — saves the first URL that downloads successfully.
download() {
	local out=$1 url
	shift
	for url in "$@"; do
		echo "  downloading $url"
		curl -fL --connect-timeout 10 --retry 2 --progress-bar -o "$out" "$url" && return 0
	done
	return 1
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

linux_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) echo "$1" ;;
	aarch64 | arm64) echo "$2" ;;
	*) die "unsupported architecture $(uname -m); install Go and Node.js yourself" ;;
	esac
}

install_system_packages() {
	if [[ $os == macos ]]; then
		command -v brew >/dev/null 2>&1 || die "Homebrew is required on macOS; install it from https://brew.sh and re-run"
		# git and make come with the Xcode Command Line Tools; nc ships with macOS.
		if ! xcode-select -p >/dev/null 2>&1; then
			xcode-select --install || true
			die "finish installing the Xcode Command Line Tools, then re-run this script"
		fi
		return
	fi
	need_sudo
	if command -v apt-get >/dev/null 2>&1; then
		"${sudo[@]}" env DEBIAN_FRONTEND=noninteractive apt-get update -y
		"${sudo[@]}" env DEBIAN_FRONTEND=noninteractive apt-get install -y git make curl ca-certificates tar xz-utils netcat-openbsd
	elif command -v dnf >/dev/null 2>&1; then
		"${sudo[@]}" dnf install -y git make curl ca-certificates tar xz nmap-ncat
	elif command -v yum >/dev/null 2>&1; then
		"${sudo[@]}" yum install -y git make curl ca-certificates tar xz nmap-ncat
	elif command -v pacman >/dev/null 2>&1; then
		"${sudo[@]}" pacman -Sy --needed --noconfirm git make curl ca-certificates tar xz openbsd-netcat
	elif command -v zypper >/dev/null 2>&1; then
		"${sudo[@]}" zypper --non-interactive install git make curl ca-certificates tar xz netcat-openbsd
	elif command -v apk >/dev/null 2>&1; then
		"${sudo[@]}" apk add --no-cache bash git make curl ca-certificates tar xz netcat-openbsd
	else
		die "no supported package manager found; install git, make, curl, tar, xz and nc yourself"
	fi
}

# Puts DIR on PATH for this run and for future login shells.
add_to_path() {
	local dir=$1 name=$2
	case ":$PATH:" in *":$dir:"*) ;; *) export PATH="$dir:$PATH" ;; esac
	if [[ $os == linux && -d /etc/profile.d ]]; then
		printf 'case ":$PATH:" in *":%s:"*) ;; *) export PATH="%s:$PATH" ;; esac\n' "$dir" "$dir" |
			"${sudo[@]}" tee "/etc/profile.d/$name.sh" >/dev/null
	fi
}

install_go() {
	if [[ $os == macos ]]; then
		if brew list go >/dev/null 2>&1; then brew upgrade go || true; else brew install go; fi
		return
	fi
	need_sudo
	local arch version file tmp
	arch=$(linux_arch amd64 arm64)
	version=$(fetch https://go.dev/VERSION?m=text https://golang.google.cn/VERSION?m=text | head -n 1) ||
		die "could not determine the latest Go version"
	file="$version.linux-$arch.tar.gz"
	tmp=$(mktemp -d)
	download "$tmp/$file" "https://dl.google.com/go/$file" "https://golang.google.cn/dl/$file" ||
		die "could not download $file"
	local expected
	expected=$(fetch "https://dl.google.com/go/$file.sha256" "https://golang.google.cn/dl/$file.sha256") ||
		die "could not download the checksum for $file"
	[[ "$(sha256_of "$tmp/$file")" == "${expected%% *}" ]] || die "checksum mismatch for $file"
	"${sudo[@]}" rm -rf /usr/local/go
	"${sudo[@]}" tar -C /usr/local -xzf "$tmp/$file"
	rm -rf "$tmp"
	add_to_path /usr/local/go/bin go
	# Module downloads from proxy.golang.org are often unreachable from
	# mainland China; fall back to goproxy.cn when it is.
	if ! curl -fsS --connect-timeout 5 -o /dev/null https://proxy.golang.org/; then
		go env -w GOPROXY=https://goproxy.cn,direct
		ok "set GOPROXY=https://goproxy.cn,direct"
	fi
}

install_node() {
	if [[ $os == macos ]]; then
		if brew list node >/dev/null 2>&1; then brew upgrade node || true; else brew install node; fi
		return
	fi
	need_sudo
	local arch mirror sums file tmp
	arch=$(linux_arch x64 arm64)
	for mirror in https://nodejs.org/dist https://npmmirror.com/mirrors/node; do
		sums=$(fetch "$mirror/latest-v$min_node.x/SHASUMS256.txt") && break
	done
	[[ -n "${sums:-}" ]] || die "could not reach nodejs.org or its mirror"
	file=$(awk -v suffix="-linux-$arch.tar.xz" 'substr($2, length($2) - length(suffix) + 1) == suffix { print $2; exit }' <<<"$sums")
	[[ -n "$file" ]] || die "no Node.js $min_node build for linux-$arch"
	tmp=$(mktemp -d)
	download "$tmp/$file" "$mirror/latest-v$min_node.x/$file" || die "could not download $file"
	[[ "$(sha256_of "$tmp/$file")" == "$(awk -v f="$file" '$2 == f { print $1 }' <<<"$sums")" ]] ||
		die "checksum mismatch for $file"
	# The tarball's bin/, lib/, include/ and share/ merge into /usr/local.
	"${sudo[@]}" tar -C /usr/local --strip-components=1 --no-same-owner \
		--exclude="*/CHANGELOG.md" --exclude="*/README.md" --exclude="*/LICENSE" -xJf "$tmp/$file"
	rm -rf "$tmp"
	add_to_path /usr/local/bin node
	if [[ $mirror == *npmmirror* ]]; then
		npm config set registry https://registry.npmmirror.com
		ok "set the npm registry to https://registry.npmmirror.com"
	fi
}

install_pm2() {
	if [[ -w "$(npm prefix -g)/lib" || -w "$(npm prefix -g)" ]]; then
		npm install -g pm2
	else
		need_sudo
		"${sudo[@]}" env "PATH=$PATH" npm install -g pm2
	fi
}

# ---------------------------------------------------------------------------
step "Checking dependencies"

missing_system=()
for command in git make curl tar nc; do
	command -v "$command" >/dev/null 2>&1 || missing_system+=("$command")
done
[[ $os == linux ]] && ! command -v xz >/dev/null 2>&1 && missing_system+=(xz)

go_ok=0
if command -v go >/dev/null 2>&1 && version_ge "$(go env GOVERSION | sed 's/^go//')" "$min_go"; then go_ok=1; fi
node_ok=0
if command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1 &&
	version_ge "$(node --version | sed 's/^v//')" "$min_node"; then node_ok=1; fi
pm2_ok=0
command -v pm2 >/dev/null 2>&1 && pm2_ok=1

todo=()
((${#missing_system[@]} == 0)) || todo+=("system packages (${missing_system[*]})")
((go_ok)) || todo+=("Go $min_go+")
((node_ok)) || todo+=("Node.js $min_node+")
((pm2_ok)) || todo+=("pm2")

if ((${#todo[@]} > 0)); then
	warn "missing: $(IFS=,; echo "${todo[*]}" | sed 's/,/, /g')"
	if ((assume_yes == 0)) && ! confirm "Install them automatically?"; then
		die "install the missing dependencies and re-run this script"
	fi
	((${#missing_system[@]} == 0)) || install_system_packages
	((go_ok)) || install_go
	((node_ok)) || install_node
	hash -r
	((pm2_ok)) || install_pm2
	hash -r
fi

for command in git make curl nc go node npm; do
	command -v "$command" >/dev/null 2>&1 || die "$command is still not available after installation"
done
command -v pm2 >/dev/null 2>&1 || die "pm2 installed but is not on PATH; add \"$(npm prefix -g)/bin\" to PATH and re-run"

go_version=$(go env GOVERSION | sed 's/^go//')
version_ge "$go_version" "$min_go" || die "Go $go_version at $(command -v go) is too old; need $min_go+ (it shadows any newer install on PATH)"
ok "go $go_version"

node_version=$(node --version | sed 's/^v//')
version_ge "$node_version" "$min_node" || die "Node.js $node_version at $(command -v node) is too old; need $min_node+ (it shadows any newer install on PATH)"
ok "node $node_version, npm $(npm --version)"
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
