import { StrictMode, useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { createBrowserRouter, RouterProvider } from 'react-router-dom'
import 'katex/dist/katex.min.css'
import './index.css'
import './i18n'
import App from './App.tsx'
import LoginView from './components/LoginView.tsx'
import { isAuthenticated, restoreSession, subscribeAuthentication } from './api/auth.ts'

const router = createBrowserRouter([
  { path: '*', Component: App },
])

export function AuthenticationGate() {
  const [ready, setReady] = useState(false)
  const [authenticated, setAuthenticated] = useState(isAuthenticated())

  useEffect(() => {
    const unsubscribe = subscribeAuthentication(() => setAuthenticated(isAuthenticated()))
    void restoreSession().finally(() => setReady(true))
    return unsubscribe
  }, [])

  if (!ready) return <main className="login-page"><p className="login-loading">Loading Hephaestus</p></main>
  return authenticated ? <RouterProvider router={router} /> : <LoginView />
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <AuthenticationGate />
  </StrictMode>,
)
