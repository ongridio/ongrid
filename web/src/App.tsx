import { lazy, type ReactNode } from 'react';
import { Navigate, Route, Routes, useLocation } from 'react-router-dom';
import { Layout } from '@/components/Layout';
import { useAuth } from '@/store/auth';

const LoginPage = lazy(() => import('@/pages/Login'));
const HomePage = lazy(() => import('@/pages/Home'));
const ChatThreadPage = lazy(() => import('@/pages/ChatThread'));
const EdgesPage = lazy(() => import('@/pages/Edges'));
const EdgeDetailPage = lazy(() => import('@/pages/EdgeDetail'));
const DeviceShellPage = lazy(() => import('@/pages/DeviceShell'));
const DashboardPage = lazy(() => import('@/pages/Dashboard'));
const MonitorPage = lazy(() => import('@/pages/Monitor'));
const LogsPage = lazy(() => import('@/pages/Logs'));
const TracesPage = lazy(() => import('@/pages/Traces'));
const AlertsPage = lazy(() => import('@/pages/Alerts'));
const AlertRulesPage = lazy(() => import('@/pages/AlertRules'));
const IncidentDetailPage = lazy(() => import('@/pages/IncidentDetail'));
const ReportsPage = lazy(() => import('@/pages/Reports'));
const ReportDetailPage = lazy(() => import('@/pages/ReportDetail'));
const ReportSchedulesPage = lazy(() => import('@/pages/ReportSchedules'));
const SkillsPage = lazy(() => import('@/pages/Skills'));
const SkillRunPage = lazy(() => import('@/pages/SkillRun'));
const AgentsPage = lazy(() => import('@/pages/Agents'));
const FlowsPage = lazy(() => import('@/pages/Flows'));
const FlowEditorPage = lazy(() => import('@/pages/FlowEditor'));
const KnowledgePage = lazy(() => import('@/pages/Knowledge'));
const KnowledgeReposPage = lazy(() => import('@/pages/KnowledgeRepos'));
const TopologyPage = lazy(() => import('@/pages/Topology'));
const SettingsLayout = lazy(() => import('@/pages/SettingsLayout'));
const SettingsLLM = lazy(() => import('@/pages/settings/LLM'));
// Notifications = one-way alert delivery channels (Slack/Telegram/Larksuite
// /DingTalk/WeCom/Webhook); Channels = two-way IM bots (Slack/Telegram/
// Larksuite/DingTalk). The pages are deliberately paired: sender / receiver
// halves of the same comms surface, kept on adjacent /settings/* paths so
// admin nav stays predictable.
const SettingsNotifications = lazy(() => import('@/pages/settings/Notifications'));
const SettingsChannels = lazy(() => import('@/pages/settings/Channels'));
const SettingsIntegrations = lazy(() => import('@/pages/settings/Integrations'));
const SettingsMarketplace = lazy(() => import('@/pages/settings/Marketplace'));
const SettingsPreferences = lazy(() => import('@/pages/settings/Preferences'));
const SettingsHealth = lazy(() => import('@/pages/settings/Health'));
// Admin section (top-level "管理" tab) — platform governance pages.
// Lifted out of /settings so adding RBAC editor / audit log doesn't
// keep cluttering the settings rail. /settings now answers "how the
// platform behaves"; /admin answers "who uses it + what did they do".
const AdminLayout = lazy(() => import('@/pages/AdminLayout'));
const AdminUsers = lazy(() => import('@/pages/settings/Users'));
const AdminOrgs = lazy(() => import('@/pages/settings/Orgs'));
const AdminAuditLog = lazy(() => import('@/pages/settings/AuditLog'));
const AdminWebshell = lazy(() => import('@/pages/settings/Webshell'));

function RequireAuth({ children }: { children: ReactNode }) {
  const token = useAuth((s) => s.token);
  const location = useLocation();
  if (!token) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }
  return <>{children}</>;
}

function PublicOnly({ children }: { children: ReactNode }) {
  const token = useAuth((s) => s.token);
  if (token) return <Navigate to="/" replace />;
  return <>{children}</>;
}

export default function App() {
  return (
    <Routes>
      <Route
        path="/login"
        element={
          <PublicOnly>
            <LoginPage />
          </PublicOnly>
        }
      />
      <Route
        element={
          <RequireAuth>
            <Layout />
          </RequireAuth>
        }
      >
        <Route path="/" element={<HomePage />} />
        <Route path="/dashboard" element={<DashboardPage />} />
        <Route path="/chat/:sessionId" element={<ChatThreadPage />} />
        {/* /edges is the legacy route, kept as an alias to /devices for
            backward-compatible bookmarks; the canonical name post-split
            is /devices. */}
        <Route path="/edges" element={<Navigate to="/devices" replace />} />
        <Route path="/edges/:edgeId" element={<EdgeDetailPage />} />
        <Route path="/devices" element={<EdgesPage />} />
        <Route path="/devices/:edgeId" element={<EdgeDetailPage />} />
        {/* WebSSH: deviceId is the Prom-label device_id, not the edge.id.
            See DeviceShell.tsx for the rationale. */}
        <Route path="/devices/:deviceId/shell" element={<DeviceShellPage />} />
        <Route path="/monitor" element={<MonitorPage />} />
        <Route path="/logs" element={<LogsPage />} />
        <Route path="/traces" element={<TracesPage />} />
        <Route path="/alerts" element={<AlertsPage />} />
        <Route path="/alerts/rules" element={<AlertRulesPage />} />
        <Route path="/alerts/incidents/:id" element={<IncidentDetailPage />} />
        <Route path="/reports" element={<ReportsPage />} />
        <Route path="/reports/schedules" element={<ReportSchedulesPage />} />
        <Route path="/reports/:id" element={<ReportDetailPage />} />
        <Route path="/skills" element={<SkillsPage />} />
        <Route path="/skills/:key" element={<SkillRunPage />} />
        <Route path="/agents" element={<AgentsPage />} />
        <Route path="/workflows" element={<FlowsPage />} />
        <Route path="/workflows/:id" element={<FlowEditorPage />} />
        <Route path="/knowledge" element={<KnowledgePage />} />
        <Route path="/knowledge/repos" element={<KnowledgeReposPage />} />
        <Route path="/topology" element={<TopologyPage />} />
        {/* Old per-entity routes — folded into /topology with a type
            chip. Redirect (without query string) so bookmarks open the
            unified page; the operator picks the type chip themselves. */}
        <Route path="/services" element={<Navigate to="/topology" replace />} />
        <Route path="/clusters" element={<Navigate to="/topology" replace />} />
        <Route path="/apps" element={<Navigate to="/topology" replace />} />
        <Route path="/racks" element={<Navigate to="/topology" replace />} />
        {/* WebSSH 会话审计住在设备侧 — 不在 /admin（管理）里，因为
            它和具体设备紧耦合。AdminLayout 不再列这个 tab。 */}
        <Route path="/edges/shell-sessions" element={<AdminWebshell />} />
        <Route path="/admin/webshell" element={<Navigate to="/edges/shell-sessions" replace />} />
        <Route path="/webshell/sessions" element={<Navigate to="/edges/shell-sessions" replace />} />
        <Route path="/settings/webshell" element={<Navigate to="/edges/shell-sessions" replace />} />
        <Route path="/settings/users" element={<Navigate to="/admin/users" replace />} />
        <Route path="/settings/orgs" element={<Navigate to="/admin/orgs" replace />} />
        {/* 2026-05-30 naming sweep: the alert-delivery page is now
            "Notifications" (one-way), the IM-bot page is now "Channels"
            (two-way). URL ↔ label is uniform across nav + page title.
            Old paths (/communications, /bots, /communication) redirect
            here so any bookmark or deep-link survives. */}
        <Route path="/settings/communication" element={<Navigate to="/settings/notifications" replace />} />
        <Route path="/settings/communications" element={<Navigate to="/settings/notifications" replace />} />
        <Route path="/settings/bots" element={<Navigate to="/settings/channels" replace />} />
        <Route path="/settings/im-apps" element={<Navigate to="/settings/channels" replace />} />
        <Route path="/settings/advanced" element={<Navigate to="/settings/integrations" replace />} />
        <Route path="/settings/monitor" element={<Navigate to="/settings/integrations" replace />} />
        <Route path="/settings/general" element={<Navigate to="/settings/integrations" replace />} />
        <Route path="/settings" element={<SettingsLayout />}>
          <Route index element={<Navigate to="health" replace />} />
          <Route path="llm" element={<SettingsLLM />} />
          <Route path="notifications" element={<SettingsNotifications />} />
          <Route path="channels" element={<SettingsChannels />} />
          <Route path="integrations" element={<SettingsIntegrations />} />
          <Route path="health" element={<SettingsHealth />} />
          {/* /settings/marketplace retired (2026-05-19). Install surface
              is currently hidden from visible nav (no AIOps skill
              ecosystem yet); reachable via /skills?tab=install URL only.
              Redirect kept for any operator-bookmarked old URL. */}
          <Route path="marketplace" element={<Navigate to="/skills?tab=install" replace />} />
          <Route path="preferences" element={<SettingsPreferences />} />
        </Route>
        <Route path="/admin" element={<AdminLayout />}>
          <Route index element={<Navigate to="users" replace />} />
          <Route path="users" element={<AdminUsers />} />
          <Route path="orgs" element={<AdminOrgs />} />
          <Route path="audit" element={<AdminAuditLog />} />
        </Route>
        {/* Audit log lives under the Admin (Users & Orgs) section — it's
            platform governance ("who did what"), grouped with users/orgs,
            not in the always-visible sidebar footer. Old /audit links
            redirect so bookmarks keep working. */}
        <Route path="/audit" element={<Navigate to="/admin/audit" replace />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}
