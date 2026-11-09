import { useState } from 'react';

import { PageHeader } from '@/components/ui/PageHeader';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/Tabs';
import { useI18n } from '@/i18n/locale';

import { CatalogPanel } from '../components/discovery/CatalogPanel';
import { RulesPanel } from '../components/discovery/RulesPanel';
import { TargetsPanel } from '../components/discovery/TargetsPanel';
import { TemplatesPanel } from '../components/discovery/TemplatesPanel';

// 自动发现 — the module's configuration surface.
//
// THE FOUR TABS ARE THE FOUR QUESTIONS, in the order an operator answers them:
// what can be collected (Rules), where it lands (Targets), what the providers
// report (Catalog), and how a deployment's definitions move in and out
// (Templates).
//
// IT DOES NOT START A COLLECTION. The collector is an agent that pulls its
// targets; the only run-shaped action, a target's test, is answered 501 and the
// page shows that rather than pretending otherwise.

type Tab = 'rules' | 'targets' | 'catalog' | 'templates';

export default function DiscoveryPage() {
  const { tr } = useI18n();
  const [tab, setTab] = useState<Tab>('rules');

  return (
    <div className="flex h-full min-h-0 w-full min-w-0 flex-col">
      <PageHeader
        title={tr('自动发现', 'Auto discovery')}
        subtitle={tr(
          '维护发现规则、把它们绑定到模型，并查看内置提供方的字段目录。采集由 agent 拉取执行。',
          'Maintain discovery rules, bind them to models, and browse what the built-in providers report. Collection is an agent’s pull.',
        )}
      />

      <Tabs value={tab} onValueChange={(value) => setTab(value as Tab)} className="min-h-0 flex-1">
        <TabsList className="mx-6 mt-4">
          <TabsTrigger value="rules">{tr('规则', 'Rules')}</TabsTrigger>
          <TabsTrigger value="targets">{tr('目标', 'Targets')}</TabsTrigger>
          <TabsTrigger value="catalog">{tr('目录', 'Catalog')}</TabsTrigger>
          <TabsTrigger value="templates">{tr('模板', 'Templates')}</TabsTrigger>
        </TabsList>

        <TabsContent value="rules" className="min-h-0 flex-1 overflow-y-auto p-6">
          <RulesPanel />
        </TabsContent>
        <TabsContent value="targets" className="min-h-0 flex-1 overflow-y-auto p-6">
          <TargetsPanel />
        </TabsContent>
        <TabsContent value="catalog" className="min-h-0 flex-1 overflow-y-auto p-6">
          <CatalogPanel />
        </TabsContent>
        <TabsContent value="templates" className="min-h-0 flex-1 overflow-y-auto p-6">
          <TemplatesPanel />
        </TabsContent>
      </Tabs>
    </div>
  );
}
