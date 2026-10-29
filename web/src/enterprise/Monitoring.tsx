import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { Activity, ArrowRight } from 'lucide-react';
import {
  Button,
  Card,
  Chip,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
  EmptyState,
  Input,
  Label,
  Select,
} from '@/components/ui';
import { useI18n } from '@/i18n/locale';
import type { Bilingual, Resource } from './data';

export const monitorPages = [
  ['overview', '监控总览', 'Monitoring overview'],
  ['hosts', '主机监控', 'Hosts'],
  ['applications', '应用服务', 'Applications'],
  ['databases', '数据库监控', 'Databases'],
  ['network', '网络设备', 'Network devices'],
  ['clusters', '容器集群', 'Container clusters'],
  ['availability', '接口可用性', 'Endpoint availability'],
  ['logs', '日志检索', 'Log explorer'],
  ['rules', '告警规则', 'Alert rules'],
] as const;
type Kind =
  | 'hosts'
  | 'applications'
  | 'databases'
  | 'network'
  | 'clusters'
  | 'availability';
type Target = {
  id: string;
  name: string;
  group: string;
  address: string;
  warning: boolean;
  values: number[];
  resource?: string;
};
type Definition = { columns: Bilingual[]; units: string[]; targets: Target[] };
const b = (zh: string, en: string): Bilingual => ({ zh, en });
export function monitoringData(
  resources: Resource[],
): Record<Kind, Definition> {
  const targets = (
    prefix: string,
    count: number,
    values: (i: number) => number[],
  ): Target[] =>
    Array.from({ length: count }, (_, i) => ({
      id: `${prefix}-${i + 1}`,
      name: `${prefix}-${String(i + 1).padStart(2, '0')}`,
      group: ['commerce', 'finance', 'platform'][i % 3],
      address: `10.42.${4 + Math.floor(i / 8)}.${20 + i}`,
      warning: i === 0,
      values: values(i),
    }));
  return {
    hosts: {
      columns: [
        b('CPU 使用率', 'CPU usage'),
        b('内存使用率', 'Memory usage'),
        b('磁盘使用率', 'Disk usage'),
        b('负载', 'Load'),
      ],
      units: ['%', '%', '%', ''],
      targets: resources
        .filter((r) => r.kind === 'server')
        .map((r, i) => ({
          id: r.id,
          name: r.name,
          group: r.group,
          address: r.ip,
          warning: r.status === 'warning',
          values: [r.cpu, r.memory, 41 + (i % 38), +(r.cpu / 24).toFixed(2)],
          resource: r.id,
        })),
    },
    applications: {
      columns: [
        b('请求速率', 'Request rate'),
        b('P95 延迟', 'P95 latency'),
        b('错误率', 'Error rate'),
        b('实例数', 'Instances'),
      ],
      units: [' req/s', ' ms', '%', ''],
      targets: targets('service', 12, (i) => [
        420 + i * 93,
        i === 0 ? 1250 : 85 + i * 17,
        i === 0 ? 2.4 : 0.02,
        3 + (i % 4),
      ]).map((t, i) => ({
        ...t,
        name: `${['order', 'payment', 'account', 'inventory'][i % 4]}-service-${Math.floor(i / 4) + 1}`,
      })),
    },
    databases: {
      columns: [
        b('查询速率', 'Queries per second'),
        b('连接使用率', 'Connection usage'),
        b('复制延迟', 'Replication lag'),
        b('缓存命中率', 'Cache hit rate'),
      ],
      units: [' QPS', '%', ' ms', '%'],
      targets: targets('mysql', 6, (i) => [
        1620 + i * 170,
        i === 0 ? 89 : 32 + i * 5,
        i === 0 ? 420 : 12 + i * 3,
        +(98.1 + i * 0.2).toFixed(1),
      ]),
    },
    network: {
      columns: [
        b('入站流量', 'Inbound traffic'),
        b('出站流量', 'Outbound traffic'),
        b('丢包率', 'Packet loss'),
        b('链路延迟', 'Link latency'),
      ],
      units: [' Mbps', ' Mbps', '%', ' ms'],
      targets: targets('core-switch', 8, (i) => [
        620 + i * 50,
        430 + i * 41,
        i === 0 ? 1.2 : 0,
        i === 0 ? 18 : 2 + i,
      ]),
    },
    clusters: {
      columns: [
        b('节点数', 'Nodes'),
        b('运行 Pod', 'Running pods'),
        b('CPU 分配率', 'CPU allocation'),
        b('内存分配率', 'Memory allocation'),
      ],
      units: ['', '', '%', '%'],
      targets: targets('k8s', 4, (i) => [
        6 + i * 2,
        128 + i * 38,
        i === 0 ? 87 : 48 + i * 4,
        63 + i * 3,
      ]),
    },
    availability: {
      columns: [
        b('可用率', 'Availability'),
        b('响应时间', 'Response time'),
        b('证书剩余', 'Certificate validity'),
        b('检查周期', 'Check interval'),
      ],
      units: ['%', ' ms', ' d', ' s'],
      targets: targets('endpoint', 10, (i) => [
        i === 0 ? 99.2 : 99.99,
        i === 0 ? 1280 : 65 + i * 23,
        18 + i * 24,
        60,
      ]).map((t, i) => ({
        ...t,
        address: `https://${['orders', 'payment', 'account', 'inventory', 'gateway'][i % 5]}.starlane.example/health/${i + 1}`,
      })),
    },
  };
}
function Trend({
  value,
  hours,
  label,
  unit,
}: {
  value: number;
  hours: number;
  label: string;
  unit: string;
}) {
  const points = Array.from({ length: hours + 1 }, (_, i) => ({
    time: `${i - hours}h`,
    value: +(value * (0.98 + 0.02 * Math.cos((hours - i) * 0.65))).toFixed(
      unit === '' && Number.isInteger(value) ? 0 : 2,
    ),
  }));
  return (
    <>
      <p className="ex-chart-legend">
        <span className="ex-live-dot" />
        {label} <small>{unit || '—'}</small>
      </p>
      <div className="ex-chart">
        <ResponsiveContainer width="100%" height="100%">
          <AreaChart
            data={points}
            margin={{ top: 15, right: 16, left: 0, bottom: 0 }}
          >
            <CartesianGrid stroke="var(--ex-line)" vertical={false} />
            <XAxis
              dataKey="time"
              tick={{ fill: 'var(--ex-muted)', fontSize: 10 }}
              minTickGap={30}
            />
            <YAxis
              width={45}
              tick={{ fill: 'var(--ex-muted)', fontSize: 10 }}
            />
            <Tooltip
              contentStyle={{
                background: 'var(--ex-surface)',
                borderColor: 'var(--ex-line)',
                color: 'var(--ex-ink)',
              }}
              formatter={(v: number) => [`${v}${unit}`, label]}
            />
            <Area
              type="monotone"
              dataKey="value"
              stroke="#2880ef"
              fill="#2880ef"
              fillOpacity={0.08}
              isAnimationActive={false}
            />
          </AreaChart>
        </ResponsiveContainer>
      </div>
    </>
  );
}
export default function Monitoring({
  resources,
  anchor,
}: {
  resources: Resource[];
  anchor: string;
}) {
  const { tr } = useI18n();
  const { category = 'overview' } = useParams();
  const definitions = monitoringData(resources);
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState('all');
  const [hours, setHours] = useState('24');
  const [selected, setSelected] = useState<Target | null>(null);
  const text = (v: Bilingual) => tr(v.zh, v.en);
  const page = monitorPages.find((p) => p[0] === category) ?? monitorPages[0];
  const definition = definitions[category as Kind];
  const clear = () => {
    setQuery('');
    setStatus('all');
  };
  return (
    <>
      <div className="ex-heading">
        <div>
          <p className="ex-eyebrow">UNIFIED OBSERVABILITY</p>
          <h1>{tr(page[1], page[2])}</h1>
          <p className="ex-muted">
            {tr(
              '统一检查基础设施、业务服务与运行事件。当前展示本地样本快照。',
              'Inspect infrastructure, services and operational events. Local sample snapshots are shown.',
            )}
          </p>
        </div>
        <Chip tone="info">{tr('数据快照', 'Data snapshot')}</Chip>
      </div>
      {category === 'overview' ? (
        <>
          <div className="ex-stats">
            {[
              [
                tr('监控对象', 'Targets'),
                Object.values(definitions).reduce(
                  (n, d) => n + d.targets.length,
                  0,
                ),
              ],
              [
                tr('关注对象', 'Needs attention'),
                Object.values(definitions).reduce(
                  (n, d) => n + d.targets.filter((t) => t.warning).length,
                  0,
                ),
              ],
              [tr('监控分类', 'Categories'), 6],
              [tr('日志记录', 'Log records'), 60],
            ].map(([name, value]) => (
              <Card className="ex-stat" key={name}>
                <span className="ex-stat-icon">
                  <Activity size={20} />
                </span>
                <p>{name}</p>
                <strong>{value}</strong>
                <small>{tr('当前时间窗口', 'Current time window')}</small>
              </Card>
            ))}
          </div>
          <div className="ex-three-col">
            {Object.entries(definitions).map(([key, d]) => {
              const name = monitorPages.find((p) => p[0] === key)!;
              return (
                <Card key={key}>
                  <div className="ex-card-title">
                    <h2>{tr(name[1], name[2])}</h2>
                    <Chip
                      tone={
                        d.targets.some((t) => t.warning) ? 'warning' : 'success'
                      }
                    >
                      {d.targets.length} {tr('项', 'targets')}
                    </Chip>
                  </div>
                  <p className="ex-muted ex-small">
                    {tr('参考对象', 'Reference target')}: {d.targets[0].name}
                  </p>
                  <Trend
                    value={d.targets[0].values[0]}
                    hours={24}
                    label={text(d.columns[0])}
                    unit={d.units[0]}
                  />
                  <Link className="ex-monitor-link" to={`/monitoring/${key}`}>
                    {tr('查看监控明细', 'View monitoring details')}{' '}
                    <ArrowRight size={13} />
                  </Link>
                </Card>
              );
            })}
          </div>
        </>
      ) : category === 'logs' ? (
        <LogExplorer anchor={anchor} />
      ) : category === 'rules' ? (
        <Rules />
      ) : definition ? (
        <>
          <div className="ex-filters">
            <Input
              type="search"
              aria-label={tr('搜索监控对象', 'Search monitoring targets')}
              placeholder={tr(
                '搜索名称、地址或业务域',
                'Search name, address or group',
              )}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            <Select
              label={tr('健康状态', 'Health status')}
              value={status}
              onValueChange={setStatus}
              options={[
                { value: 'all', label: tr('全部状态', 'All statuses') },
                { value: 'healthy', label: tr('运行正常', 'Healthy') },
                { value: 'warning', label: tr('需要关注', 'Attention') },
              ]}
            />
            <span className="ex-muted">
              {tr('数据时间', 'Snapshot time')}{' '}
              {new Date(anchor).toLocaleString()}
            </span>
          </div>
          <Card className="ex-table-card">
            <div className="ex-table-scroll">
              <table>
                <thead>
                  <tr>
                    {[
                      tr('监控对象', 'Target'),
                      tr('业务域 / 地址', 'Group / address'),
                      tr('状态', 'Status'),
                      ...definition.columns.map(text),
                      tr('操作', 'Action'),
                    ].map((h) => (
                      <th key={h}>{h}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {definition.targets
                    .filter(
                      (t) =>
                        `${t.name} ${t.address} ${t.group}`
                          .toLowerCase()
                          .includes(query.toLowerCase()) &&
                        (status === 'all' ||
                          t.warning === (status === 'warning')),
                    )
                    .map((t) => (
                      <tr key={t.id}>
                        <td className="ex-resource-name">
                          <Button variant="link" onClick={() => setSelected(t)}>
                            {t.name}
                          </Button>
                        </td>
                        <td className="ex-wide-cell">
                          <div>{t.group}</div>
                          <small className="ex-muted">{t.address}</small>
                        </td>
                        <td>
                          <Chip tone={t.warning ? 'warning' : 'success'}>
                            {t.warning
                              ? tr('需要关注', 'Attention')
                              : tr('运行正常', 'Healthy')}
                          </Chip>
                        </td>
                        {t.values.map((v, i) => (
                          <td key={i}>
                            {v}
                            {definition.units[i]}
                          </td>
                        ))}
                        <td>
                          <Button variant="link" onClick={() => setSelected(t)}>
                            {tr('查看详情', 'View details')}
                          </Button>
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>
            {!definition.targets.some(
              (t) =>
                `${t.name} ${t.address} ${t.group}`
                  .toLowerCase()
                  .includes(query.toLowerCase()) &&
                (status === 'all' || t.warning === (status === 'warning')),
            ) && (
              <EmptyState
                title={tr('没有匹配对象', 'No matching targets')}
                action={
                  <Button onClick={clear}>
                    {tr('清除筛选', 'Clear filters')}
                  </Button>
                }
              />
            )}
          </Card>
          <Dialog
            open={!!selected}
            onOpenChange={(open) => {
              if (!open) setSelected(null);
            }}
          >
            <DialogContent className="ex-monitor-dialog">
              <DialogTitle>{selected?.name}</DialogTitle>
              <DialogDescription>
                {tr(
                  '指标趋势与资源信息 · 本地样本快照',
                  'Metric trends and target details · Local sample snapshot',
                )}
              </DialogDescription>
              {selected && (
                <>
                  <dl className="ex-details">
                    <dt>{tr('地址', 'Address')}</dt>
                    <dd>{selected.address}</dd>
                    <dt>{tr('业务域', 'Business group')}</dt>
                    <dd>{selected.group}</dd>
                  </dl>
                  <Select
                    label={tr('时间范围', 'Time range')}
                    value={hours}
                    onValueChange={setHours}
                    options={['6', '12', '24'].map((h) => ({
                      value: h,
                      label: tr(`近 ${h} 小时`, `Last ${h} hours`),
                    }))}
                  />
                  <div className="ex-monitor-trends">
                    {definition.columns.map((c, i) => (
                      <div key={c.en}>
                        <Trend
                          value={selected.values[i]}
                          label={text(c)}
                          unit={definition.units[i]}
                          hours={Number(hours)}
                        />
                      </div>
                    ))}
                  </div>
                  {selected.resource && (
                    <Link to={`/inventory/${selected.resource}`}>
                      {tr('查看资源台账', 'View inventory')}
                    </Link>
                  )}
                </>
              )}
            </DialogContent>
          </Dialog>
        </>
      ) : (
        <EmptyState
          title={tr('监控分类不存在', 'Monitoring category not found')}
        />
      )}
    </>
  );
}
function LogExplorer({ anchor }: { anchor: string }) {
  const { tr } = useI18n();
  const [query, setQuery] = useState('');
  const [level, setLevel] = useState('all');
  const [selected, setSelected] = useState<number | null>(null);
  const logs = Array.from({ length: 60 }, (_, i) => ({
    id: i,
    time: new Date(Date.parse(anchor) - i * 47000).toLocaleString(),
    level: i % 13 === 0 ? 'ERROR' : i % 7 === 0 ? 'WARN' : 'INFO',
    service: [
      'order-service',
      'payment-service',
      'gateway',
      'mysql-primary',
      'k8s-controller',
    ][i % 5],
    message:
      i % 13 === 0
        ? tr(
            '上游请求超时，耗时 1250ms',
            'Upstream request timed out after 1250ms',
          )
        : i % 7 === 0
          ? tr(
              '连接池使用率超过 80%',
              'Connection pool utilization exceeds 80%',
            )
          : tr('请求处理完成，状态码 200', 'Request completed with status 200'),
  }));
  const items = logs.filter(
    (l) =>
      (level === 'all' || l.level === level) &&
      `${l.service} ${l.message}`.toLowerCase().includes(query.toLowerCase()),
  );
  return (
    <>
      <div className="ex-filters">
        <Input
          type="search"
          aria-label={tr('搜索日志', 'Search logs')}
          placeholder={tr('输入服务名称或关键字', 'Service name or keyword')}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <Select
          label={tr('日志级别', 'Log level')}
          value={level}
          onValueChange={setLevel}
          options={['all', 'ERROR', 'WARN', 'INFO'].map((v) => ({
            value: v,
            label: v === 'all' ? tr('全部级别', 'All levels') : v,
          }))}
        />
        <span>{tr(`共 ${items.length} 条`, `${items.length} records`)}</span>
      </div>
      <Card className="ex-table-card">
        <div className="ex-table-scroll">
          <table>
            <thead>
              <tr>
                {[
                  tr('时间', 'Time'),
                  tr('级别', 'Level'),
                  tr('服务', 'Service'),
                  tr('日志内容', 'Message'),
                  tr('操作', 'Action'),
                ].map((h) => (
                  <th key={h}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {items.map((l) => (
                <tr key={l.id}>
                  <td>{l.time}</td>
                  <td>
                    <Chip
                      tone={
                        l.level === 'ERROR'
                          ? 'danger'
                          : l.level === 'WARN'
                            ? 'warning'
                            : 'info'
                      }
                    >
                      {l.level}
                    </Chip>
                  </td>
                  <td>{l.service}</td>
                  <td className="ex-wide-cell">{l.message}</td>
                  <td>
                    <Button variant="link" onClick={() => setSelected(l.id)}>
                      {tr('查看详情', 'View details')}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!items.length && (
          <EmptyState
            title={tr('没有匹配日志', 'No matching logs')}
            action={
              <Button
                onClick={() => {
                  setQuery('');
                  setLevel('all');
                }}
              >
                {tr('清除筛选', 'Clear filters')}
              </Button>
            }
          />
        )}
      </Card>
      <Dialog
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) setSelected(null);
        }}
      >
        <DialogContent>
          <DialogTitle>{tr('日志详情', 'Log details')}</DialogTitle>
          <DialogDescription>
            {tr(
              '结构化事件与关联信息',
              'Structured event and correlation details',
            )}
          </DialogDescription>
          {selected !== null && (
            <dl className="ex-details">
              {Object.entries({
                ...logs[selected],
                trace_id: `trace-${String(selected + 1).padStart(8, '0')}`,
                source: 'local-snapshot',
              }).map(([key, value]) => (
                <div className="contents" key={key}>
                  <dt>{key}</dt>
                  <dd>{value}</dd>
                </div>
              ))}
            </dl>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
const ruleSeeds = [
  ['host-cpu', '主机 CPU 使用率', 'Host CPU usage', 'CPU >', 85, '%'],
  ['host-memory', '主机内存使用率', 'Host memory usage', 'Memory >', 90, '%'],
  ['disk', '磁盘容量', 'Disk capacity', 'Disk >', 80, '%'],
  [
    'api-latency',
    '接口响应时间',
    'Endpoint response time',
    'P95 >',
    1000,
    'ms',
  ],
  [
    'db-connections',
    '数据库连接池',
    'Database connection pool',
    'Connections >',
    80,
    '%',
  ],
  ['packet-loss', '网络丢包率', 'Network packet loss', 'Loss >', 1, '%'],
  ['pod-restarts', '容器重启次数', 'Container restarts', 'Restarts >', 5, ''],
  ['availability', '接口不可用', 'Endpoint unavailable', 'Failures >', 3, ''],
] as const;
function Rules() {
  const { tr } = useI18n();
  const [values, setValues] = useState(() => {
    try {
      const v = JSON.parse(
        localStorage.getItem('enterprise-ai.rules.v1') || 'null',
      );
      if (
        Array.isArray(v) &&
        v.length === 8 &&
        v.every((n) => Number.isFinite(n) && n > 0)
      )
        return v as number[];
    } catch {
      /* Use defaults when saved thresholds cannot be read. */
    }
    return ruleSeeds.map((r) => Number(r[4]));
  });
  const [editing, setEditing] = useState<number | null>(null);
  const [draft, setDraft] = useState('');
  const [error, setError] = useState(false);
  return (
    <>
      <p className="ex-muted ex-note">
        {tr(
          '规则用于本地阈值配置，不向采集端下发；当前快照不会重新计算告警。',
          'Thresholds are stored locally and are not sent to collectors. Existing snapshot incidents are not recalculated.',
        )}
      </p>
      {error && (
        <p role="alert">
          {tr(
            '保存失败，请检查浏览器存储后重试。',
            'Save failed. Check browser storage and retry.',
          )}
        </p>
      )}
      <Card className="ex-table-card">
        <div className="ex-table-scroll">
          <table>
            <thead>
              <tr>
                {[
                  tr('规则名称', 'Rule'),
                  tr('触发条件', 'Condition'),
                  tr('持续时间', 'Duration'),
                  tr('作用域', 'Scope'),
                  tr('操作', 'Action'),
                ].map((h) => (
                  <th key={h}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {ruleSeeds.map((r, i) => (
                <tr key={r[0]}>
                  <td>{tr(r[1], r[2])}</td>
                  <td>
                    {r[3]} {values[i]} {r[5]}
                  </td>
                  <td>5 min</td>
                  <td>{tr('全部业务域', 'All business groups')}</td>
                  <td>
                    <Button
                      variant="link"
                      onClick={() => {
                        setEditing(i);
                        setDraft(String(values[i]));
                      }}
                    >
                      {tr('编辑阈值', 'Edit threshold')}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
      <Dialog
        open={editing !== null}
        onOpenChange={(open) => {
          if (!open) setEditing(null);
        }}
      >
        <DialogContent>
          <DialogTitle>{tr('编辑阈值', 'Edit threshold')}</DialogTitle>
          <DialogDescription>
            {tr(
              '仅修改本浏览器中的阈值配置。',
              'Only changes the threshold in this browser.',
            )}
          </DialogDescription>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              if (editing === null) return;
              const next = [...values];
              next[editing] = Number(draft);
              try {
                localStorage.setItem(
                  'enterprise-ai.rules.v1',
                  JSON.stringify(next),
                );
                setValues(next);
                setError(false);
                setEditing(null);
              } catch {
                setError(true);
              }
            }}
          >
            <Label htmlFor="rule-value">{tr('阈值', 'Threshold')}</Label>
            <Input
              id="rule-value"
              type="number"
              min="0.01"
              max="1000000"
              step="0.01"
              required
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
            />
            <div className="ex-actions">
              <Button type="submit" variant="primary">
                {tr('保存规则', 'Save rule')}
              </Button>
              <Button onClick={() => setEditing(null)}>
                {tr('取消', 'Cancel')}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
