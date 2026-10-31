import { useState } from 'react';

import { PageHeader } from '@/components/ui/PageHeader';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/Tabs';
import { useI18n } from '@/i18n/locale';

import { ModelGroupsPanel } from '../components/preferences/ModelGroupsPanel';
import { SavedSearchesPanel } from '../components/preferences/SavedSearchesPanel';
import { TreeLevelsPanel } from '../components/preferences/TreeLevelsPanel';
import SubscriptionsPage from './Subscriptions';

// 偏好设置 — the one page for "how I see the CMDB".
//
// THE TABS ARE INDEPENDENT PANELS, not one form: a subscription is per user,
// a model group is shared configuration, a tree level is a per-user view, and
// a saved search belongs to the person who made it. One save button across
// them would silently apply the wrong ownership rule to half the page.

type Tab = 'models' | 'groups' | 'tree' | 'searches';

export default function PreferencesPage() {
  const { tr } = useI18n();
  const [tab, setTab] = useState<Tab>('models');

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('偏好设置', 'Preferences')}
        subtitle={tr(
          '选择模型与列、维护共享分组，以及管理你的树层级和保存搜索。',
          'Choose models and columns, maintain shared groups, and manage your tree levels and saved searches.',
        )}
      />

      <Tabs value={tab} onValueChange={(value) => setTab(value as Tab)} className="min-h-0 flex-1">
        <TabsList className="mx-6 mt-4">
          <TabsTrigger value="models">{tr('模型与列', 'Models & columns')}</TabsTrigger>
          <TabsTrigger value="groups">{tr('模型分组', 'Model groups')}</TabsTrigger>
          <TabsTrigger value="tree">{tr('树层级', 'Tree levels')}</TabsTrigger>
          <TabsTrigger value="searches">{tr('保存搜索', 'Saved searches')}</TabsTrigger>
        </TabsList>

        <TabsContent value="models" className="min-h-0 flex-1">
          <SubscriptionsPage embedded />
        </TabsContent>
        <TabsContent value="groups" className="min-h-0 flex-1 overflow-y-auto p-6">
          <ModelGroupsPanel />
        </TabsContent>
        <TabsContent value="tree" className="min-h-0 flex-1 overflow-y-auto p-6">
          <TreeLevelsPanel />
        </TabsContent>
        <TabsContent value="searches" className="min-h-0 flex-1 overflow-y-auto p-6">
          <SavedSearchesPanel />
        </TabsContent>
      </Tabs>
    </div>
  );
}
