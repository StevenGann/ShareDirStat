import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import { applyThemePref, getThemePref } from './theme';
import './styles.css';

// The CSP forbids an inline head script, so the stored theme override is
// applied at module load — before first paint in practice.
applyThemePref(getThemePref());

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
