#!/usr/bin/env sh

set -eu

cd "$(dirname "$0")/.."

printf '%s\n' 'Chinese-text candidates outside locale resources:'
rg -n --glob '*.{ts,tsx}' --glob '!src/i18n/**' '[\p{Han}]' src || true

printf '%s\n' ''
printf '%s\n' 'User-facing attribute candidates:'
rg -n --glob '*.{tsx}' --glob '!src/i18n/**' "(aria-label|aria-description|title|placeholder|alt)=[\"'][^\"']+" src || true

printf '%s\n' ''
printf '%s\n' 'Browser dialog and validation candidates:'
rg -n --glob '*.{ts,tsx}' --glob '!src/i18n/**' '(window\.(alert|confirm|prompt)|new Notification|setCustomValidity)' src || true