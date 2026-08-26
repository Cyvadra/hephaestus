#!/usr/bin/env bash

set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

for command in go npm pm2 nc; do
	if ! command -v "$command" >/dev/null 2>&1; then
		echo "deploy: required command not found: $command" >&2
		exit 1
	fi
done

if [[ ! -f .env ]]; then
	echo "deploy: missing $root/.env" >&2
	exit 1
fi

make deploy-build
pm2 startOrRestart ecosystem.config.cjs
pm2 save

pm2 jlist | node -e '
let input = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (chunk) => { input += chunk; });
process.stdin.on("end", () => {
  const applications = JSON.parse(input);
  const required = ["hephaestus-api", "hephaestus-web"];
  const unavailable = required.filter((name) =>
    !applications.some((application) => application.name === name && application.pm2_env.status === "online"),
  );
  if (unavailable.length > 0) {
    console.error(`deploy: PM2 applications not online: ${unavailable.join(", ")}`);
    process.exit(1);
  }
});
'

for port in 9016 5173; do
	if ! nc -z -w 5 127.0.0.1 "$port"; then
		echo "deploy: port 127.0.0.1:$port is not reachable" >&2
		exit 1
	fi
done

pm2 status hephaestus-api hephaestus-web
echo "deploy: complete"
