import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import {
  Link,
  Navigate,
  NavLink,
  Route,
  Routes,
  useLocation,
  useNavigate,
  useParams,
} from 'react-router-dom';
import {
  Activity,
  ArrowDownToLine,
  ArrowRight,
  BookOpen,
  Bot,
  ChevronRight,
  Gauge,
  Layers3,
  Menu,
  Play,
  Plus,
  Server,
  ShieldCheck,
  Sparkles,
  Workflow,
} from 'lucide-react';
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip as ChartTooltip,
  XAxis,
  YAxis,
} from 'recharts';
import {
  Button,
  Card,
  Chip,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  EmptyState,
  EmptyStateCard,
  Input,
  Label,
  Select,
  Textarea,
} from '@/components/ui';
import { useDialogs } from '@/components/ui/useDialogs';
import { useI18n } from '@/i18n/locale';
import {
  executeFlow,
  flows,
  load,
  metricPoints,
  save,
  seed,
  updateIncident,
  uniqueId,
  type Article,
  type Bilingual,
  type Incident,
  type Sandbox,
} from './data';
import { toggleEnterpriseTheme } from './theme';
import Monitoring, { monitorPages } from './Monitoring';

const Context = createContext<{
  data: Sandbox;
  commit: (data: Sandbox) => void;
}>({ data: seed(), commit: () => {} });
const useSandbox = () => useContext(Context);
const useText = () => {
  const { tr } = useI18n();
  return (v: Bilingual) => tr(v.zh, v.en);
};
function Logo({ size = 38 }: { size?: number }) {
  return (
    <img
      src="/favicon.svg"
      width={size}
      height={size}
      alt=""
      aria-hidden="true"
    />
  );
}
function Stamp({
  value,
}: {
  value: Incident['status'] | 'healthy' | 'warning' | 'success';
}) {
  const { tr } = useI18n();
  const words = {
    open: tr('待处置', 'Open'),
    acknowledged: tr('处理中', 'In progress'),
    closed: tr('已关闭', 'Closed'),
    healthy: tr('运行正常', 'Healthy'),
    warning: tr('需要关注', 'Attention'),
    success: tr('模拟完成', 'Simulated'),
  };
  return (
    <Chip
      tone={
        value === 'open'
          ? 'danger'
          : value === 'warning' || value === 'acknowledged'
            ? 'warning'
            : 'success'
      }
    >
      {words[value]}
    </Chip>
  );
}
function Heading({
  title,
  subtitle,
  action,
}: {
  title: string;
  subtitle: string;
  action?: ReactNode;
}) {
  return (
    <div className="ex-heading">
      <div>
        <p className="ex-eyebrow">ENTERPRISE OPERATIONS</p>
        <h1>{title}</h1>
        <p className="ex-muted">{subtitle}</p>
      </div>
      {action}
    </div>
  );
}
function date(value: string) {
  return new Date(value).toLocaleString(document.documentElement.lang, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
}

export default function EnterpriseApp() {
  const { tr, locale, toggleLocale } = useI18n();
  const initial = useRef<ReturnType<typeof load>>();
  if (!initial.current) {
    try {
      initial.current = load(localStorage);
    } catch {
      initial.current = { data: seed(), error: true };
    }
  }
  const [data, setData] = useState(initial.current.data);
  const [warning, setWarning] = useState(initial.current.error);
  useEffect(() => {
    const snapshot = initial.current;
    if (snapshot && !snapshot.error) {
      try {
        if (!save(localStorage, snapshot.data)) setWarning(true);
      } catch {
        setWarning(true);
      }
    }
  }, []);
  const [entered, setEntered] = useState(() => {
    try {
      return sessionStorage.getItem('enterprise-ai.entered') === '1';
    } catch {
      return false;
    }
  });
  const [drawer, setDrawer] = useState(false);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [loginError, setLoginError] = useState(false);
  const { dialog, confirmAction } = useDialogs();
  const navigate = useNavigate();
  const location = useLocation();
  const viewRef = useRef<HTMLElement>(null);
  const commit = (next: Sandbox) => {
    setData(next);
    try {
      setWarning(!save(localStorage, next));
    } catch {
      setWarning(true);
    }
  };
  useEffect(() => {
    document.title = tr(
      '企业AI 智能运维平台',
      'Enterprise AI Operations Platform',
    );
  }, [locale, tr]);
  useEffect(() => {
    setDrawer(false);
    viewRef.current?.focus({ preventScroll: true });
    window.scrollTo(0, 0);
  }, [location.pathname]);
  const reset = async () => {
    if (
      await confirmAction(
        tr(
          '恢复初始示例数据？本浏览器的告警处理、知识编辑和执行记录将被替换。',
          'Restore sample data? Incident changes, edited knowledge and execution records in this browser will be replaced.',
        ),
      )
    )
      commit(seed());
  };
  const sections = [
    {
      path: '/overview',
      name: tr('运营驾驶舱', 'Command center'),
      icon: Gauge,
      items: [['/overview', tr('运维总览', 'Overview')]],
    },
    {
      path: '/risk',
      name: tr('风险处置', 'Risk response'),
      icon: ShieldCheck,
      items: [['/risk', tr('告警中心', 'Incidents')]],
    },
    {
      path: '/monitoring/overview',
      name: tr('监控中心', 'Monitoring'),
      icon: Activity,
      items: monitorPages.map(([key, zh, en]) => [
        `/monitoring/${key}`,
        tr(zh, en),
      ]),
    },
    {
      path: '/inventory',
      name: tr('资产与观测', 'Assets & metrics'),
      icon: Layers3,
      items: [
        ['/inventory', tr('资源台账', 'Inventory')],
        ['/observations', tr('指标监测', 'Metrics')],
      ],
    },
    {
      path: '/intelligence',
      name: tr('智能作业', 'Intelligent operations'),
      icon: Sparkles,
      items: [
        ['/intelligence', tr('AI 运维助手', 'AI assistant')],
        ['/automation', tr('自动化流程', 'Workflows')],
        ['/executions', tr('执行记录', 'Executions')],
      ],
    },
    {
      path: '/library',
      name: tr('运维知识', 'Knowledge'),
      icon: BookOpen,
      items: [['/library', tr('知识文档', 'Documents')]],
    },
  ];
  const current =
    sections.find((s) =>
      s.items.some(
        ([path]) =>
          location.pathname === path ||
          location.pathname.startsWith(`${path}/`),
      ),
    ) ?? sections[0];
  const enter = () => {
    commit(data);
    setEntered(true);
    try {
      sessionStorage.setItem('enterprise-ai.entered', '1');
    } catch {
      /* Access remains available without session persistence. */
    }
    navigate('/overview');
  };
  const exit = () => {
    setEntered(false);
    try {
      sessionStorage.removeItem('enterprise-ai.entered');
    } catch {
      /* In-memory session has already ended. */
    }
    navigate('/login');
  };
  if (!entered || location.pathname === '/login')
    return (
      <div className="ex-app ex-login">
        <div className="ex-login-art">
          <Logo size={60} />
          <p className="ex-eyebrow">INTELLIGENCE IN EVERY OPERATION</p>
          <h1>
            {tr('让每一次运维\n都有据可循', 'Clarity for every\noperation')}
          </h1>
          <p>
            {tr(
              '从资源洞察到风险处置，让企业运维形成闭环。',
              'Connect resource insights, risk response and operational knowledge.',
            )}
          </p>
          <div className="ex-orbit">
            <Activity size={74} />
            <span>AI</span>
          </div>
          <small>
            {tr(
              '资产洞察 · 风险响应 · 智能作业',
              'Asset insights · Risk response · Intelligent operations',
            )}
          </small>
        </div>
        <main className="ex-login-form">
          <Logo size={46} />
          <p className="ex-eyebrow">YOUR OPERATIONS WORKSPACE</p>
          <h2>
            {tr('企业AI 智能运维平台', 'Enterprise AI Operations Platform')}
          </h2>
          <p className="ex-muted">
            {tr(
              '欢迎登录企业运维工作台',
              'Welcome to your enterprise operations workspace',
            )}
          </p>
          <Card>
            <form
              className="ex-login-fields"
              onSubmit={(e) => {
                e.preventDefault();
                if (username.trim().length < 3 || password.length < 6) {
                  setLoginError(true);
                  return;
                }
                setLoginError(false);
                setPassword('');
                enter();
              }}
            >
              <Label htmlFor="login-account">{tr('账号', 'Account')}</Label>
              <Input
                id="login-account"
                name="username"
                autoComplete="username"
                required
                minLength={3}
                maxLength={64}
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder={tr(
                  '请输入账号（至少 3 位）',
                  'Account (at least 3 characters)',
                )}
                aria-describedby="login-note"
                aria-invalid={loginError}
              />
              <Label htmlFor="login-password">{tr('密码', 'Password')}</Label>
              <Input
                id="login-password"
                name="password"
                type="password"
                autoComplete="current-password"
                required
                minLength={6}
                maxLength={128}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={tr(
                  '请输入密码（至少 6 位）',
                  'Password (at least 6 characters)',
                )}
                aria-describedby="login-note"
                aria-invalid={loginError}
              />
              {loginError && (
                <p role="alert">
                  {tr(
                    '账号至少 3 位，密码至少 6 位，请检查输入。',
                    'Use an account of at least 3 characters and a password of at least 6 characters.',
                  )}
                </p>
              )}
              <Button type="submit" variant="primary" className="w-full">
                {tr('登录', 'Sign in')}
                <ArrowRight size={16} />
              </Button>
              <small id="login-note" className="ex-muted">
                {tr(
                  '本地表单校验，不提供服务端身份认证；请勿输入真实系统密码。',
                  'Local form validation only; no server authentication. Do not enter passwords used by other systems.',
                )}
              </small>
            </form>
          </Card>
          <Button variant="link" onClick={toggleLocale}>
            {tr('Switch to English', '切换为中文')}
          </Button>
          <p className="ex-muted ex-small">
            {tr(
              '模拟数据与预设 AI 结果，不连接真实设备。',
              'Simulated data and preset AI results. No real devices connected.',
            )}
          </p>
          <a href="/LICENSE.txt">{tr('开源许可证', 'Open source license')}</a>
        </main>
      </div>
    );
  return (
    <Context.Provider value={{ data, commit }}>
      <div className="ex-app">
        <header className="ex-topbar">
          <Link to="/overview" className="ex-brand">
            <Logo />
            <span>
              {tr('企业AI 智能运维平台', 'Enterprise AI Operations Platform')}
              <small>OPERATIONS WORKSPACE</small>
            </span>
          </Link>
          <nav
            className="ex-primary"
            aria-label={tr('业务导航', 'Business navigation')}
          >
            {sections.map((s) => (
              <Link
                key={s.path}
                to={s.path}
                aria-current={s === current ? 'page' : undefined}
                className={s === current ? 'active' : ''}
              >
                {s.name}
              </Link>
            ))}
          </nav>
          <div className="ex-account">
            <Chip tone="info">{tr('企业工作台', 'Enterprise workspace')}</Chip>
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <Button
                    variant="subtle"
                    aria-label={tr('账户菜单', 'Account menu')}
                  />
                }
              >
                <span className="ex-avatar">XL</span>
                <span className="ex-account-name">
                  {tr('运维操作员', 'Operations operator')}
                </span>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onClick={toggleLocale}>
                  {tr('切换 English', '切换中文')}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={toggleEnterpriseTheme}>
                  {tr('切换深浅主题', 'Toggle light / dark')}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => void reset()}>
                  {tr('恢复示例数据', 'Restore sample data')}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => navigate('/about')}>
                  {tr('平台说明', 'About platform')}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={exit}>
                  {tr('退出登录', 'Sign out')}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </header>
        <div className="ex-workspace">
          <aside className="ex-sidebar">
            <div className="ex-tenant">
              <span className="ex-tenant-icon">
                <Layers3 size={20} />
              </span>
              <div>
                <strong>{tr('星澜企业', 'Starlane Enterprise')}</strong>
                <small>{tr('企业资源空间', 'Enterprise workspace')}</small>
              </div>
            </div>
            <p className="ex-side-label">{current.name}</p>
            <nav aria-label={tr('区域导航', 'Section navigation')}>
              {current.items.map(([path, name]) => (
                <NavLink key={path} to={path} end>
                  <ChevronRight size={15} />
                  {name}
                </NavLink>
              ))}
            </nav>
            <div className="ex-sidebar-bottom">
              <div className="ex-live-dot" />
              {tr(
                '本地样本快照 · 独立存储',
                'Local sample snapshots · Isolated storage',
              )}
              <hr />
              <a href="/enterprise-manual.docx" download>
                <ArrowDownToLine size={15} />
                {tr('下载操作手册', 'Download user manual')}
              </a>
              <Link to="/about">
                {tr('平台说明与软件许可', 'About & licenses')}
              </Link>
            </div>
          </aside>
          <div className="ex-main">
            <div className="ex-breadcrumb">
              <Button
                size="sm"
                variant="subtle"
                className="ex-mobile-menu"
                aria-label={tr('打开导航', 'Open navigation')}
                onClick={() => setDrawer(true)}
              >
                <Menu size={18} />
              </Button>
              <span>{tr('企业工作台', 'Workspace')}</span>
              <ChevronRight size={13} />
              <span>
                {location.pathname === '/about'
                  ? tr('平台说明', 'About platform')
                  : current.name}
              </span>
              <span className="ex-breadcrumb-right">
                {tr('数据快照', 'Snapshot')} {date(data.anchor)}
              </span>
            </div>
            {warning && (
              <div role="alert" className="ex-warning">
                {tr(
                  '浏览器存储不可用或数据版本异常，当前更改可能仅在本次会话保留。请备份知识文本，或恢复示例后重试。',
                  'Browser storage is unavailable or data is invalid. Changes may last only for this session. Back up edited text or restore samples and retry.',
                )}
              </div>
            )}
            <main
              ref={viewRef}
              tabIndex={-1}
              className="ex-content"
              id="main-content"
            >
              <Routes>
                <Route path="/overview" element={<Overview />} />
                <Route path="/risk" element={<Incidents />} />
                <Route path="/risk/:id" element={<IncidentDetail />} />
                <Route path="/inventory" element={<Inventory />} />
                <Route path="/inventory/:id" element={<ResourceDetail />} />
                <Route path="/observations" element={<Metrics />} />
                <Route
                  path="/monitoring/:category"
                  element={
                    <Monitoring
                      key={location.pathname}
                      resources={data.resources}
                      anchor={data.anchor}
                    />
                  }
                />
                <Route path="/intelligence" element={<Assistant />} />
                <Route path="/automation" element={<Automation />} />
                <Route path="/executions" element={<Executions />} />
                <Route path="/library" element={<Knowledge />} />
                <Route path="/library/:id" element={<Knowledge />} />
                <Route path="/about" element={<About />} />
                <Route path="*" element={<Navigate to="/overview" replace />} />
              </Routes>
              <footer className="ex-footer">
                <span>ENTERPRISE AI · OPERATIONS WORKSPACE</span>
                <span>
                  {tr('数据快照 · 本地保存', 'Data snapshots · Local storage')}
                </span>
              </footer>
            </main>
          </div>
        </div>
        <Dialog open={drawer} onOpenChange={setDrawer}>
          <DialogContent className="p-5">
            <DialogTitle>
              {tr('工作台导航', 'Workspace navigation')}
            </DialogTitle>
            <DialogDescription>
              {tr('选择业务区域和页面', 'Choose a business area and page')}
            </DialogDescription>
            <nav className="ex-drawer">
              {sections.flatMap((s) =>
                s.items.map(([path, name]) => (
                  <Link key={path} to={path}>
                    {s.name} / {name}
                  </Link>
                )),
              )}
              <Button onClick={() => setDrawer(false)}>
                {tr('关闭', 'Close')}
              </Button>
            </nav>
          </DialogContent>
        </Dialog>
        {dialog}
      </div>
    </Context.Provider>
  );
}

function Overview() {
  const { tr } = useI18n();
  const { data } = useSandbox();
  const text = useText();
  const open = data.incidents.filter((i) => i.status !== 'closed');
  return (
    <>
      <Heading
        title={tr('运维总览', 'Operations overview')}
        subtitle={tr(
          '掌握企业运行态势，让风险处置更进一步。',
          'Understand your operating landscape and move every response forward.',
        )}
        action={
          <Link className="ex-link-button" to="/intelligence">
            <Sparkles size={16} />
            {tr('唤起智能助手', 'Open AI assistant')}
          </Link>
        }
      />
      <div className="ex-hero">
        <div>
          <Chip tone="info">
            {tr('今日运行摘要', 'Daily operational brief')}
          </Chip>
          <h2>
            {tr('全局可见，处置有序', 'A clear view. A coordinated response.')}
          </h2>
          <p>
            {tr(
              `已纳管 ${data.resources.length} 项资源，${open.length} 项风险等待跟进。建议优先关注订单服务高 CPU 与支付接口延迟。`,
              `${data.resources.length} managed resources and ${open.length} active incidents. Prioritize order service CPU and payment API latency.`,
            )}
          </p>
          <Link to="/risk">
            {tr('查看风险清单', 'Review active incidents')}
            <ArrowRight size={16} />
          </Link>
        </div>
        <div className="ex-hero-mark">
          <Logo size={100} />
          <span>OBSERVE / UNDERSTAND / ACT</span>
        </div>
      </div>
      <div className="ex-stats">
        {[
          [
            tr('纳管资源', 'Managed resources'),
            data.resources.length,
            tr('覆盖三大业务域', 'Across three business groups'),
            Server,
          ],
          [
            tr('资源健康率', 'Resource health'),
            `${Math.round((data.resources.filter((r) => r.status === 'healthy').length / data.resources.length) * 100)}%`,
            tr('基于模拟状态', 'Based on simulated status'),
            Activity,
          ],
          [
            tr('待跟进风险', 'Active incidents'),
            open.length,
            tr('包含处理中告警', 'Includes acknowledged incidents'),
            ShieldCheck,
          ],
          [
            tr('自动化执行', 'Workflow executions'),
            data.runs.length,
            tr('全部为模拟执行', 'All executions are simulated'),
            Workflow,
          ],
        ].map(([label, value, hint, Icon]) => {
          const I = Icon as typeof Server;
          return (
            <Card key={String(label)} className="ex-stat">
              <span className="ex-stat-icon">
                <I size={20} />
              </span>
              <p>{String(label)}</p>
              <strong>{String(value)}</strong>
              <small>{String(hint)}</small>
            </Card>
          );
        })}
      </div>
      <div className="ex-two-col">
        <Card>
          <div className="ex-card-title">
            <h2>{tr('资源负载趋势', 'Resource load trend')}</h2>
            <Chip>{tr('近 24 小时', 'Last 24 hours')}</Chip>
          </div>
          <Trend cpu={55} hours={24} />
        </Card>
        <Card>
          <div className="ex-card-title">
            <h2>{tr('优先处置', 'Response priorities')}</h2>
            <Link to="/risk">{tr('全部', 'View all')} →</Link>
          </div>
          <div className="ex-list">
            {open.slice(0, 4).map((i) => (
              <Link to={`/risk/${i.id}`} key={i.id}>
                <span className={`ex-severity ${i.severity}`} />
                <div>
                  <strong>{text(i.title)}</strong>
                  <small>
                    {i.id} ·{' '}
                    {data.resources.find((r) => r.id === i.resourceId)?.name}
                  </small>
                </div>
                <ChevronRight size={16} />
              </Link>
            ))}
          </div>
        </Card>
      </div>
      <div className="ex-section-title">
        <h2>{tr('工作台快捷入口', 'Continue your work')}</h2>
        <span>{tr('从洞察到行动', 'From insight to action')}</span>
      </div>
      <div className="ex-three-col">
        {[
          [
            tr('资源全景', 'Resource landscape'),
            tr('查看业务资产与运行状态', 'Review assets and operating status'),
            '/inventory',
            Server,
          ],
          [
            tr('智能巡检', 'Intelligent inspection'),
            tr('模拟执行标准化巡检流程', 'Simulate a standardized inspection'),
            '/automation',
            Workflow,
          ],
          [
            tr('知识沉淀', 'Operational knowledge'),
            tr(
              '把处置经验变成可复用规程',
              'Turn experience into reusable procedures',
            ),
            '/library',
            BookOpen,
          ],
        ].map(([title, hint, path, Icon]) => {
          const I = Icon as typeof Server;
          return (
            <Link key={String(path)} to={String(path)} className="ex-shortcut">
              <I size={24} />
              <div>
                <strong>{String(title)}</strong>
                <p>{String(hint)}</p>
              </div>
              <ArrowRight size={17} />
            </Link>
          );
        })}
      </div>
    </>
  );
}
function Trend({ cpu, hours }: { cpu: number; hours: number }) {
  const { tr } = useI18n();
  return (
    <>
      <div className="ex-chart-legend">
        <span className="ex-live-dot" />
        CPU <span className="ex-memory-dot" />
        {tr('内存', 'Memory')}{' '}
        <small>
          {tr('模拟数据 · 使用率 %', 'Simulated data · Utilization %')}
        </small>
      </div>
      <div
        className="ex-chart"
        role="img"
        aria-label={tr(
          `最近 ${hours} 小时 CPU 和内存使用率模拟趋势`,
          `Simulated CPU and memory utilization over ${hours} hours`,
        )}
      >
        <ResponsiveContainer width="100%" height="100%">
          <AreaChart
            data={metricPoints(cpu, hours)}
            margin={{ top: 15, right: 12, left: -25, bottom: 0 }}
          >
            <CartesianGrid
              strokeDasharray="3 5"
              vertical={false}
              stroke="var(--ex-line)"
            />
            <XAxis
              dataKey="hour"
              tick={{ fill: 'var(--ex-muted)', fontSize: 11 }}
              minTickGap={35}
              axisLine={false}
              tickLine={false}
            />
            <YAxis
              domain={[0, 100]}
              tick={{ fill: 'var(--ex-muted)', fontSize: 11 }}
              axisLine={false}
              tickLine={false}
            />
            <ChartTooltip
              contentStyle={{
                background: 'var(--ex-surface)',
                border: '1px solid var(--ex-line)',
                borderRadius: 8,
                color: 'var(--ex-ink)',
              }}
            />
            <Area
              name="CPU %"
              type="monotone"
              dataKey="cpu"
              stroke="#2880ef"
              fill="#2880ef"
              fillOpacity={0.08}
              isAnimationActive={false}
            />
            <Area
              name={tr('内存 %', 'Memory %')}
              type="monotone"
              dataKey="memory"
              stroke="#17aaa5"
              fill="#17aaa5"
              fillOpacity={0.04}
              isAnimationActive={false}
            />
          </AreaChart>
        </ResponsiveContainer>
      </div>
    </>
  );
}
function Incidents() {
  const { tr } = useI18n();
  const { data } = useSandbox();
  const text = useText();
  const [status, setStatus] = useState('all');
  const [query, setQuery] = useState('');
  const items = data.incidents.filter(
    (i) =>
      (status === 'all' || i.status === status) &&
      `${i.id} ${text(i.title)}`.toLowerCase().includes(query.toLowerCase()),
  );
  return (
    <>
      <Heading
        title={tr('告警中心', 'Incident center')}
        subtitle={tr(
          '关联业务影响，记录每一步风险处置。',
          'Connect business impact and record each response.',
        )}
      />
      <div className="ex-filters">
        <Input
          type="search"
          aria-label={tr('搜索告警', 'Search incidents')}
          placeholder={tr('搜索告警名称或编号', 'Search incident title or ID')}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <Select
          label={tr('告警状态', 'Incident status')}
          value={status}
          onValueChange={setStatus}
          options={[
            { value: 'all', label: tr('全部状态', 'All statuses') },
            { value: 'open', label: tr('待处置', 'Open') },
            { value: 'acknowledged', label: tr('处理中', 'In progress') },
            { value: 'closed', label: tr('已关闭', 'Closed') },
          ]}
        />
        <span className="ex-muted">
          {tr(`共 ${items.length} 条`, `${items.length} incidents`)}
        </span>
      </div>
      <Card className="ex-table-card">
        <div className="ex-table-scroll">
          <table>
            <thead>
              <tr>
                {[
                  tr('告警编号', 'Incident ID'),
                  tr('风险与业务影响', 'Risk & business impact'),
                  tr('关联资源', 'Resource'),
                  tr('级别', 'Severity'),
                  tr('状态', 'Status'),
                  tr('触发时间', 'Triggered'),
                  tr('操作', 'Action'),
                ].map((h) => (
                  <th key={h}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {items.map((i) => (
                <tr key={i.id}>
                  <td className="ex-code">{i.id}</td>
                  <td className="ex-wide-cell">
                    <Link to={`/risk/${i.id}`}>{text(i.title)}</Link>
                  </td>
                  <td>
                    {data.resources.find((r) => r.id === i.resourceId)?.name}
                  </td>
                  <td>
                    <Chip
                      tone={
                        i.severity === 'critical'
                          ? 'danger'
                          : i.severity === 'warning'
                            ? 'warning'
                            : 'info'
                      }
                    >
                      {i.severity === 'critical'
                        ? tr('严重', 'Critical')
                        : i.severity === 'warning'
                          ? tr('警告', 'Warning')
                          : tr('提示', 'Info')}
                    </Chip>
                  </td>
                  <td>
                    <Stamp value={i.status} />
                  </td>
                  <td>{date(i.time)}</td>
                  <td>
                    <Link to={`/risk/${i.id}`}>
                      {tr('查看详情', 'View details')}
                    </Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!items.length && (
          <EmptyState
            title={tr('没有匹配告警', 'No matching incidents')}
            action={
              <Button
                onClick={() => {
                  setStatus('all');
                  setQuery('');
                }}
              >
                {tr('清除筛选', 'Clear filters')}
              </Button>
            }
          />
        )}
      </Card>
    </>
  );
}
function IncidentDetail() {
  const { id } = useParams();
  const { data, commit } = useSandbox();
  const { tr } = useI18n();
  const text = useText();
  const { dialog, confirmAction } = useDialogs();
  const i = data.incidents.find((x) => x.id === id);
  if (!i) return <Missing />;
  const resource = data.resources.find((r) => r.id === i.resourceId)!;
  return (
    <>
      <Heading
        title={text(i.title)}
        subtitle={`${i.id} · ${date(i.time)}`}
        action={<Stamp value={i.status} />}
      />
      <div className="ex-two-col">
        <Card>
          <h2>{tr('事件上下文', 'Incident context')}</h2>
          <dl className="ex-details">
            <dt>{tr('关联资源', 'Affected resource')}</dt>
            <dd>
              <Link to={`/inventory/${resource.id}`}>{resource.name}</Link>
            </dd>
            <dt>{tr('业务域', 'Business group')}</dt>
            <dd>{resource.group}</dd>
            <dt>{tr('影响说明', 'Impact')}</dt>
            <dd>
              {tr(
                '模拟业务请求处理能力下降，需要人工确认原因。',
                'Simulated request processing degradation requiring investigation.',
              )}
            </dd>
            <dt>{tr('当前状态', 'Current status')}</dt>
            <dd>
              <Stamp value={i.status} />
            </dd>
          </dl>
          <div className="ex-actions">
            <Button
              disabled={i.status !== 'open'}
              variant="primary"
              onClick={() => commit(updateIncident(data, i.id, 'acknowledged'))}
            >
              {tr('确认告警', 'Acknowledge')}
            </Button>
            <Button
              disabled={i.status === 'closed'}
              onClick={async () => {
                if (
                  await confirmAction(
                    tr('确认关闭这条告警？', 'Close this incident?'),
                  )
                )
                  commit(updateIncident(data, i.id, 'closed'));
              }}
            >
              {tr('关闭告警', 'Close incident')}
            </Button>
          </div>
        </Card>
        <Card>
          <h2>{tr('建议处置路径', 'Recommended response')}</h2>
          <ol className="ex-steps">
            <li>
              {tr(
                '确认资源及业务影响范围',
                'Identify the resource and business impact',
              )}
            </li>
            <li>
              {tr(
                '检查指标趋势与相关知识',
                'Review metrics and related knowledge',
              )}
            </li>
            <li>
              {tr(
                '通过智能助手获取模拟分析',
                'Get simulated analysis from the AI assistant',
              )}
            </li>
            <li>
              {tr(
                '执行模拟流程并核对记录',
                'Simulate a workflow and review its record',
              )}
            </li>
          </ol>
          <div className="ex-actions">
            <Link
              to={`/intelligence?scenario=${i.resourceId === 'res-2' ? 'latency' : 'cpu'}`}
              className="ex-link-button"
            >
              <Sparkles size={16} />
              {tr('AI 辅助分析', 'Analyze with AI')}
            </Link>
            <Link to={`/library/${i.resourceId === 'res-2' ? 'kb-2' : 'kb-1'}`}>
              {tr('查看关联知识', 'Related knowledge')}
            </Link>
          </div>
        </Card>
      </div>
      {dialog}
    </>
  );
}
function Inventory() {
  const { data } = useSandbox();
  const { tr } = useI18n();
  const [query, setQuery] = useState('');
  const [group, setGroup] = useState('all');
  const items = data.resources.filter(
    (r) =>
      `${r.name} ${r.ip}`.toLowerCase().includes(query.toLowerCase()) &&
      (group === 'all' || r.group === group),
  );
  return (
    <>
      <Heading
        title={tr('资源台账', 'Resource inventory')}
        subtitle={tr(
          '按业务组织资产，统一查看健康状态与容量。',
          'Organize assets by business, health and capacity.',
        )}
      />
      <div className="ex-filters">
        <Input
          type="search"
          aria-label={tr('搜索资源', 'Search resources')}
          placeholder={tr('搜索资源名称或 IP', 'Search resource name or IP')}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <Select
          label={tr('业务域', 'Business group')}
          value={group}
          onValueChange={setGroup}
          options={[
            { value: 'all', label: tr('全部业务域', 'All business groups') },
            ...['commerce', 'finance', 'platform'].map((g) => ({
              value: g,
              label: g,
            })),
          ]}
        />
        <span className="ex-muted">
          {tr(`共 ${items.length} 项资源`, `${items.length} resources`)}
        </span>
      </div>
      <Card className="ex-table-card">
        <div className="ex-table-scroll">
          <table>
            <thead>
              <tr>
                {[
                  tr('资源名称', 'Resource name'),
                  tr('业务域', 'Business group'),
                  tr('地址', 'Address'),
                  tr('类型', 'Type'),
                  'CPU',
                  tr('内存', 'Memory'),
                  tr('状态', 'Status'),
                  tr('操作', 'Action'),
                ].map((h) => (
                  <th key={h}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {items.map((r) => (
                <tr key={r.id}>
                  <td className="ex-resource-name">
                    <Server size={16} />
                    <Link to={`/inventory/${r.id}`}>{r.name}</Link>
                  </td>
                  <td>{r.group}</td>
                  <td className="ex-code">{r.ip}</td>
                  <td>
                    {r.kind === 'server'
                      ? tr('应用主机', 'App host')
                      : r.kind === 'database'
                        ? tr('数据库', 'Database')
                        : tr('网络设备', 'Network')}
                  </td>
                  <td>
                    <div className="ex-meter">
                      <span style={{ width: `${r.cpu}%` }} />
                    </div>
                    {r.cpu}%
                  </td>
                  <td>{r.memory}%</td>
                  <td>
                    <Stamp value={r.status} />
                  </td>
                  <td>
                    <Link to={`/inventory/${r.id}`}>
                      {tr('查看详情', 'View details')}
                    </Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!items.length && (
          <EmptyState
            title={tr('没有匹配资源', 'No matching resources')}
            action={
              <Button
                onClick={() => {
                  setQuery('');
                  setGroup('all');
                }}
              >
                {tr('清除筛选', 'Clear filters')}
              </Button>
            }
          />
        )}
      </Card>
    </>
  );
}
function ResourceDetail() {
  const { id } = useParams();
  const { data } = useSandbox();
  const { tr } = useI18n();
  const text = useText();
  const r = data.resources.find((x) => x.id === id);
  if (!r) return <Missing />;
  return (
    <>
      <Heading
        title={r.name}
        subtitle={`${r.ip} · ${r.group}`}
        action={<Stamp value={r.status} />}
      />
      <div className="ex-two-col">
        <Card>
          <h2>{tr('运行概况', 'Operating profile')}</h2>
          <dl className="ex-details">
            <dt>CPU</dt>
            <dd>{r.cpu}%</dd>
            <dt>{tr('内存使用率', 'Memory utilization')}</dt>
            <dd>{r.memory}%</dd>
            <dt>{tr('资源编号', 'Resource ID')}</dt>
            <dd>{r.id}</dd>
            <dt>{tr('所属空间', 'Workspace')}</dt>
            <dd>{tr('星澜企业示例空间', 'Starlane sample workspace')}</dd>
          </dl>
          <Link to={`/observations?resource=${r.id}`}>
            {tr('查看指标监测', 'View metrics')} →
          </Link>
        </Card>
        <Card>
          <h2>{tr('关联告警', 'Related incidents')}</h2>
          <div className="ex-list">
            {data.incidents
              .filter((i) => i.resourceId === r.id)
              .map((i) => (
                <Link to={`/risk/${i.id}`} key={i.id}>
                  <div>
                    <strong>{text(i.title)}</strong>
                    <small>{i.id}</small>
                  </div>
                  <Stamp value={i.status} />
                </Link>
              ))}
          </div>
          {!data.incidents.some((i) => i.resourceId === r.id) && (
            <EmptyState
              title={tr(
                '当前资源没有示例告警',
                'No sample incidents for this resource',
              )}
            />
          )}
        </Card>
      </div>
      <Card>
        <h2>{tr('近 24 小时负载', 'Load over the last 24 hours')}</h2>
        <Trend cpu={r.cpu} hours={24} />
      </Card>
    </>
  );
}
function Metrics() {
  const { tr } = useI18n();
  const { data } = useSandbox();
  const location = useLocation();
  const requested = new URLSearchParams(location.search).get('resource');
  const [resource, setResource] = useState(
    data.resources.some((r) => r.id === requested) ? requested! : 'res-1',
  );
  const [hours, setHours] = useState('24');
  const r = data.resources.find((x) => x.id === resource)!;
  return (
    <>
      <Heading
        title={tr('指标监测', 'Metric explorer')}
        subtitle={tr(
          '按资源和时间范围检查模拟容量趋势。',
          'Explore simulated capacity by resource and time range.',
        )}
      />
      <div className="ex-filters">
        <Select
          label={tr('监测资源', 'Monitored resource')}
          value={resource}
          onValueChange={setResource}
          options={data.resources.map((r) => ({ value: r.id, label: r.name }))}
        />
        <Select
          label={tr('时间范围', 'Time range')}
          value={hours}
          onValueChange={setHours}
          options={['6', '12', '24'].map((h) => ({
            value: h,
            label: tr(`近 ${h} 小时`, `Last ${h} hours`),
          }))}
        />
      </div>
      <div className="ex-stats ex-stats-two">
        <Card className="ex-stat">
          <p>{tr('当前 CPU 使用率', 'Current CPU utilization')}</p>
          <strong>{r.cpu}%</strong>
          <small>{r.name}</small>
        </Card>
        <Card className="ex-stat">
          <p>{tr('当前内存使用率', 'Current memory utilization')}</p>
          <strong>{r.memory}%</strong>
          <small>{tr('固定示例快照', 'Fixed sample snapshot')}</small>
        </Card>
      </div>
      <Card>
        <h2>{tr('容量趋势', 'Capacity trend')}</h2>
        <Trend cpu={r.cpu} hours={Number(hours)} />
      </Card>
    </>
  );
}
function Assistant() {
  const { tr } = useI18n();
  const text = useText();
  const { data } = useSandbox();
  const location = useLocation();
  const [question, setQuestion] = useState('');
  const [scenario, setScenario] = useState(
    new URLSearchParams(location.search).get('scenario') ?? '',
  );
  const [reply, setReply] = useState('');
  const [asked, setAsked] = useState('');
  const answers = (id: string) =>
    id === 'cpu'
      ? tr(
          `资源 ${data.resources[0].name} 的 CPU 快照为 ${data.resources[0].cpu}%。示例告警 INC-1201 指向订单服务负载升高。建议核对最近应用变更，检查热点进程，再评估限流或扩容。本沙盘不会执行真实命令。`,
          `Resource ${data.resources[0].name} has a CPU snapshot of ${data.resources[0].cpu}%. Sample incident INC-1201 indicates elevated order service load. Review recent changes, inspect hot processes and evaluate rate limits or scaling. This sandbox does not execute real commands.`,
        )
      : id === 'latency'
        ? tr(
            '支付服务示例告警 INC-1202 显示接口 P95 延迟升高。模拟关联分析提示检查数据库连接池和慢查询。建议先确认影响范围，再运行接口延迟诊断流程。',
            'Sample payment incident INC-1202 shows increased API P95 latency. Simulated correlation suggests checking the database connection pool and slow queries. Confirm impact, then run the API latency workflow.',
          )
        : tr(
            `本次示例巡检覆盖 ${data.resources.length} 项资源，发现 ${data.resources.filter((r) => r.status === 'warning').length} 项资源需要关注，${data.incidents.filter((i) => i.status !== 'closed').length} 条告警尚未关闭。建议优先处理严重告警，并通过每日健康巡检保留执行记录。`,
            `The sample inspection covers ${data.resources.length} resources: ${data.resources.filter((r) => r.status === 'warning').length} need attention and ${data.incidents.filter((i) => i.status !== 'closed').length} incidents remain active. Prioritize critical incidents and retain a daily inspection record.`,
          );
  const run = (id: string, q: string) => {
    setAsked(q);
    setScenario(id);
    setReply(
      id
        ? answers(id)
        : tr(
            '当前支持高 CPU 排查、接口延迟分析和每日巡检。请选择下方问题查看完整分析流程。',
            'Supports high CPU investigation, API latency analysis and daily inspection. Choose a question to explore the workflow.',
          ),
    );
    setQuestion('');
  };
  const submit = () => {
    const q = question.trim();
    if (!q) return;
    run(
      /cpu|负载/i.test(q)
        ? 'cpu'
        : /延迟|latency/i.test(q)
          ? 'latency'
          : /巡检|inspection/i.test(q)
            ? 'daily'
            : '',
      q,
    );
  };
  return (
    <>
      <Heading
        title={tr('AI 运维助手', 'AI operations assistant')}
        subtitle={tr(
          '结合资源、告警与知识，为处置提供参考。',
          'Bring resources, incidents and knowledge into your response.',
        )}
        action={
          <Chip tone="info">
            {tr('预设结果 · 模拟分析', 'Preset results · Simulated analysis')}
          </Chip>
        }
      />
      <div className="ex-assistant">
        <Card className="ex-conversation">
          <span className="ex-assistant-icon">
            <Bot size={29} />
          </span>
          <h2>
            {tr(
              '今天需要排查什么问题？',
              'What would you like to investigate?',
            )}
          </h2>
          <p className="ex-muted">
            {tr(
              '选择一个场景，查看证据、建议和下一步。',
              'Choose a scenario to explore evidence, advice and next steps.',
            )}
          </p>
          <div className="ex-prompts">
            {flows.map((f) => (
              <Button key={f.id} onClick={() => run(f.id, text(f.title))}>
                <Sparkles size={15} />
                {text(f.title)}
              </Button>
            ))}
          </div>
          {reply && (
            <div className="ex-answer" aria-live="polite">
              <div className="ex-question">{asked}</div>
              <p className="ex-eyebrow">
                {tr('模拟分析结果', 'SIMULATED ANALYSIS')}
              </p>
              <p>{reply}</p>
              {scenario && (
                <>
                  <div className="ex-evidence">
                    <Link
                      to={`/risk/${scenario === 'latency' ? 'INC-1202' : 'INC-1201'}`}
                    >
                      {tr('查看告警证据', 'View incident evidence')}
                    </Link>
                    <Link
                      to={`/library/${scenario === 'latency' ? 'kb-2' : scenario === 'daily' ? 'kb-3' : 'kb-1'}`}
                    >
                      {tr('查看参考知识', 'Reference knowledge')}
                    </Link>
                  </div>
                  <Link
                    to={`/automation?flow=${scenario}`}
                    className="ex-link-button"
                  >
                    {tr('前往关联流程', 'Open related workflow')}
                    <ArrowRight size={15} />
                  </Link>
                </>
              )}
            </div>
          )}
          {scenario && !reply && (
            <Button
              variant="primary"
              onClick={() =>
                run(
                  scenario,
                  text(
                    flows.find((f) => f.id === scenario)?.title ??
                      flows[0].title,
                  ),
                )
              }
            >
              {tr('分析关联场景', 'Analyze linked scenario')}
            </Button>
          )}
          <form
            className="ex-composer"
            onSubmit={(e) => {
              e.preventDefault();
              submit();
            }}
          >
            <Label htmlFor="question">
              {tr('你的运维问题', 'Your operations question')}
            </Label>
            <Textarea
              id="question"
              value={question}
              onChange={(e) => setQuestion(e.target.value)}
              maxLength={2000}
              placeholder={tr(
                '例如：订单服务 CPU 为什么升高？',
                'For example: Why is order service CPU high?',
              )}
            />
            <div>
              <span>
                {tr(
                  '不连接外部模型，不执行真实命令',
                  'No external model or real command execution',
                )}
              </span>
              <Button
                type="submit"
                variant="primary"
                disabled={!question.trim()}
              >
                {tr('发送问题', 'Send question')}
                <ArrowRight size={15} />
              </Button>
            </div>
          </form>
        </Card>
        <Card className="ex-assistant-guide">
          <p className="ex-eyebrow">CONNECTED CONTEXT</p>
          <h2>{tr('分析上下文', 'Analysis context')}</h2>
          <div className="ex-context-item">
            <Server size={20} />
            <span>
              {tr('资源快照', 'Resource snapshots')}
              <strong>{data.resources.length}</strong>
            </span>
          </div>
          <div className="ex-context-item">
            <ShieldCheck size={20} />
            <span>
              {tr('事件线索', 'Incident signals')}
              <strong>{data.incidents.length}</strong>
            </span>
          </div>
          <div className="ex-context-item">
            <BookOpen size={20} />
            <span>
              {tr('知识文档', 'Knowledge documents')}
              <strong>{data.articles.length}</strong>
            </span>
          </div>
          <p className="ex-muted">
            {tr(
              '回答为预设分析结果；请勿用于生产决策。',
              'Answers are preset analysis results. Do not use them for production decisions.',
            )}
          </p>
        </Card>
      </div>
    </>
  );
}
function Automation() {
  const { tr } = useI18n();
  const text = useText();
  const { data, commit } = useSandbox();
  const [pending, setPending] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const { dialog, confirmAction } = useDialogs();
  const latest = useRef(data);
  latest.current = data;
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const selected = new URLSearchParams(useLocation().search).get('flow');
  const execute = async (id: string) => {
    if (pending) return;
    setPending(id);
    const ok = await confirmAction(
      tr(
        '开始模拟执行？仅生成本浏览器的执行记录，不操作真实设备。',
        'Start simulated execution? This only creates a record in this browser and never operates real devices.',
      ),
    );
    if (!mounted.current) return;
    if (ok) {
      commit(executeFlow(latest.current, id));
      setDone(id);
    }
    setPending(null);
  };
  return (
    <>
      <Heading
        title={tr('自动化流程', 'Automation workflows')}
        subtitle={tr(
          '把标准处置步骤编排成可复用的作业。',
          'Turn standard response steps into reusable operations.',
        )}
      />
      <div className="ex-three-col">
        {flows.map((f) => (
          <Card
            key={f.id}
            className={`ex-flow ${f.id === selected ? 'ex-highlight' : ''}`}
          >
            <span className="ex-stat-icon">
              <Workflow size={23} />
            </span>
            <Chip tone="info">{tr('模拟流程', 'Simulated workflow')}</Chip>
            <h2>{text(f.title)}</h2>
            <p className="ex-muted">{text(f.description)}</p>
            <ol className="ex-steps">
              {f.steps.map((s, i) => (
                <li key={i}>{text(s)}</li>
              ))}
            </ol>
            <Button
              variant="primary"
              disabled={pending !== null}
              onClick={() => void execute(f.id)}
            >
              <Play size={15} />
              {pending === f.id
                ? tr('等待确认', 'Awaiting confirmation')
                : tr('模拟执行', 'Simulate execution')}
            </Button>
            {done === f.id && (
              <p role="status" className="ex-success">
                {tr(
                  '模拟完成，执行记录已保存。',
                  'Simulation complete. Execution record saved.',
                )}{' '}
                <Link to="/executions">{tr('查看记录', 'View records')} →</Link>
              </p>
            )}
          </Card>
        ))}
      </div>
      {dialog}
    </>
  );
}
function Executions() {
  const { tr } = useI18n();
  const text = useText();
  const { data } = useSandbox();
  return (
    <>
      <Heading
        title={tr('执行记录', 'Execution history')}
        subtitle={tr(
          '追溯每次模拟执行的流程、时间和结果。',
          'Review the workflow, time and outcome of every simulation.',
        )}
      />
      <Card className="ex-table-card">
        <div className="ex-table-scroll">
          <table>
            <thead>
              <tr>
                {[
                  tr('执行编号', 'Execution ID'),
                  tr('流程', 'Workflow'),
                  tr('执行时间', 'Execution time'),
                  tr('步骤数', 'Steps'),
                  tr('结果', 'Result'),
                ].map((h) => (
                  <th key={h}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data.runs.map((r) => (
                <tr key={r.id}>
                  <td className="ex-code ex-id-cell">{r.id}</td>
                  <td>{text(flows.find((f) => f.id === r.flowId)!.title)}</td>
                  <td>{date(r.time)}</td>
                  <td>{r.steps}</td>
                  <td>
                    <Stamp value={r.status} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
      <p className="ex-muted ex-note">
        {tr(
          '记录仅证明沙盘模拟完成，不代表真实主机执行成功。最多保留最近 100 次模拟记录。',
          'Records indicate sandbox completion, not successful execution on real hosts. The latest 100 records are retained.',
        )}
      </p>
    </>
  );
}
function Knowledge() {
  const { id } = useParams();
  const { data, commit } = useSandbox();
  const { tr, locale } = useI18n();
  const text = useText();
  const navigate = useNavigate();
  const [query, setQuery] = useState('');
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState('');
  const [body, setBody] = useState('');
  const [draftLocale, setDraftLocale] = useState<'zh' | 'en'>('zh');
  const article = data.articles.find((a) => a.id === id);
  const isNew = id === 'new';
  useEffect(() => {
    setEditing(false);
  }, [id]);
  const edit = () => {
    setDraftLocale(locale === 'en-US' ? 'en' : 'zh');
    setTitle(article ? text(article.title) : '');
    setBody(article ? text(article.body) : '');
    setEditing(true);
  };
  const store = () => {
    if (!title.trim() || !body.trim()) return;
    const next: Article = article
      ? {
          ...article,
          title: { ...article.title, [draftLocale]: title.trim() },
          body: { ...article.body, [draftLocale]: body.trim() },
        }
      : {
          id: `kb-${uniqueId()}`,
          title: { zh: title.trim(), en: title.trim() },
          body: { zh: body.trim(), en: body.trim() },
          category: { zh: '自建知识', en: 'Custom knowledge' },
        };
    commit({
      ...data,
      articles: article
        ? data.articles.map((a) => (a.id === article.id ? next : a))
        : [next, ...data.articles],
    });
    setEditing(false);
    navigate(`/library/${next.id}`);
  };
  if (id && !article && !isNew) return <Missing />;
  if (id)
    return (
      <>
        <Heading
          title={isNew ? tr('新建知识', 'New knowledge') : text(article!.title)}
          subtitle={tr(
            '沉淀可复用的企业运维经验。',
            'Capture reusable enterprise operations knowledge.',
          )}
          action={
            !editing && !isNew ? (
              <Button onClick={edit}>{tr('编辑文档', 'Edit document')}</Button>
            ) : undefined
          }
        />
        {editing || isNew ? (
          <Card>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                store();
              }}
              className="ex-editor"
            >
              <Label htmlFor="kb-title">
                {tr('文档标题', 'Document title')}
              </Label>
              <Input
                id="kb-title"
                required
                maxLength={160}
                value={title}
                onChange={(e) => setTitle(e.target.value)}
              />
              <Label htmlFor="kb-body">{tr('正文', 'Body')}</Label>
              <Textarea
                id="kb-body"
                required
                maxLength={30000}
                rows={14}
                value={body}
                onChange={(e) => setBody(e.target.value)}
              />
              <div className="ex-actions">
                <Button
                  variant="primary"
                  type="submit"
                  disabled={!title.trim() || !body.trim()}
                >
                  {tr('保存文档', 'Save document')}
                </Button>
                <Button
                  onClick={() => {
                    setEditing(false);
                    if (isNew) navigate('/library');
                  }}
                >
                  {tr('取消', 'Cancel')}
                </Button>
              </div>
            </form>
          </Card>
        ) : (
          <Card>
            <Chip>{text(article!.category)}</Chip>
            <article className="ex-article">{text(article!.body)}</article>
            <Link to="/library">
              ← {tr('返回知识列表', 'Back to documents')}
            </Link>
          </Card>
        )}
      </>
    );
  const items = data.articles.filter((a) =>
    text(a.title).toLowerCase().includes(query.toLowerCase()),
  );
  return (
    <>
      <Heading
        title={tr('运维知识', 'Operations knowledge')}
        subtitle={tr(
          '汇集操作规程与排障经验，为每次处置提供依据。',
          'Bring procedures and experience together to guide every response.',
        )}
        action={
          <Button
            variant="primary"
            onClick={() => {
              setDraftLocale(locale === 'en-US' ? 'en' : 'zh');
              setTitle('');
              setBody('');
              navigate('/library/new');
            }}
          >
            <Plus size={16} />
            {tr('新建知识', 'New document')}
          </Button>
        }
      />
      <div className="ex-filters">
        <Input
          type="search"
          aria-label={tr('搜索知识', 'Search knowledge')}
          placeholder={tr('搜索知识文档', 'Search documents')}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <span className="ex-muted">
          {tr(`共 ${items.length} 篇`, `${items.length} documents`)}
        </span>
      </div>
      {items.length ? (
        <div className="ex-three-col">
          {items.map((a) => (
            <Link
              to={`/library/${a.id}`}
              key={a.id}
              className="ex-knowledge-card"
            >
              <BookOpen size={25} />
              <Chip>{text(a.category)}</Chip>
              <h2>{text(a.title)}</h2>
              <p>{text(a.body).split('\n')[0]}</p>
              <span>
                {tr('阅读文档', 'Read document')}
                <ArrowRight size={15} />
              </span>
            </Link>
          ))}
        </div>
      ) : (
        <EmptyStateCard
          title={tr('没有匹配文档', 'No matching documents')}
          action={
            <Button onClick={() => setQuery('')}>
              {tr('清除筛选', 'Clear filters')}
            </Button>
          }
        />
      )}
    </>
  );
}
function About() {
  const { tr } = useI18n();
  return (
    <>
      <Heading
        title={tr('平台说明', 'About the platform')}
        subtitle={tr(
          '版本、数据范围与软件许可',
          'Version, data scope and software licenses',
        )}
      />
      <Card>
        <Logo size={52} />
        <h2 className="mt-4">
          {tr('企业AI 智能运维平台', 'Enterprise AI Operations Platform')}
        </h2>
        <p className="ex-muted">
          {tr(
            '独立定制版本 · 企业运维工作台',
            'Independent distribution · Enterprise workspace',
          )}
        </p>
        <dl className="ex-details">
          <dt>{tr('运行版本', 'Build version')}</dt>
          <dd>{import.meta.env.VITE_ENTERPRISE_VERSION || 'development'}</dd>
          <dt>{tr('功能范围', 'Functional scope')}</dt>
          <dd>
            {tr(
              '虚构资源、模拟告警、预设 AI 结果、本地知识编辑与流程模拟。',
              'Fictional resources, simulated incidents, preset AI, local knowledge editing and simulated workflows.',
            )}
          </dd>
          <dt>{tr('数据存储', 'Data storage')}</dt>
          <dd>
            {tr(
              '业务数据仅在本浏览器保存。登录仅做本地表单校验，不提供服务端身份认证。',
              'Business data is saved in this browser. Sign-in validates the local form and does not provide server authentication.',
            )}
          </dd>
        </dl>
        <h2>{tr('软件许可', 'Software licenses')}</h2>
        <p>
          {tr(
            '本平台为独立定制发行版本。软件许可、第三方版权与来源声明及对应源码可通过以下入口查阅。软件按许可证条款提供，不附带担保。',
            'This is an independent distribution. Licenses, third-party copyright and source notices, and corresponding source are available below. The software is provided without warranty under its license terms.',
          )}
        </p>
        <div className="ex-actions">
          <a href="/LICENSE.txt">AGPL-3.0-only</a>
          <a href="/THIRD-PARTY-NOTICES.txt">
            {tr('第三方软件声明', 'Third-party software notices')}
          </a>
          <a href="/enterprise-source.tar.gz" download>
            {tr('下载本版本对应源码', 'Download corresponding source')}
          </a>
          <a href="/enterprise-manual.docx" download>
            {tr('下载操作手册', 'Download user manual')}
          </a>
        </div>
      </Card>
    </>
  );
}
function Missing() {
  const { tr } = useI18n();
  return (
    <EmptyStateCard
      title={tr('记录不存在', 'Record not found')}
      hint={tr(
        '记录可能已被恢复示例操作替换。',
        'The record may have been replaced when sample data was restored.',
      )}
      action={<Link to="/overview">{tr('返回总览', 'Back to overview')}</Link>}
    />
  );
}
