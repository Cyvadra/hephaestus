const path = require('node:path')

const root = __dirname

// Optional outbound proxy for the API process. Model providers and web tools
// are the only components that make outbound requests; set HEPHAESTUS_PROXY_URL
// before running `make deploy` when this host needs one. Nothing is injected
// when it is unset, so the processes inherit the environment as-is.
//
// Do not rely on the PM2 daemon's inherited environment: the daemon can outlive
// several config generations, so each app declares its own environment below.
const PROXY_URL = process.env.HEPHAESTUS_PROXY_URL || ''
const NO_PROXY = process.env.HEPHAESTUS_NO_PROXY || '127.0.0.1,localhost'

const proxyEnv = PROXY_URL
  ? {
      http_proxy: PROXY_URL,
      https_proxy: PROXY_URL,
      HTTP_PROXY: PROXY_URL,
      HTTPS_PROXY: PROXY_URL,
      all_proxy: PROXY_URL,
      ALL_PROXY: PROXY_URL,
      no_proxy: NO_PROXY,
      NO_PROXY: NO_PROXY,
    }
  : {}

module.exports = {
  apps: [
    {
      name: 'hephaestus-api',
      cwd: root,
      script: path.join(root, 'hephaestus'),
      interpreter: 'none',
      env: {
        ...proxyEnv,
        GIN_MODE: 'release',
        HEPHAESTUS_LISTEN_ADDR: '127.0.0.1:9016',
      },
      autorestart: true,
      restart_delay: 3000,
      max_restarts: 10,
      kill_timeout: 10000,
      time: true,
    },
    {
      name: 'hephaestus-web',
      cwd: path.join(root, 'frontend'),
      script: 'npm',
      args: 'run preview',
      interpreter: 'none',
      env: {
        ...proxyEnv,
        NODE_ENV: 'production',
      },
      autorestart: true,
      restart_delay: 3000,
      max_restarts: 10,
      kill_timeout: 5000,
      time: true,
    },
  ],
}
