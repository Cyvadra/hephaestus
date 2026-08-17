const TOKEN_HEADER = 'X-Hephaestus-Token'

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

async function digest(password: string, timestamp: number, salt: string) {
  const bytes = new TextEncoder().encode(`${password}${timestamp}${salt}`)
  const hash = await crypto.subtle.digest('SHA-256', bytes)
  return Array.from(new Uint8Array(hash), byte => byte.toString(16).padStart(2, '0')).join('')
}

export async function login(username: string, password: string) {
  const timestamp = Date.now()
  const salt = randomSalt()
  const response = await fetch('/api/v1/auth/login', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, timestamp, salt, digest: await digest(password, timestamp, salt) }),
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