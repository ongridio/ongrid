import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { http, HttpResponse } from "msw";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ClustersPage, { DeviceClusterDetailPage } from "./Clusters";
import { server } from "@/test/msw-server";

const permissions = vi.hoisted(() => ({ isAdmin: true }));

vi.mock("@/store/me", () => ({
  usePermissions: () => ({
    isAdmin: permissions.isAdmin,
    canMutate: permissions.isAdmin,
    role: permissions.isAdmin ? "admin" : "user",
  }),
}));

const manualCluster = {
  id: 501,
  type: "cluster",
  name: "bare-metal-prod",
  props: { source: "manual", description: "Production hosts" },
  created_at: "2026-07-31T00:00:00Z",
  updated_at: "2026-07-31T01:00:00Z",
};

const kubernetesCluster = {
  id: 901,
  type: "cluster",
  name: "k8s-prod",
  props: { source: "kubernetes", k8s_cluster_id: 48, status: "degraded" },
  created_at: "2026-07-31T00:00:00Z",
  updated_at: "2026-07-31T01:00:00Z",
};

const devices = [
  {
    id: 19,
    node_id: 119,
    name: "bare-metal-1",
    hostname: "bm-1",
    ip_address: "10.0.0.19",
    os: "linux",
    arch: "amd64",
    online: true,
    last_seen_at: "2026-07-31T01:00:00Z",
  },
  {
    id: 20,
    node_id: 120,
    name: "bare-metal-2",
    hostname: "bm-2",
    ip_address: "10.0.0.20",
    os: "linux",
    arch: "amd64",
    online: false,
    last_seen_at: "2026-07-31T00:00:00Z",
  },
  {
    id: 17,
    node_id: 117,
    name: "k8s-worker",
    hostname: "worker-1",
    ip_address: "10.0.0.17",
    os: "linux",
    arch: "amd64",
    online: true,
    last_seen_at: "2026-07-31T01:00:00Z",
  },
];

const membership = {
  id: 701,
  src_id: 119,
  dst_id: 501,
  type: "member_of",
  props: { source: "edge_enrollment", profile_id: 31, device_id: 19 },
  created_at: "2026-07-31T01:00:00Z",
};

const activeProfile = {
  id: 31,
  name: "prod rollout",
  assignment_mode: "cluster",
  cluster_node_id: 501,
  expires_at: "2026-08-01T00:00:00Z",
  max_uses: 100,
  used_count: 1,
  status: "active",
  created_at: "2026-07-31T00:00:00Z",
};

describe("device cluster pages", () => {
  beforeEach(() => {
    localStorage.setItem("ongrid-locale", "zh-CN");
    permissions.isAdmin = true;
    installBaseHandlers();
  });

  it("lists both enrollment methods with member and enrollment health", async () => {
    render(
      <MemoryRouter>
        <ClustersPage />
      </MemoryRouter>,
    );

    const clusterLink = await screen.findByRole("link", {
      name: "bare-metal-prod",
    });
    expect(clusterLink).toHaveAttribute("href", "/clusters/501");
    expect(screen.getByRole("link", { name: "k8s-prod" })).toHaveAttribute("href", "/clusters/901");
    expect(screen.getByText("接入类型")).toBeInTheDocument();
    expect(screen.getByText("Host")).toBeInTheDocument();
    expect(screen.getByText("K8s")).toBeInTheDocument();
    expect(screen.getByText("降级")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "删除集群 k8s-prod" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "应用性能" })).not.toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: "安装批次" })).not.toBeInTheDocument();
    expect(screen.getByText("最近活动")).toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: "拓扑连接" })).not.toBeInTheDocument();
    expect(
      screen.getByText("2 个集群 · 1 台设备 · 1 台在线"),
    ).toBeInTheDocument();
  });

  it("opens Kubernetes lifecycle commands using the telemetry cluster ID without navigating", async () => {
    const user = userEvent.setup();
    const requested: string[] = [];
    server.use(http.get('/api/v1/k8s/clusters/:id', ({ params }) => {
      requested.push(String(params.id));
      return HttpResponse.json({ id: 48, name: 'k8s-prod', status: 'offline', mode: 'full-node' });
    }));
    render(<MemoryRouter><ClustersPage /></MemoryRouter>);
    await screen.findByRole('link', { name: 'k8s-prod' });
    for (const [button, title] of [
      ['查看集群 k8s-prod 的升级命令', '一键 Helm 升级'],
      ['查看集群 k8s-prod 的卸载命令', 'Helm 卸载命令'],
      ['删除集群 k8s-prod', '删除 Kubernetes 集群'],
    ]) {
      await user.click(screen.getByRole('button', { name: button }));
      expect(await screen.findByRole('dialog')).toHaveTextContent(title);
      await user.keyboard('{Escape}');
      await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    }
    expect(requested).toEqual(['48', '48', '48']);
    expect(screen.getByRole('link', { name: 'k8s-prod' })).toBeInTheDocument();
  });

  it("opens cluster detail when clicking anywhere on the row", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/clusters"]}>
        <Routes>
          <Route path="/clusters" element={<ClustersPage />} />
          <Route
            path="/clusters/:clusterId"
            element={<div>cluster detail target</div>}
          />
        </Routes>
      </MemoryRouter>,
    );

    const clusterLink = await screen.findByRole("link", {
      name: "bare-metal-prod",
    });
    const row = clusterLink.closest("tr") as HTMLTableRowElement;
    await user.click(within(row).getAllByRole("cell")[1]);

    expect(
      await screen.findByText("cluster detail target"),
    ).toBeInTheDocument();
  });

  it("deletes an empty cluster from the list action", async () => {
    const user = userEvent.setup();
    let deletedCluster = 0;
    const emptyCluster = {
      ...manualCluster,
      id: 502,
      name: "empty-cluster",
    };
    server.use(
      http.get("/api/v1/topology/nodes", () =>
        HttpResponse.json({ items: [emptyCluster], total: 1 }),
      ),
      http.get("/api/v1/topology/relations", () =>
        HttpResponse.json({ items: [], total: 0 }),
      ),
      http.get("/api/v1/edge-enrollment-profiles", () =>
        HttpResponse.json({ items: [], total: 0, page: 1, page_size: 100 }),
      ),
      http.delete("/api/v1/topology/nodes/:id", ({ params }) => {
        deletedCluster = Number(params.id);
        return new HttpResponse(null, { status: 204 });
      }),
    );

    render(
      <MemoryRouter>
        <ClustersPage />
      </MemoryRouter>,
    );

    await user.click(
      await screen.findByRole("button", { name: "删除集群 empty-cluster" }),
    );
    const dialog = screen.getByRole("dialog", { name: "删除设备集群" });
    await user.click(within(dialog).getByRole("button", { name: "删除集群" }));

    await waitFor(() => expect(deletedCluster).toBe(502));
    expect(screen.queryByText("empty-cluster")).not.toBeInTheDocument();
  });

  it("adds an eligible host while excluding Kubernetes-managed devices", async () => {
    const user = userEvent.setup();
    let createdRelation: Record<string, unknown> | null = null;
    server.use(
      http.post("/api/v1/topology/relations", async ({ request }) => {
        createdRelation = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({
          id: 702,
          ...createdRelation,
          created_at: "2026-07-31T02:00:00Z",
        });
      }),
    );

    renderDetail();
    await screen.findByRole("heading", { name: "成员设备" });
    await user.click(screen.getByRole("button", { name: "添加成员" }));

    const dialog = screen.getByRole("dialog", { name: "添加集群成员" });
    expect(within(dialog).getByText("bare-metal-2")).toBeInTheDocument();
    expect(within(dialog).queryByText("k8s-worker")).not.toBeInTheDocument();

    await user.click(within(dialog).getByText("bare-metal-2"));
    await user.click(
      within(dialog).getByRole("button", { name: "添加 1 台设备" }),
    );

    await waitFor(() =>
      expect(createdRelation).toEqual({
        src_id: 120,
        dst_id: 501,
        type: "member_of",
        props: { source: "manual", device_id: 20 },
      }),
    );
  });

  it("generates an enrollment profile locked to the current cluster", async () => {
    const user = userEvent.setup();
    let profileRequest: Record<string, unknown> | null = null;
    server.use(
      http.post("/api/v1/edge-enrollment-profiles", async ({ request }) => {
        profileRequest = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json({
          profile: { ...activeProfile, id: 32, name: "second rollout" },
          enrollment_token: "oen_once_only",
        });
      }),
    );

    renderDetail();
    await screen.findByRole("heading", { name: "成员设备" });
    await user.click(screen.getByRole("button", { name: "批量安装" }));

    const dialog = screen.getByRole("dialog", { name: "安装设备到集群" });
    const nameInput = within(dialog).getByLabelText("安装批次名称");
    await user.clear(nameInput);
    await user.type(nameInput, "second rollout");
    await user.click(
      within(dialog).getByRole("button", { name: "生成安装命令" }),
    );

    await waitFor(() =>
      expect(profileRequest).toEqual({
        name: "second rollout",
        assignment_mode: "cluster",
        cluster_node_id: 501,
        expires_in_hours: 24,
        max_uses: 100,
      }),
    );
    expect(
      await within(dialog).findByText(/oen_once_only/),
    ).toBeInTheDocument();
  });

  it("removes only the membership relation", async () => {
    const user = userEvent.setup();
    let deletedRelation = 0;
    server.use(
      http.delete("/api/v1/topology/relations/:id", ({ params }) => {
        deletedRelation = Number(params.id);
        return new HttpResponse(null, { status: 204 });
      }),
    );

    renderDetail();
    const memberLink = await screen.findByRole("link", {
      name: "bare-metal-1",
    });
    const row = memberLink.closest("tr") as HTMLTableRowElement;
    await user.click(within(row).getByRole("button", { name: "移除" }));

    await waitFor(() => expect(deletedRelation).toBe(701));
  });

  it("deletes an installation batch without deleting cluster members", async () => {
    const user = userEvent.setup();
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    let deletedProfile = 0;
    server.use(
      http.delete("/api/v1/edge-enrollment-profiles/:id", ({ params }) => {
        deletedProfile = Number(params.id);
        return new HttpResponse(null, { status: 204 });
      }),
    );

    try {
      renderDetail();
      await screen.findByRole("heading", { name: "成员设备" });
      await user.click(
        screen.getByRole("button", { name: "删除安装批次 prod rollout" }),
      );

      await user.click(await screen.findByRole("button", { name: "确认" }));
      await waitFor(() => expect(deletedProfile).toBe(31));
      expect(screen.queryByText("prod rollout")).not.toBeInTheDocument();
      expect(
        screen.getByRole("link", { name: "bare-metal-1" }),
      ).toBeInTheDocument();
    } finally {
      confirmSpy.mockRestore();
    }
  });

  it("preflights and triggers a cluster upgrade by package architecture", async () => {
    const user = userEvent.setup();
    let upgradeRequest: Record<string, unknown> | null = null;
    server.use(
      http.post("/api/v1/edge-upgrade-jobs", async ({ request }) => {
        upgradeRequest = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json(upgradeJob(), { status: 202 });
      }),
    );

    renderDetail();
    await screen.findByRole("heading", { name: "成员设备" });
    expect(screen.getByText("v0.10.1")).toBeInTheDocument();
    expect(screen.getByText("落后")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "批量升级" }));
    const dialog = screen.getByRole("dialog", {
      name: "批量升级 · bare-metal-prod",
    });
    expect(within(dialog).getAllByText("linux-amd64")).toHaveLength(2);
    expect(within(dialog).getByText("1")).toBeInTheDocument();

    await user.click(
      within(dialog).getByRole("button", { name: "升级 1 台设备" }),
    );

    await waitFor(() =>
      expect(upgradeRequest).toEqual({
        edge_ids: [9],
        target_version: "v0.10.2",
        cluster_node_id: 501,
        force_reinstall: false,
      }),
    );
    expect(
      await within(dialog).findByText("升级任务 #88 已创建"),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText(/现在可以关闭弹窗或离开页面，不会中断升级/),
    ).toBeInTheDocument();
    expect(within(dialog).getByText("1 × ≤10")).toBeInTheDocument();
  });

  it("skips same-version devices by default and requires an explicit force option", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/edges", () =>
        HttpResponse.json({
          items: [hostEdge({ agent_version: "0.10.2" })],
          total: 1,
        }),
      ),
    );

    renderDetail();
    await screen.findByRole("heading", { name: "成员设备" });
    expect(screen.queryByText("落后")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "批量升级" }));
    const dialog = screen.getByRole("dialog", {
      name: "批量升级 · bare-metal-prod",
    });

    expect(
      within(dialog).getByRole("button", { name: "升级 0 台设备" }),
    ).toBeDisabled();
    await user.click(
      within(dialog).getByRole("checkbox", {
        name: /强制重新安装同版本设备/,
      }),
    );
    expect(
      within(dialog).getByRole("button", { name: "升级 1 台设备" }),
    ).toBeEnabled();
  });

  it("shows persistent upgrade history and retries failed devices", async () => {
    const user = userEvent.setup();
    let attempts = 0;
    server.use(
      http.get("/api/v1/edge-upgrade-jobs", () =>
        HttpResponse.json({
          items: [failedUpgradeJob(attempts)],
          total: 1,
          page: 1,
          page_size: 20,
        }),
      ),
      http.get("/api/v1/edge-upgrade-jobs/:id", () =>
        HttpResponse.json({
          job: failedUpgradeJob(attempts),
          items: [failedUpgradeJobItem(attempts)],
        }),
      ),
      http.post("/api/v1/edge-upgrade-jobs/:id/retry", () => {
        attempts += 1;
        return HttpResponse.json(failedUpgradeJob(attempts), { status: 202 });
      }),
    );

    renderDetail();
    await screen.findByRole("heading", { name: "升级记录" });
    await user.click(screen.getByRole("button", { name: "查看" }));
    const dialog = await screen.findByRole("dialog", {
      name: "升级任务 #88",
    });

    expect(
      await within(dialog).findByText("edge disconnected"),
    ).toBeInTheDocument();
    expect(within(dialog).getByText("1/1")).toBeInTheDocument();
    expect(within(dialog).getByText(/第 1 批/)).toBeInTheDocument();
    await user.click(
      within(dialog).getByRole("button", { name: "重试 1 台失败设备" }),
    );

    await waitFor(() => expect(attempts).toBe(1));
    await waitFor(() =>
      expect(within(dialog).getByText("排队中")).toBeInTheDocument(),
    );
  });

  it("hides write actions from non-admin users", async () => {
    permissions.isAdmin = false;
    renderDetail();

    await screen.findByRole("heading", { name: "成员设备" });
    expect(
      screen.queryByRole("button", { name: "添加成员" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "批量安装" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "批量升级" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "移除" }),
    ).not.toBeInTheDocument();
  });

  it("blocks cluster deletion while members or active batches exist", async () => {
    renderDetail();

    await screen.findByRole("heading", { name: "成员设备" });
    const deleteButton = screen.getByRole("button", { name: "删除集群" });
    expect(deleteButton).toBeDisabled();
    await userEvent.hover(deleteButton.parentElement!);
    expect(await screen.findByRole("tooltip")).toHaveTextContent("请先移除全部成员，再删除集群。");
    expect(screen.queryByText("集群生命周期")).not.toBeInTheDocument();
  });
});

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={["/clusters/501"]}>
      <Routes>
        <Route
          path="/clusters/:clusterId"
          element={<DeviceClusterDetailPage />}
        />
      </Routes>
    </MemoryRouter>,
  );
}

function installBaseHandlers() {
  server.use(
    http.get("/api/v1/topology/nodes/:id", ({ params }) => {
      if (params.id === "501") return HttpResponse.json(manualCluster);
      return HttpResponse.json({ error: "not found" }, { status: 404 });
    }),
    http.get("/api/v1/topology/nodes", () =>
      HttpResponse.json({
        items: [manualCluster, kubernetesCluster],
        total: 2,
      }),
    ),
    http.get("/api/v1/devices", () =>
      HttpResponse.json({ items: devices, total: devices.length }),
    ),
    http.get("/api/v1/topology/relations", ({ request }) => {
      const url = new URL(request.url);
      if (url.searchParams.get("src_or_dst_id") === "501") {
        return HttpResponse.json({ items: [membership], total: 1 });
      }
      return HttpResponse.json({ items: [membership], total: 1 });
    }),
    http.get("/api/v1/edge-enrollment-profiles", () =>
      HttpResponse.json({
        items: [activeProfile],
        total: 1,
        page: 1,
        page_size: 100,
      }),
    ),
    http.get("/api/v1/edges", () =>
      HttpResponse.json({
        items: [
          hostEdge(),
          {
            id: 5,
            name: "k8s-worker",
            status: "online",
            roles: [],
            access_key_id: "ak-k8s",
            last_seen_at: "2026-07-31T01:00:00Z",
            device_id: 17,
          },
        ],
        total: 2,
      }),
    ),
    http.get("/api/v1/k8s/edge-attachments", () =>
      HttpResponse.json({
        items: [
          {
            edge_id: 5,
            cluster_id: 48,
            cluster_name: "k8s-prod",
            cluster_mode: "full-node",
            node_name: "worker-1",
            kind: "k8s-node",
          },
        ],
        total: 1,
      }),
    ),
    http.get("/api/v1/version", () =>
      HttpResponse.json({ manager_version: "v0.10.2" }),
    ),
    http.get("/api/v1/edge-bundles", () =>
      HttpResponse.json({
        manager_version: "v0.10.2",
        items: [
          {
            arch: "linux-amd64",
            version: "v0.10.2",
            available: true,
            bytes: 1024,
            sha256:
              "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
          },
          {
            arch: "linux-arm64",
            version: "v0.10.2",
            available: true,
            bytes: 1024,
            sha256:
              "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
          },
        ],
      }),
    ),
    http.get("/api/v1/edge-upgrade-jobs", () =>
      HttpResponse.json({ items: [], total: 0, page: 1, page_size: 20 }),
    ),
  );
}

function upgradeJob(overrides: Record<string, unknown> = {}) {
  return {
    id: 88,
    cluster_node_id: 501,
    target_version: "v0.10.2",
    status: "queued",
    force_reinstall: false,
    batch_size: 10,
    current_batch: 0,
    total_batches: 1,
    total: 1,
    succeeded: 0,
    failed: 0,
    skipped: 0,
    pending: 1,
    created_by: 1,
    created_at: "2026-07-31T02:00:00Z",
    updated_at: "2026-07-31T02:00:00Z",
    ...overrides,
  };
}

function failedUpgradeJob(attempts: number) {
  if (attempts > 0) return upgradeJob();
  return upgradeJob({
    status: "failed",
    current_batch: 1,
    failed: 1,
    pending: 0,
    finished_at: "2026-07-31T02:01:00Z",
    updated_at: "2026-07-31T02:01:00Z",
  });
}

function failedUpgradeJobItem(attempts: number) {
  return {
    id: 901,
    job_id: 88,
    edge_id: 9,
    device_id: 19,
    edge_name: "bare-metal-1",
    device_name: "bare-metal-1",
    arch: "linux-amd64",
    from_version: "v0.10.1",
    target_version: "v0.10.2",
    batch_number: 1,
    status: attempts > 0 ? "queued" : "failed",
    attempt: attempts + 1,
    error_code: attempts > 0 ? undefined : "edge_offline",
    error_message: attempts > 0 ? undefined : "edge disconnected",
    created_at: "2026-07-31T02:00:00Z",
    updated_at: "2026-07-31T02:01:00Z",
  };
}

function hostEdge(overrides: Record<string, unknown> = {}) {
  return {
    id: 9,
    name: "bare-metal-1",
    status: "online",
    roles: ["server"],
    access_key_id: "ak-host",
    last_seen_at: "2026-07-31T01:00:00Z",
    last_registered_at: "2026-07-31T00:50:00Z",
    device_id: 19,
    agent_version: "v0.10.1",
    ...overrides,
  };
}
