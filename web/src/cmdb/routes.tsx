import { lazy, Suspense, type ReactNode } from 'react';
import { Navigate } from 'react-router-dom';
import { Boxes, Database, FileUp, GitBranch, History, Inbox, Layers3, ListTree, Network, Radar, Search, Server, Share2, Star, Waypoints } from 'lucide-react';

import type { IconType } from '@/lib/icon';
import { useI18n } from '@/i18n/locale';

import './styles.css';
import { ToastProvider } from './components/Toast';

// The CMDB module's single export surface into the host application.
//
// THIS FILE IS THE ISOLATION BOUNDARY. App.tsx, Sidebar.tsx and lib/routes.ts
// each learn about CMDB through one import here and nothing else, so adding a
// page, renaming a path or growing a submenu never touches an ongrid file. That
// is what keeps the CMDB rebaseable onto a newer upstream — the alternative is
// every new page editing a 750-line sidebar.
//
// PAGES ARE LAZY AND SUSPENSE IS OURS. The host mounts `element` verbatim, so the
// loading fallback for a CMDB page lives in the CMDB module rather than in the
// application's route table.

const RelationTypesPage = lazy(() => import('./pages/RelationTypes'));
const ModelsPage = lazy(() => import('./pages/Models'));
const ModelDetailPage = lazy(() => import('./pages/ModelDetail'));
const ModelRelationsPage = lazy(() => import('./pages/ModelRelations'));
const ServiceTreesPage = lazy(() => import('./pages/ServiceTrees'));
const PreferencesPage = lazy(() => import('./pages/Preferences'));
const SubscriptionsPage = lazy(() => import('./pages/Subscriptions'));
const InstancesPage = lazy(() => import('./pages/Instances'));
const BatchImportPage = lazy(() => import('./pages/BatchImport'));
const DiscoveryPage = lazy(() => import('./pages/Discovery'));
const DiscoveryPoolPage = lazy(() => import('./pages/DiscoveryPool'));
const OperationHistoryPage = lazy(() => import('./pages/OperationHistory'));
const TopologyViewsPage = lazy(() => import('./pages/TopologyViews'));
const SearchPage = lazy(() => import('./pages/Search'));
const HierarchyPage = lazy(() => import('./pages/Hierarchy'));
const TreeViewsPage = lazy(() => import('./pages/TreeViews'));
const ServiceTreePage = lazy(() => import('./pages/ServiceTree'));
const DCIMPage = lazy(() => import('./pages/DCIM'));
const IPAMPage = lazy(() => import('./pages/IPAM'));

/** The first page an operator lands on when they open the module. */
export const CMDB_HOME = '/cmdb/models';

function PageShell({ children }: { children: ReactNode }) {
  const { tr } = useI18n();
  return (
    <ToastProvider>
      <Suspense
        fallback={
          <div className="flex h-full items-center justify-center text-xs text-text-muted">
            {tr('加载中…', 'Loading…')}
          </div>
        }
      >
        {children}
      </Suspense>
    </ToastProvider>
  );
}

/** One route the host mounts. */
export type CMDBRoute = { path: string; element: ReactNode };

/**
 * Every CMDB route, in the shape the host's `<Routes>` takes.
 *
 * `element` IS ALREADY WRAPPED — the suspense boundary and the toast provider
 * travel with it, so a host that maps this array adds no CMDB-specific code.
 */
export const cmdbRoutes: CMDBRoute[] = [
  // Bare `/cmdb` opens the first page rather than a blank module root.
  { path: '/cmdb', element: <Navigate to={CMDB_HOME} replace /> },
  {
    path: '/cmdb/models',
    element: (
      <PageShell>
        <ModelsPage />
      </PageShell>
    ),
  },
  {
    // THE DETAIL ROUTE IS A CHILD PATH OF THE LIST, so a model's page keeps the
    // list one click away and a bookmarked model survives a reload.
    path: '/cmdb/models/:id',
    element: (
      <PageShell>
        <ModelDetailPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/model-relations',
    element: (
      <PageShell>
        <ModelRelationsPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/relations',
    element: (
      <PageShell>
        <ModelRelationsPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/relation-types',
    element: (
      <PageShell>
        <RelationTypesPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/service-trees',
    element: (
      <PageShell>
        <ServiceTreesPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/preferences',
    element: (
      <PageShell>
        <PreferencesPage />
      </PageShell>
    ),
  },
  // Kept for bookmarks; the menu now opens /cmdb/preferences.
  {
    path: '/cmdb/subscriptions',
    element: (
      <PageShell>
        <SubscriptionsPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/instances',
    element: (
      <PageShell>
        <InstancesPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/search',
    element: (
      <PageShell>
        <SearchPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/batch',
    element: (
      <PageShell>
        <BatchImportPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/discovery',
    element: (
      <PageShell>
        <DiscoveryPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/adc',
    element: (
      <PageShell>
        <DiscoveryPoolPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/operation-history',
    element: (
      <PageShell>
        <OperationHistoryPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/topology-views',
    element: (
      <PageShell>
        <TopologyViewsPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/hierarchy',
    element: (
      <PageShell>
        <HierarchyPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/tree-views',
    element: (
      <PageShell>
        <TreeViewsPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/tree-views/:typeId',
    element: (
      <PageShell>
        <TreeViewsPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/service-tree',
    element: (
      <PageShell>
        <ServiceTreePage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/dcim',
    element: (
      <PageShell>
        <DCIMPage />
      </PageShell>
    ),
  },
  {
    path: '/cmdb/ipam',
    element: (
      <PageShell>
        <IPAMPage />
      </PageShell>
    ),
  },
];

/** One sidebar entry. Structurally identical to the host's own nav item type. */
export type CMDBNavItem = {
  key: string;
  to: string;
  icon: IconType;
  label: string;
  /** A localized subgroup heading rendered inside the CMDB section. */
  group?: string;
};

/**
 * The module's sidebar section.
 *
 * IT IS A FUNCTION OF `tr` RATHER THAN A CONSTANT because the host's sidebar
 * resolves labels through the live locale; a module-level constant would freeze
 * the language at import time.
 */
export function cmdbNavSection(tr: (zh: string, en: string) => string): {
  storageKey: string;
  title: string;
  defaultOpen: boolean;
  items: CMDBNavItem[];
} {
  return {
    storageKey: 'cmdb',
    title: 'CMDB',
    // COLLAPSED BY DEFAULT, like the other secondary sections: an operator who
    // works in CMDB every day opens it once and the choice persists.
    defaultOpen: false,
    items: [
      { key: 'models', to: '/cmdb/models', icon: Boxes, label: tr('模型配置', 'Model configuration'), group: tr('模型管理', 'Models') },
      { key: 'relations', to: '/cmdb/relations', icon: Share2, label: tr('关系', 'Relations'), group: tr('模型管理', 'Models') },
      { key: 'relation-types', to: '/cmdb/relation-types', icon: GitBranch, label: tr('关系类型', 'Relation types'), group: tr('模型管理', 'Models') },
      { key: 'service-trees', to: '/cmdb/service-trees', icon: Layers3, label: tr('服务树定义', 'Tree definitions'), group: tr('模型管理', 'Models') },
      { key: 'preferences', to: '/cmdb/preferences', icon: Star, label: tr('偏好设置', 'Preferences'), group: tr('模型管理', 'Models') },
      { key: 'instances', to: '/cmdb/instances', icon: Database, label: tr('资源数据', 'Resources'), group: tr('资源管理', 'Resources') },
      { key: 'batch', to: '/cmdb/batch', icon: FileUp, label: tr('批量导入', 'Batch import'), group: tr('资源管理', 'Resources') },
      { key: 'search', to: '/cmdb/search', icon: Search, label: tr('资源搜索', 'Resource search'), group: tr('资源管理', 'Resources') },
      { key: 'tree-views', to: '/cmdb/tree-views', icon: ListTree, label: tr('资源层级', 'Hierarchy'), group: tr('资源管理', 'Resources') },
      { key: 'relation-views', to: '/cmdb/service-tree', icon: Network, label: tr('关系视图', 'Relation views'), group: tr('资源管理', 'Resources') },
      { key: 'discovery', to: '/cmdb/discovery', icon: Radar, label: tr('自动发现', 'Auto discovery'), group: tr('资源管理', 'Resources') },
      { key: 'adc', to: '/cmdb/adc', icon: Inbox, label: tr('自动发现池', 'Discovery pool'), group: tr('资源管理', 'Resources') },
      { key: 'operation-history', to: '/cmdb/operation-history', icon: History, label: tr('操作历史', 'Operation history'), group: tr('资源管理', 'Resources') },
      { key: 'topology-views', to: '/cmdb/topology-views', icon: Waypoints, label: tr('拓扑视图', 'Topology views'), group: tr('资源管理', 'Resources') },
      { key: 'dcim', to: '/cmdb/dcim', icon: Server, label: tr('数据中心', 'Data centre'), group: tr('资源管理', 'Resources') },
      { key: 'ipam', to: '/cmdb/ipam', icon: Network, label: tr('IPAM', 'IPAM'), group: tr('资源管理', 'Resources') },
    ],
  };
}

/** Command-palette entries. Structurally identical to the host's route def. */
export const cmdbPaletteRoutes: Array<{
  path: string;
  zh: string;
  en: string;
  keywords?: string[];
  group: 'CMDB';
}> = [
  {
    path: '/cmdb/models',
    zh: 'CMDB / 模型配置',
    en: 'CMDB / Model configuration',
    keywords: ['cmdb', 'model', 'ci type', 'moxing', 'peizhi'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/relations',
    zh: 'CMDB / 关系',
    en: 'CMDB / Relations',
    keywords: ['cmdb', 'model relation', 'guanxi'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/relation-types',
    zh: 'CMDB / 关系类型',
    en: 'CMDB / Relation types',
    keywords: ['cmdb', 'relation', 'type', 'guanxi', 'leixing'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/service-trees',
    zh: 'CMDB / 服务树定义',
    en: 'CMDB / Tree definitions',
    keywords: ['cmdb', 'service tree', 'shu', 'dingyi'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/preferences',
    zh: 'CMDB / 偏好设置',
    en: 'CMDB / Preferences',
    keywords: ['cmdb', 'preference', 'subscription', 'columns', 'pianhao', 'dingyue'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/instances',
    zh: 'CMDB / 资源数据',
    en: 'CMDB / Resources',
    keywords: ['cmdb', 'instance', 'resource', 'ziyuan', 'shuju'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/search',
    zh: 'CMDB / 资源搜索',
    en: 'CMDB / Resource search',
    keywords: ['cmdb', 'search', 'query', 'sousuo'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/batch',
    zh: 'CMDB / 批量导入',
    en: 'CMDB / Batch import',
    keywords: ['cmdb', 'batch', 'import', 'csv', 'xlsx', 'daoru'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/tree-views',
    zh: 'CMDB / 资源层级',
    en: 'CMDB / Hierarchy',
    keywords: ['cmdb', 'hierarchy', 'tree', 'cengji'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/service-tree',
    zh: 'CMDB / 关系视图',
    en: 'CMDB / Relation views',
    keywords: ['cmdb', 'service tree', 'relation view', 'guanxi', 'fuwushu'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/discovery',
    zh: 'CMDB / 自动发现',
    en: 'CMDB / Auto discovery',
    keywords: ['cmdb', 'discovery', 'agent', 'provider', 'zidongfaxian', 'caiji'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/adc',
    zh: 'CMDB / 自动发现池',
    en: 'CMDB / Discovery pool',
    keywords: ['cmdb', 'adc', 'discovery', 'pool', 'accept', 'faxianchi'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/operation-history',
    zh: 'CMDB / 操作历史',
    en: 'CMDB / Operation history',
    keywords: ['cmdb', 'history', 'audit', 'change', 'caozuolishi'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/topology-views',
    zh: 'CMDB / 拓扑视图',
    en: 'CMDB / Topology views',
    keywords: ['cmdb', 'topology', 'graph', 'view', 'tuopu'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/dcim',
    zh: 'CMDB / 数据中心',
    en: 'CMDB / Data centre',
    keywords: ['cmdb', 'dcim', 'rack', '机柜', 'shujuzhongxin'],
    group: 'CMDB',
  },
  {
    path: '/cmdb/ipam',
    zh: 'CMDB / IPAM',
    en: 'CMDB / IP address space',
    keywords: ['cmdb', 'ipam', 'subnet', 'address', 'cidr', 'ip', 'wangduan'],
    group: 'CMDB',
  },
];
