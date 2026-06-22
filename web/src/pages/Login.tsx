import { useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { Loader2 } from 'lucide-react';
import { login } from '@/api/auth';
import { useAuth } from '@/store/auth';
import { ApiError } from '@/api/client';
import { OngridLogo } from '@/components/OngridLogo';
import { useI18n } from '@/i18n/locale';

export default function LoginPage() {
  const navigate = useNavigate();
  const setSession = useAuth((s) => s.setSession);
  const { tr } = useI18n();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setPending(true);
    try {
      const res = await login(email.trim(), password);
      setSession({
        access_token: res.access_token,
        refresh_token: res.refresh_token,
        role: res.user?.role ?? res.role ?? 'user',
        email: res.user?.email ?? res.email ?? email.trim(),
      });
      navigate('/', { replace: true });
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setError(tr('邮箱或密码错误', 'Invalid email or password'));
      } else if (err instanceof ApiError) {
        setError(err.message || tr('登录失败，请稍后重试', 'Sign-in failed; please try again'));
      } else {
        setError(tr('登录失败，请稍后重试', 'Sign-in failed; please try again'));
      }
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="relative flex min-h-screen items-start justify-center bg-zinc-950 px-4 pt-[16vh]">
      {/* Brand backdrop — soft radial gradient only. The blown-up logo
          watermark in the corner was reported as visually noisy; the
          small logo inside the card is enough brand presence. */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 overflow-hidden"
        style={{
          background:
            'radial-gradient(circle at 30% 20%, rgba(140,109,240,0.12), transparent 55%), radial-gradient(circle at 70% 80%, rgba(48,166,208,0.10), transparent 55%)',
        }}
      />


      <div className="relative w-full max-w-sm rounded-2xl border border-zinc-800 bg-zinc-900/80 p-7 shadow-xl backdrop-blur">
        <div className="mb-6 text-center">
          <div className="mx-auto mb-4 flex h-20 w-20 items-center justify-center">
            <OngridLogo size={72} />
          </div>
          <h1 className="text-xl font-semibold text-zinc-100">{tr('登录到 Ongrid', 'Sign in to Ongrid')}</h1>
          <p className="mt-1 text-xs text-zinc-500">{tr('AIOps 工作台', 'AIOps Workbench')}</p>
        </div>

        <form onSubmit={onSubmit} className="space-y-3" noValidate>
          <div>
            <label htmlFor="email" className="mb-1 block text-xs text-zinc-400">
              {tr('邮箱', 'Email')}
            </label>
            <input
              id="email"
              type="email"
              required
              autoComplete="email"
              autoFocus
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              className="w-full rounded-lg border border-zinc-800 bg-zinc-950/40 px-3 py-2 text-sm text-zinc-100 placeholder:text-zinc-500 focus:border-zinc-600 focus:outline-none"
              placeholder="you@example.com"
            />
          </div>

          <div>
            <label htmlFor="password" className="mb-1 block text-xs text-zinc-400">
              {tr('密码', 'Password')}
            </label>
            <input
              id="password"
              type="password"
              required
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className="w-full rounded-lg border border-zinc-800 bg-zinc-950/40 px-3 py-2 text-sm text-zinc-100 placeholder:text-zinc-500 focus:border-zinc-600 focus:outline-none"
              placeholder="••••••••"
            />
          </div>

          {error && (
            <div
              role="alert"
              className="rounded-lg border border-red-500/20 bg-red-500/10 px-3 py-2 text-xs text-red-300"
            >
              {error}
            </div>
          )}

          <button
            type="submit"
            disabled={pending || !email || !password}
            aria-label={tr('登录', 'Sign in')}
            style={{
              // Brand gradient lifted from the logo's left pillar — keeps
              // the primary CTA visually anchored to the brand. Hover
              // ramps brightness; disabled uses inherited opacity.
              backgroundImage:
                'linear-gradient(135deg, #8C6DF0 0%, #5269F4 100%)',
            }}
            className="inline-flex w-full items-center justify-center gap-2 rounded-lg px-3 py-2 text-sm font-medium text-white shadow-md transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {pending && <Loader2 size={14} className="animate-spin" />}
            <span>{tr('登录', 'Sign in')}</span>
          </button>
        </form>

      </div>
    </div>
  );
}
