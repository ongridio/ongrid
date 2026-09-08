import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import App from './App';
import { TooltipProvider } from './components/ui/Tooltip';
import './styles/index.css';
import { applyAccentOnBoot } from './store/theme';
import { applyThemeOnBoot } from './store/mode';
import { getLocale } from './i18n/locale';

// Apply persisted theme + accent + lang before first paint. Each is
// one DOM write; cheap to run unconditionally.
applyThemeOnBoot();
applyAccentOnBoot();
document.documentElement.lang = getLocale();

const rootEl = document.getElementById('root');
if (!rootEl) throw new Error('Missing #root element');

createRoot(rootEl).render(
  <StrictMode>
    <BrowserRouter>
      <TooltipProvider delay={300}><App /></TooltipProvider>
    </BrowserRouter>
  </StrictMode>
);
