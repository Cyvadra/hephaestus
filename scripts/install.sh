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
	# Check passwordless (NOPASSWD) sudo first: `sudo -v` may still prompt for
	# a password there, notably with sudo-rs on recent Ubuntu.
	sudo -n true 2>/dev/null && return 0
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
	if [[ -z "${GOPROXY:-}" ]] && ! curl -fsS --connect-timeout 5 -o /dev/null https://proxy.golang.org/ 2>/dev/null; then
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

have_chrome() {
	local name
	for name in google-chrome google-chrome-stable chromium chromium-browser; do
		command -v "$name" >/dev/null 2>&1 && return 0
	done
	[[ $os == macos && -d "/Applications/Google Chrome.app" ]]
}

# Best effort: Google Chrome on x86_64 Linux, the distribution's Chromium
# elsewhere. Failure is not fatal since web fetch can fall back to DeepSeek.
install_chrome() {
	if [[ $os == macos ]]; then
		brew install --cask google-chrome
		return
	fi
	need_sudo
	local tmp
	tmp=$(mktemp -d)
	if [[ $(uname -m) == x86_64 ]] && command -v apt-get >/dev/null 2>&1; then
		download "$tmp/chrome.deb" https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb &&
			"${sudo[@]}" env DEBIAN_FRONTEND=noninteractive apt-get install -y "$tmp/chrome.deb"
	elif [[ $(uname -m) == x86_64 ]] && command -v dnf >/dev/null 2>&1; then
		download "$tmp/chrome.rpm" https://dl.google.com/linux/direct/google-chrome-stable_current_x86_64.rpm &&
			"${sudo[@]}" dnf install -y "$tmp/chrome.rpm"
	elif [[ $(uname -m) == x86_64 ]] && command -v yum >/dev/null 2>&1; then
		download "$tmp/chrome.rpm" https://dl.google.com/linux/direct/google-chrome-stable_current_x86_64.rpm &&
			"${sudo[@]}" yum install -y "$tmp/chrome.rpm"
	elif command -v apt-get >/dev/null 2>&1; then
		"${sudo[@]}" env DEBIAN_FRONTEND=noninteractive apt-get install -y chromium
	elif command -v dnf >/dev/null 2>&1; then
		"${sudo[@]}" dnf install -y chromium
	elif command -v pacman >/dev/null 2>&1; then
		"${sudo[@]}" pacman -S --needed --noconfirm chromium
	elif command -v zypper >/dev/null 2>&1; then
		"${sudo[@]}" zypper --non-interactive install chromium
	elif command -v apk >/dev/null 2>&1; then
		"${sudo[@]}" apk add --no-cache chromium
	fi
	local status=$?
	rm -rf "$tmp"
	return $status
}

install_pm2() {
	if [[ -w "$(npm prefix -g)/lib" || -w "$(npm prefix -g)" ]]; then
		npm install -g pm2
	else
		need_sudo
		"${sudo[@]}" env "PATH=$PATH" npm install -g pm2
	fi
	# A Node.js installed outside /usr/local (e.g. /usr/local/lib/nodejs/...)
	# puts global binaries in its own bin, which is often not on PATH.
	command -v pm2 >/dev/null 2>&1 || add_to_path "$(npm prefix -g)/bin" npm-global
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

# Chrome/Chromium backs the local web fetch provider; it is optional.
if ! have_chrome; then
	warn "missing: Chrome/Chromium (used by web fetch; DeepSeek is the fallback)"
	if ((assume_yes)) || confirm "Install it automatically?"; then
		install_chrome && hash -r
	fi
fi
if have_chrome; then
	ok "Chrome/Chromium found"
else
	warn "no Chrome/Chromium; web fetch will rely on Firecrawl or DeepSeek"
fi

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

# zone_coordinates TZ — prints "latitude longitude" in decimal degrees for an
# IANA timezone, from the tzdata zone table; "0 0" when it is not listed.
zone_coordinates() {
	local table
	for table in /usr/share/zoneinfo/zone1970.tab /usr/share/zoneinfo/zone.tab /var/db/timezone/zoneinfo/zone.tab; do
		[[ -r "$table" ]] || continue
		awk -v tz="$1" '
			function degrees(s,   sign, d, m, sec, n) {
				sign = substr(s, 1, 1) == "-" ? -1 : 1
				s = substr(s, 2); n = length(s)
				# ISO 6709: DDMM or DDMMSS for latitude, DDDMM or DDDMMSS for longitude.
				if (n == 4 || n == 6) { d = substr(s, 1, 2); m = substr(s, 3, 2); sec = substr(s, 5, 2) }
				else { d = substr(s, 1, 3); m = substr(s, 4, 2); sec = substr(s, 6, 2) }
				return sign * (d + m / 60 + sec / 3600)
			}
			!/^#/ && $3 == tz {
				match($2, /^[+-][0-9]+/)
				printf "%.4f %.4f\n", degrees(substr($2, 1, RLENGTH)), degrees(substr($2, RLENGTH + 1))
				found = 1; exit
			}
			END { exit !found }
		' "$table" && return 0
	done
	echo "0 0"
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
	echo "  ${bold}Model provider${reset} — a DeepSeek API key, or an OpenAI-compatible endpoint."
	echo "  Get a DeepSeek key at https://platform.deepseek.com/api_keys"
	while :; do
		ask deepseek_key "DeepSeek API key (leave empty to use another endpoint)"
		[[ -n "$deepseek_key" ]] && break
		ask local_model_url "OpenAI-compatible endpoint URL"
		[[ -n "$local_model_url" ]] && break
		warn "at least one model provider is required"
	done
	set_env HEPHAESTUS_DEEPSEEK_API_KEY "$deepseek_key"
	if [[ -n "${local_model_url:-}" ]]; then
		set_env HEPHAESTUS_LOCAL_MODEL_URL "$local_model_url"
		ask local_model_key "API key for that endpoint (leave empty if none)"
		set_env HEPHAESTUS_LOCAL_MODEL_API_KEY "$local_model_key"
	fi

	echo
	echo "  ${bold}Web fetch${reset} — Firecrawl (https://www.firecrawl.dev/app/api-keys) or a local headless Chrome/Chromium."
	ask firecrawl_key "Firecrawl API key (leave empty to use local Chrome)"
	if [[ -n "$firecrawl_key" ]]; then
		set_env HEPHAESTUS_WEB_FETCH_PROVIDER firecrawl
		set_env HEPHAESTUS_FIRECRAWL_API_KEY "$firecrawl_key"
	else
		set_env HEPHAESTUS_WEB_FETCH_PROVIDER local
		have_chrome || warn "no Chrome/Chromium installed; web fetch will fall back to DeepSeek"
	fi

	echo
	echo "  ${bold}Web search${reset} — optional extra providers; leave any empty to skip it."
	echo "  Several keys for one provider may be given comma-separated."
	echo "    Brave:   https://api-dashboard.search.brave.com/app/keys"
	echo "    Tavily:  https://app.tavily.com/home"
	echo "    SerpAPI: https://serpapi.com/manage-api-key"
	echo "    SearXNG: the base URL of a self-hosted instance, see https://docs.searxng.org/"
	ask brave_keys "Brave Search API key(s)"
	ask tavily_keys "Tavily API key(s)"
	ask serpapi_keys "SerpAPI API key(s)"
	ask searxng_url "SearXNG base URL"
	set_env HEPHAESTUS_WEB_SEARCH_BRAVE_API_KEYS "$brave_keys"
	set_env HEPHAESTUS_WEB_SEARCH_TAVILY_API_KEYS "$tavily_keys"
	set_env HEPHAESTUS_WEB_SEARCH_SERPAPI_API_KEYS "$serpapi_keys"
	set_env HEPHAESTUS_WEB_SEARCH_SEARXNG_BASE_URL "$searxng_url"

	echo
	echo "  ${bold}QQ bot${reset} — optional; chat with Hephaestus from QQ."
	echo "  Create a bot at https://q.qq.com/qqbot/dashboard and copy its AppID and AppSecret."
	ask qq_app_id "QQ bot AppID (leave empty to skip)"
	if [[ -n "$qq_app_id" ]]; then
		while [[ -z "${qq_app_secret:-}" ]]; do ask qq_app_secret "QQ bot AppSecret (required with an AppID)"; done
		set_env HEPHAESTUS_QQ_APP_ID "$qq_app_id"
		set_env HEPHAESTUS_QQ_APP_SECRET "$qq_app_secret"
		echo "  Leave the OpenID empty if you don't know it: the bot then replies to any"
		echo "  sender with their own OpenID, which you copy into HEPHAESTUS_QQ_USER_OPENID"
		echo "  in .env before running 'make deploy' again."
		ask qq_openid "Your QQ user OpenID"
		set_env HEPHAESTUS_QQ_USER_OPENID "$qq_openid"
	fi

	# Location and timezone come from the system timezone; edit .env to refine them.
	timezone=$(detect_timezone)
	read -r latitude longitude < <(zone_coordinates "$timezone")
	location=${timezone##*/}
	location=${location//_/ }
	set_env HEPHAESTUS_ENV_LOCATION "$location"
	set_env HEPHAESTUS_ENV_LATITUDE "$latitude"
	set_env HEPHAESTUS_ENV_LONGITUDE "$longitude"
	set_env HEPHAESTUS_ENV_TIMEZONE "$timezone"
	echo
	ok "location: $location ($latitude, $longitude), timezone $timezone — from the system timezone"

	echo
	ask proxy_url "Outbound HTTP proxy URL (leave empty for none)"
	set_env HEPHAESTUS_PROXY_URL "$proxy_url"

	ok "wrote $root/.env (mode 600)"
	echo "  Other options (PostgreSQL, shell tool, ...) are documented in .env —"
	echo "  edit it and run 'make deploy' to apply changes."
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

if [[ $os == linux ]]; then
	# Prefer the public IP as seen from outside; fall back to the first local address.
	public_ip=$(fetch https://api.ipify.org https://4.ipw.cn https://ifconfig.me 2>/dev/null | head -n 1 | tr -d '[:space:]' || true)
	[[ "$public_ip" =~ ^[0-9a-fA-F:.]+$ ]] || public_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
	# SSH_CONNECTION is "client_ip client_port server_ip server_port".
	ssh_port=$(awk '{print $4}' <<<"${SSH_CONNECTION:-}")
	ssh_port_flag=''
	[[ -n "$ssh_port" && "$ssh_port" != 22 ]] && ssh_port_flag="-p $ssh_port "
	cat <<EOF

  To open the Web UI from your own computer, forward the port over SSH there:
    ssh -N ${ssh_port_flag}-L 8082:127.0.0.1:5173 $(id -un)@${public_ip:-<server-ip>}
  then browse to http://127.0.0.1:8082
EOF
fi
