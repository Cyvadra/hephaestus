const path = require('node:path')

const root = __dirname

// Proxy defaults.
//
// approx-client (mihomo) exposes one listener per exit node on port 17890, each
// bound to its own 127.0.1.x loopback address; `us-direct.approx.internal`
// resolves to 127.0.1.5 via /etc/hosts. That per-node listener is the supported
// default.
//
// The old 127.0.0.1:7890 mixed-port is DEPRECATED and must not be reintroduced
// here or anywhere else. Do not rely on the PM2 daemon's inherited environment:
// the daemon can outlive several config generations, so every app declares its
// proxy explicitly below.
const PROXY_URL = process.env.HEPHAESTUS_PROXY_URL || 'http://us-direct.approx.internal:17890'
const NO_PROXY =
  process.env.HEPHAESTUS_NO_PROXY || '127.0.0.1,localhost,38.59.245.198,47.86.99.175'

const proxyEnv = {
  http_proxy: PROXY_URL,
  https_proxy: PROXY_URL,
  HTTP_PROXY: PROXY_URL,
  HTTPS_PROXY: PROXY_URL,
  all_proxy: PROXY_URL,
  ALL_PROXY: PROXY_URL,
  no_proxy: NO_PROXY,
  NO_PROXY: NO_PROXY,
  GOPROXY: process.env.GOPROXY || 'https://goproxy.cn,direct',
}

module.exports = {
  apps: [
    {
      name: 'hephaestus-api',
      cwd: root,
      script: path.join(root, 'hephaestus'),
      interpreter: 'none',
      env: {
        ...proxyEnv,
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
