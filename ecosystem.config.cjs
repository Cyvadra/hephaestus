const path = require('node:path')

const root = __dirname

module.exports = {
  apps: [
    {
      name: 'hephaestus-api',
      cwd: root,
      script: path.join(root, 'hephaestus'),
      interpreter: 'none',
      env: {
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