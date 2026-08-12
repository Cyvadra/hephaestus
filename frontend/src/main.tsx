import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createBrowserRouter, RouterProvider } from 'react-router-dom'
import 'katex/dist/katex.min.css'
import './index.css'
import App from './App.tsx'

const router = createBrowserRouter([
  { path: '*', Component: App },
])

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
)
