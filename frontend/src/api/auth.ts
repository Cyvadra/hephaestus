const TOKEN_HEADER = 'X-Hephaestus-Token'
const SHA256_ROUND_CONSTANTS = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
])

type AuthListener = () => void

let token: string | null = null
const listeners = new Set<AuthListener>()

function notify() {
  listeners.forEach(listener => listener())
}

export function isAuthenticated() {
  return token !== null
}

export function subscribeAuthentication(listener: AuthListener) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

function setToken(nextToken: string | null) {
  token = nextToken
  notify()
}

export async function authFetch(input: RequestInfo | URL, init: RequestInit = {}) {
  const headers = new Headers(init.headers)
  if (token) headers.set('Authorization', `Bearer ${token}`)
  const response = await fetch(input, { ...init, headers, credentials: 'same-origin' })
  const refreshedToken = response.headers.get(TOKEN_HEADER)
  if (refreshedToken) setToken(refreshedToken)
  if (response.status === 401 && token) setToken(null)
  return response
}

function randomSalt() {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  return Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('')
}

function rotateRight(value: number, amount: number) {
  return (value >>> amount) | (value << (32 - amount))
}

function sha256(value: string) {
  const input = new TextEncoder().encode(value)
  const paddedLength = Math.ceil((input.length + 9) / 64) * 64
  const padded = new Uint8Array(paddedLength)
  padded.set(input)
  padded[input.length] = 0x80
  const view = new DataView(padded.buffer)
  view.setUint32(paddedLength - 4, input.length * 8)

  const hash = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19])
  const words = new Uint32Array(64)
  for (let offset = 0; offset < paddedLength; offset += 64) {
    for (let index = 0; index < 16; index++) words[index] = view.getUint32(offset + index * 4)
    for (let index = 16; index < 64; index++) {
      const before = words[index - 15]
      const after = words[index - 2]
      const sigma0 = rotateRight(before, 7) ^ rotateRight(before, 18) ^ (before >>> 3)
      const sigma1 = rotateRight(after, 17) ^ rotateRight(after, 19) ^ (after >>> 10)
      words[index] = words[index - 16] + sigma0 + words[index - 7] + sigma1
    }

    let [a, b, c, d, e, f, g, h] = hash
    for (let index = 0; index < 64; index++) {
      const sum1 = rotateRight(e, 6) ^ rotateRight(e, 11) ^ rotateRight(e, 25)
      const choice = (e & f) ^ (~e & g)
      const temporary1 = h + sum1 + choice + SHA256_ROUND_CONSTANTS[index] + words[index]
      const sum0 = rotateRight(a, 2) ^ rotateRight(a, 13) ^ rotateRight(a, 22)
      const majority = (a & b) ^ (a & c) ^ (b & c)
      const temporary2 = sum0 + majority
      ;[a, b, c, d, e, f, g, h] = [temporary1 + temporary2, a, b, c, d + temporary1, e, f, g]
    }
    ;[hash[0], hash[1], hash[2], hash[3], hash[4], hash[5], hash[6], hash[7]] = [
      hash[0] + a, hash[1] + b, hash[2] + c, hash[3] + d, hash[4] + e, hash[5] + f, hash[6] + g, hash[7] + h,
    ]
  }
  return Array.from(hash, word => word.toString(16).padStart(8, '0')).join('')
}

function digest(password: string, timestamp: number, salt: string) {
  return sha256(`${password}${timestamp}${salt}`)
}

export async function login(username: string, password: string) {
  const timestamp = Date.now()
  const salt = randomSalt()
  const response = await fetch('/api/v1/auth/login', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, timestamp, salt, digest: digest(password, timestamp, salt) }),
  })
  if (!response.ok) throw new Error('Invalid username or password')
  const issuedToken = response.headers.get(TOKEN_HEADER)
  if (!issuedToken) throw new Error('Login response did not include a session token')
  setToken(issuedToken)
}

export async function restoreSession() {
  const response = await authFetch('/api/v1/auth/session')
  return response.ok
}

export async function logout() {
  await authFetch('/api/v1/auth/logout', { method: 'POST' }).catch(() => undefined)
  setToken(null)
}