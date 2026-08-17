import { type FormEvent, useState } from 'react'
import { LockKeyhole } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { login } from '../api/auth'

export default function LoginView() {
  const { t } = useTranslation()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setSubmitting(true)
    setError(null)
    try {
      await login(username, password)
    } catch {
      setError(t('auth.invalidCredentials'))
    } finally {
      setSubmitting(false)
    }
  }

  return <main className="login-page">
    <form className="login-form" onSubmit={submit}>
      <div className="login-mark"><LockKeyhole size={20} /></div>
      <h1>Hephaestus</h1>
      <p>{t('auth.signIn')}</p>
      <label htmlFor="login-username">{t('auth.username')}</label>
      <input id="login-username" autoComplete="username" value={username} onChange={event => setUsername(event.target.value)} required />
      <label htmlFor="login-password">{t('auth.password')}</label>
      <input id="login-password" type="password" autoComplete="current-password" value={password} onChange={event => setPassword(event.target.value)} required />
      {error && <p className="login-error" role="alert">{error}</p>}
      <button type="submit" disabled={submitting}>{submitting ? t('auth.signingIn') : t('auth.login')}</button>
    </form>
  </main>
}