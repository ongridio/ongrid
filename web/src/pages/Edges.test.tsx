import { selectOption } from '@/test/select-option';
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { http, HttpResponse } from "msw";
import { beforeEach, describe, expect, it, vi } from "vitest";

import EdgesPage from "./Edges";
import { server } from "@/test/msw-server";
import { openMetricDrilldown } from "@/lib/drilldown";

vi.mock("@/lib/drilldown", () => ({ openMetricDrilldown: vi.fn().mockResolvedValue(undefined) }));

const permissions = vi.hoisted(() => ({ isAdmin: true, canMutate: true, role: "admin" }));
vi.mock("@/store/me", () => ({ usePermissions: () => permissions }));

describe("EdgesPage", () => {
  beforeEach(() => {
    Object.assign(permissions, { isAdmin: true, canMutate: true, role: "admin" });
    localStorage.setItem("ongrid-locale", "zh-CN");
    server.use(
      http.get("/api/v1/version", () =>
        HttpResponse.json({ manager_version: "dev" }),
      ),
      http.get("/api/v1/edges", () =>
        HttpResponse.json({
          items: [
            {
              id: 3,
              name: "kind-controller",
              status: "online",
              roles: [],
              access_key_id: "ak-controller",
              last_seen_at: "2026-06-29T10:00:00Z",
              host_info: { hostname: "controller-pod", ip_address: "10.0.0.3" },
              device_id: 3,
              agent_version: "dev",
            },
            {
              id: 5,
              name: "k8s:kind-local:ongrid-k8s-control-plane",
              status: "online",
              roles: [],
              access_key_id: "ak-node",
              last_seen_at: "2026-06-29T10:00:00Z",
              host_info: {
                hostname: "ongrid-k8s-control-plane",
                ip_address: "10.0.0.5",
              },
              device_id: 17,
              agent_version: "dev",
            },
            {
              id: 9,
              name: "bare-metal-1",
              status: "online",
              roles: ["server"],
              access_key_id: "ak-host",
              last_seen_at: "2026-06-29T10:00:00Z",
              host_info: { hostname: "bm-1", ip_address: "10.0.0.9" },
              device_id: 19,
              agent_version: "dev",
            },
          ],
          total: 3,
        }),
      ),
      http.get("/api/v1/devices", () =>
        HttpResponse.json({
          items: [
            {
              id: 3,
              name: "kind-controller",
              hostname: "controller-pod",
              ip_address: "10.0.0.3",
              roles: [],
              online: true,
              last_seen_at: "2026-06-29T10:00:00Z",
            },
            {
              id: 17,
              name: "k8s:kind-local:ongrid-k8s-control-plane",
              hostname: "ongrid-k8s-control-plane",
              ip_address: "10.0.0.5",
              roles: [],
              online: true,
              last_seen_at: "2026-06-29T10:00:00Z",
            },
            {
              id: 19,
              name: "bare-metal-1",
              hostname: "bm-1",
              ip_address: "10.0.0.9",
              node_id: 119,
              roles: ["server"],
              online: true,
              last_seen_at: "2026-06-29T10:00:00Z",
            },
          ],
          total: 3,
        }),
      ),
      http.get("/api/v1/topology/nodes", () =>
        HttpResponse.json({
          items: [
            {
              id: 501,
              type: "cluster",
              name: "bare-metal-prod",
              props: { source: "manual" },
              created_at: "2026-07-31T00:00:00Z",
              updated_at: "2026-07-31T00:00:00Z",
            },
          ],
          total: 1,
        }),
      ),
      http.get("/api/v1/topology/relations", () =>
        HttpResponse.json({
          items: [
            {
              id: 701,
              src_id: 119,
              dst_id: 501,
              type: "member_of",
              props: { source: "edge_enrollment", profile_id: 1 },
              created_at: "2026-07-31T00:00:00Z",
            },
          ],
          total: 1,
        }),
      ),
      http.get("/api/v1/k8s/edge-attachments", () =>
        HttpResponse.json({
          items: [
            {
              edge_id: 3,
              cluster_id: 1,
              cluster_name: "kind-local",
              cluster_mode: "full-node",
              node_name: "ongrid-k8s-control-plane",
              kind: "k8s-controller",
            },
            {
              edge_id: 5,
              cluster_id: 1,
              cluster_name: "kind-local",
              cluster_mode: "full-node",
              node_name: "ongrid-k8s-control-plane",
              kind: "k8s-node",
            },
            {
              edge_id: 5,
              cluster_id: 1,
              cluster_name: "kind-local",
              cluster_mode: "full-node",
              node_name: "ongrid-k8s-control-plane",
              kind: "k8s-controller-runtime",
            },
          ],
          total: 3,
        }),
      ),
    );
  });

  it("在非 Kubernetes 设备行展示所属拓扑集群", async () => {
    render(
      <MemoryRouter>
        <EdgesPage />
      </MemoryRouter>,
    );

    const deviceName = await screen.findByText("bare-metal-1");
    const row = deviceName.closest("tr") as HTMLTableRowElement;
    expect(row).not.toBeNull();
    const clusterLink = within(row).getByRole("link", {
      name: "所属集群 bare-metal-prod",
    });
    expect(clusterLink).toHaveAttribute("href", "/clusters/501");
    expect(
      within(clusterLink).getByText("集群 · bare-metal-prod"),
    ).toBeInTheDocument();
  });

  it("隐藏 Controller Edge，并为设备展示统一的 K8s 和集群标签", async () => {
    render(
      <MemoryRouter>
        <EdgesPage />
      </MemoryRouter>,
    );

    const k8sNameCells = await screen.findAllByText("ongrid-k8s-control-plane");
    expect(k8sNameCells).toHaveLength(1);
    expect(
      screen.getByText("k8s:kind-local:ongrid-k8s-control-plane"),
    ).toBeInTheDocument();
    expect(screen.queryByText("kind-controller")).not.toBeInTheDocument();
    expect(screen.queryByText("K8s Node")).not.toBeInTheDocument();
    expect(screen.queryByText("K8s Controller")).not.toBeInTheDocument();
    const k8sRow = k8sNameCells[0].closest("tr");
    expect(k8sRow).not.toBeNull();
    expect(
      within(k8sRow as HTMLTableRowElement).getByText("K8s"),
    ).toBeInTheDocument();
    const clusterLink = within(k8sRow as HTMLTableRowElement).getByRole(
      "link",
      { name: "所属集群 kind-local" },
    );
    expect(clusterLink).toHaveAttribute("href", "/kubernetes/1");
    const clusterChip = within(clusterLink)
      .getByText("集群 · kind-local")
      .closest('[data-slot="badge"]');
    expect(clusterChip).toHaveAttribute("data-tone", "info");
    expect(
      within(k8sRow as HTMLTableRowElement).queryByText("Kubernetes 管理"),
    ).not.toBeInTheDocument();
    const terminalLink = within(k8sRow as HTMLTableRowElement).getByRole(
      "link",
      { name: /打开.*终端/ },
    );
    expect(terminalLink).toHaveAttribute("href", "/devices/17/shell");
    expect(terminalLink).toHaveAttribute("target", "_blank");
    expect(
      within(k8sRow as HTMLTableRowElement).getByText("查看图表"),
    ).toBeInTheDocument();
    expect(
      within(k8sRow as HTMLTableRowElement).queryByLabelText(/选择/),
    ).not.toBeInTheDocument();
    expect(screen.getByText("bare-metal-1")).toBeInTheDocument();
    expect(screen.getByText("bm-1")).toBeInTheDocument();
    expect(screen.queryByText("Host Edge")).not.toBeInTheDocument();
    expect(screen.getByText("Host")).toHaveClass(
      "bg-indigo-500/10",
      "text-indigo-300",
    );
    expect(screen.getByRole("table")).toHaveClass("min-w-full", "w-max", "table-auto");
  });

  it("点击 K8s 托管设备行进入设备详情，操作列支持 WebSSH 和修改名称", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/devices"]}>
        <EdgesPage />
        <LocationProbe />
      </MemoryRouter>,
    );

    const k8sNameCells = await screen.findAllByText("ongrid-k8s-control-plane");
    const k8sRow = k8sNameCells[0].closest("tr") as HTMLTableRowElement;
    expect(k8sRow).not.toBeNull();


      await user.click(k8sRow);

    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent("/devices/17"),
    );

    expect(
      within(k8sRow).queryByText("Kubernetes 管理"),
    ).not.toBeInTheDocument();
    expect(
      within(k8sRow).getByRole("link", { name: /打开.*终端/ }),
    ).toHaveAttribute("href", "/devices/17/shell");
    expect(within(k8sRow).getByText("查看图表")).toBeInTheDocument();
    expect(
      within(k8sRow).getByRole("button", { name: "操作" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent("/devices/17"),
    );
  });

  it.each([
    [19, "bare-metal-1", "bm-1"],
    [17, "k8s:kind-local:ongrid-k8s-control-plane", "ongrid-k8s-control-plane"],
  ])("修改设备 %s 名称，保存后优先展示名称并保留主机名", async (id, oldName, hostname) => {
    const user = userEvent.setup();
    let submitted: unknown;
    server.use(http.patch(`/api/v1/devices/${id}`, async ({ request }) => {
      submitted = await request.json();
      return new HttpResponse(null, { status: 204 });
    }));
    render(<MemoryRouter><EdgesPage /></MemoryRouter>);
    const row = (await screen.findByText(oldName)).closest("tr")!;
     await user.click(within(row).getByRole("button", { name: "操作" }));
    await user.click(await screen.findByRole('menuitem', { name: "修改设备名称" }));
    const dialog = screen.getByRole("dialog", { name: "修改设备名称" });
    const input = within(dialog).getByLabelText("设备名称");
    expect(input).toHaveValue(oldName);
    await user.clear(input);
    await user.type(input, "   ");
    expect(within(dialog).getByRole("button", { name: "保存" })).toBeDisabled();
    await user.clear(input);
    await user.type(input, "  数据库主机  ");
    await user.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(submitted).toEqual({ name: "数据库主机" });
    expect(within(row).getByText("数据库主机")).toBeInTheDocument();
    expect(within(row).getByText(hostname)).toBeInTheDocument();
  });

  it("K8s 设备查看图表使用设备 ID", async () => {
    const user = userEvent.setup();
    render(<MemoryRouter><EdgesPage /></MemoryRouter>);
    await user.click(await screen.findByRole("button", { name: "在 Grafana 查看 k8s:kind-local:ongrid-k8s-control-plane 图表" }));
    expect(openMetricDrilldown).toHaveBeenCalledWith(expect.objectContaining({
      deviceId: 17,
      expr: expect.stringContaining('device_id="17"'),
    }));
  });

  it("只读用户不能修改设备名称", async () => {
    Object.assign(permissions, { isAdmin: false, canMutate: false, role: "viewer" });
    const user = userEvent.setup();
    render(<MemoryRouter><EdgesPage /></MemoryRouter>);
    const row = (await screen.findByText("bare-metal-1")).closest("tr")!;
     await user.click(within(row).getByRole("button", { name: "操作" }));
    expect(screen.queryByRole('menuitem', { name: "修改设备名称" })).not.toBeInTheDocument();
  });

  it("名称保存失败时保留输入，允许重试", async () => {
    const user = userEvent.setup();
    server.use(http.patch("/api/v1/devices/19", () =>
      HttpResponse.json({ message: "保存失败" }, { status: 500 }),
    ));
    render(<MemoryRouter><EdgesPage /></MemoryRouter>);
    const row = (await screen.findByText("bare-metal-1")).closest("tr")!;
     await user.click(within(row).getByRole("button", { name: "操作" }));
    await user.click(await screen.findByRole('menuitem', { name: "修改设备名称" }));
    const input = screen.getByLabelText("设备名称");
    await user.clear(input);
    await user.type(input, "新名称");
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(input).toHaveValue("新名称");
    expect(within(row).getByText("bare-metal-1")).toBeInTheDocument();
    server.use(http.patch("/api/v1/devices/19", () => new HttpResponse(null, { status: 204 })));
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(within(row).getByText("新名称")).toBeInTheDocument();
  });

  it("设备操作菜单支持键盘打开和 Escape 返回触发按钮", async () => {
    const user = userEvent.setup();
    render(<MemoryRouter><EdgesPage /></MemoryRouter>);
    const row = (await screen.findByText("bare-metal-1")).closest("tr")!;
    const trigger = within(row).getByRole("button", { name: "操作" });
    trigger.focus();
    await user.keyboard("{ArrowDown}");
    expect(await screen.findByRole("menu")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();
  });

  it("整包升级创建可按设备架构执行和验证的持久任务", async () => {
    const user = userEvent.setup();
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    let submitted: Record<string, unknown> | null = null;
    server.use(
      http.get("/api/v1/version", () =>
        HttpResponse.json({ manager_version: "v0.11.1" }),
      ),
      http.post("/api/v1/edge-upgrade-jobs", async ({ request }) => {
        submitted = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json(
          {
            id: 77,
            target_version: "v0.11.1",
            status: "queued",
            total: 1,
            pending: 1,
          },
          { status: 202 },
        );
      }),
    );

    try {
      render(
        <MemoryRouter>
          <EdgesPage />
        </MemoryRouter>,
      );
      await screen.findByText("bare-metal-1");

        await user.click(within(screen.getByText("bare-metal-1").closest("tr")!).getByRole("button", { name: "操作" }));


        await user.click(
          await screen.findByRole('menuitem', {
            name: "升级整包（Edge + 插件）",
          }),
        );


      await user.click(await screen.findByRole("button", { name: "确认" }));
      await waitFor(() =>
        expect(submitted).toEqual({
          edge_ids: [9],
          target_version: "v0.11.1",
        }),
      );
      expect(
        await screen.findByText(/升级任务 #77 已创建/),
      ).toBeInTheDocument();
    } finally {
      confirmSpy.mockRestore();
    }
  });

  it("生成可绑定集群的批量安装命令", async () => {
    const user = userEvent.setup();
    let submitted: Record<string, unknown> | null = null;
    server.use(
      http.get("/api/v1/topology/nodes", () =>
        HttpResponse.json({
          items: [
            {
              id: 88,
              type: "cluster",
              name: "bare-metal-prod",
              props: {},
              created_at: "2026-07-31T00:00:00Z",
              updated_at: "2026-07-31T00:00:00Z",
            },
          ],
          total: 1,
        }),
      ),
      http.get("/api/v1/edge-enrollment-profiles", () =>
        HttpResponse.json({ items: [], total: 0, page: 1, page_size: 100 }),
      ),
      http.post("/api/v1/edge-enrollment-profiles", async ({ request }) => {
        submitted = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json(
          {
            profile: {
              id: 7,
              name: "机房批次",
              assignment_mode: "cluster",
              cluster_node_id: 88,
              expires_at: "2026-08-01T00:00:00Z",
              max_uses: 100,
              used_count: 0,
              status: "active",
              created_at: "2026-07-31T00:00:00Z",
            },
            enrollment_token: "oen_reusable_token",
          },
          { status: 201 },
        );
      }),
    );
    render(
      <MemoryRouter>
        <EdgesPage />
      </MemoryRouter>,
    );

    await screen.findByText("bare-metal-1");

      await user.click(screen.getByRole("button", { name: "批量安装设备" }));

    const dialog = await screen.findByRole("dialog", { name: "批量安装 Edge" });
    await within(dialog).findByText("暂无安装批次");

      await user.type(
        within(dialog).getByLabelText("安装批次名称"),
        "机房批次",
      );
      await selectOption(within(dialog).getByLabelText("归属方式"), "关联拓扑集群");

    await waitFor(() =>
      expect(within(dialog).getByLabelText("集群")).toBeInTheDocument(),
    );

      await selectOption(within(dialog).getByLabelText("集群"), "bare-metal-prod");
      await user.click(
        within(dialog).getByRole("button", { name: "生成安装命令" }),
      );


    await waitFor(() =>
      expect(submitted).toEqual({
        name: "机房批次",
        assignment_mode: "cluster",
        cluster_node_id: 88,
        expires_in_hours: 24,
        max_uses: 100,
      }),
    );
    expect(
      await within(dialog).findByText(/--enrollment-token=oen_reusable_token/),
    ).toBeInTheDocument();
    expect(within(dialog).getAllByText(/--tls-insecure/)).toHaveLength(2);
  });

  it("直接删除已有安装批次", async () => {
    const user = userEvent.setup();
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    let deletedProfile = 0;
    server.use(
      http.get("/api/v1/topology/nodes", () =>
        HttpResponse.json({ items: [], total: 0 }),
      ),
      http.get("/api/v1/edge-enrollment-profiles", () =>
        HttpResponse.json({
          items: [
            {
              id: 7,
              name: "待删除批次",
              assignment_mode: "batch_only",
              expires_at: "2026-08-01T00:00:00Z",
              max_uses: 100,
              used_count: 0,
              status: "active",
              created_at: "2026-07-31T00:00:00Z",
            },
          ],
          total: 1,
          page: 1,
          page_size: 100,
        }),
      ),
      http.delete("/api/v1/edge-enrollment-profiles/:id", ({ params }) => {
        deletedProfile = Number(params.id);
        return new HttpResponse(null, { status: 204 });
      }),
    );

    try {
      render(
        <MemoryRouter>
          <EdgesPage />
        </MemoryRouter>,
      );
      await screen.findByText("bare-metal-1");
      await user.click(screen.getByRole("button", { name: "批量安装设备" }));
      const dialog = await screen.findByRole("dialog", { name: "批量安装 Edge" });
      await within(dialog).findByText("待删除批次");
      await user.click(within(dialog).getByRole("button", { name: "删除" }));

      await user.click(await screen.findByRole("button", { name: "确认" }));
      await waitFor(() => expect(deletedProfile).toBe(7));
      expect(within(dialog).queryByText("待删除批次")).not.toBeInTheDocument();
    } finally {
      confirmSpy.mockRestore();
    }
  });

  it("没有已有集群时可直接新建并绑定批量安装命令", async () => {
    const user = userEvent.setup();
    const requestOrder: string[] = [];
    let clusterInput: Record<string, unknown> | null = null;
    let profileInput: Record<string, unknown> | null = null;
    server.use(
      http.get("/api/v1/topology/nodes", () =>
        HttpResponse.json({ items: [], total: 0 }),
      ),
      http.get("/api/v1/edge-enrollment-profiles", () =>
        HttpResponse.json({ items: [], total: 0, page: 1, page_size: 100 }),
      ),
      http.post("/api/v1/topology/nodes", async ({ request }) => {
        requestOrder.push("cluster");
        clusterInput = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json(
          {
            id: 99,
            type: "cluster",
            name: "上海机房生产集群",
            props: { source: "manual" },
            created_at: "2026-07-31T00:00:00Z",
            updated_at: "2026-07-31T00:00:00Z",
          },
          { status: 201 },
        );
      }),
      http.post("/api/v1/edge-enrollment-profiles", async ({ request }) => {
        requestOrder.push("profile");
        profileInput = (await request.json()) as Record<string, unknown>;
        return HttpResponse.json(
          {
            profile: {
              id: 8,
              name: "首次部署",
              assignment_mode: "cluster",
              cluster_node_id: 99,
              expires_at: "2026-08-01T00:00:00Z",
              max_uses: 100,
              used_count: 0,
              status: "active",
              created_at: "2026-07-31T00:00:00Z",
            },
            enrollment_token: "oen_first_cluster_token",
          },
          { status: 201 },
        );
      }),
    );
    render(
      <MemoryRouter>
        <EdgesPage />
      </MemoryRouter>,
    );

    await screen.findByText("bare-metal-1");

      await user.click(screen.getByRole("button", { name: "批量安装设备" }));

    const dialog = await screen.findByRole("dialog", { name: "批量安装 Edge" });
    await within(dialog).findByText("暂无安装批次");

      await user.type(
        within(dialog).getByLabelText("安装批次名称"),
        "首次部署",
      );
      await selectOption(within(dialog).getByLabelText("归属方式"), "关联拓扑集群");


    expect(
      within(dialog).getByRole("radio", { name: "新建集群" }),
    ).toBeChecked();
    expect(
      within(dialog).getByRole("radio", { name: "选择已有集群" }),
    ).toBeDisabled();

      await user.type(
        within(dialog).getByLabelText("新集群名称"),
        "上海机房生产集群",
      );
      await user.click(
        within(dialog).getByRole("button", { name: "生成安装命令" }),
      );


    await waitFor(() => expect(requestOrder).toEqual(["cluster", "profile"]));
    expect(clusterInput).toEqual({
      type: "cluster",
      name: "上海机房生产集群",
      props: { source: "manual" },
    });
    expect(profileInput).toEqual({
      name: "首次部署",
      assignment_mode: "cluster",
      cluster_node_id: 99,
      expires_in_hours: 24,
      max_uses: 100,
    });
    expect(
      await within(dialog).findByText(
        /--enrollment-token=oen_first_cluster_token/,
      ),
    ).toBeInTheDocument();
  });

  it("按主机、Kubernetes、存储和网络设备展示类型图标", async () => {
    server.use(
      http.get("/api/v1/devices", () =>
        HttpResponse.json({
          items: [
            { id: 17, name: "k8s-host", hostname: "k8s-host", roles: [], online: true },
            { id: 19, name: "app-host", hostname: "app-host", roles: ["server"], online: true },
            { id: 141, name: "storage-host", hostname: "storage-host", roles: ["storage"], online: true },
            { id: 140, name: "core-switch", hostname: "core-switch", os: "network", roles: ["network"], online: false, reachability_status: "reachable" },
          ],
          total: 4,
        }),
      ),
      http.get("/api/v1/edges", () =>
        HttpResponse.json({
          items: [
            { id: 5, name: "k8s-edge", status: "online", roles: [], access_key_id: "ak-k8s", device_id: 17, agent_version: "dev" },
            { id: 9, name: "app-edge", status: "online", roles: ["server"], access_key_id: "ak-app", device_id: 19, agent_version: "dev" },
            { id: 12, name: "storage-edge", status: "online", roles: ["storage"], access_key_id: "ak-storage", device_id: 141, agent_version: "dev" },
          ],
          total: 3,
        }),
      ),
      http.get("/api/v1/k8s/edge-attachments", () =>
        HttpResponse.json({
          items: [
            { edge_id: 5, cluster_id: 1, cluster_name: "prod-k8s", node_name: "k8s-host", kind: "k8s-node" },
          ],
          total: 1,
        }),
      ),
      http.get("/api/v1/topology/nodes", () =>
        HttpResponse.json({ items: [], total: 0 }),
      ),
      http.get("/api/v1/topology/relations", () =>
        HttpResponse.json({ items: [], total: 0 }),
      ),
    );

    render(
      <MemoryRouter initialEntries={["/devices"]}>
        <EdgesPage />
      </MemoryRouter>,
    );

    expect(await screen.findByLabelText("Kubernetes 设备")).toBeInTheDocument();
    expect(screen.getByLabelText("主机设备")).toBeInTheDocument();
    expect(screen.getByLabelText("存储设备")).toBeInTheDocument();
    expect(screen.getByLabelText("网络设备")).toBeInTheDocument();

    const networkRow = screen.getAllByText("core-switch")[0].closest(
      "tr",
    ) as HTMLTableRowElement;
    expect(within(networkRow).getByText("SNMP")).toBeInTheDocument();
    expect(within(networkRow).getByText("可达")).toBeInTheDocument();
    expect(within(networkRow).queryByText("离线")).not.toBeInTheDocument();
    expect(within(networkRow).getByText("查看拓扑")).toBeInTheDocument();
    expect(within(networkRow).queryByText("查看图表")).not.toBeInTheDocument();
    expect(within(networkRow).queryByText("终端")).not.toBeInTheDocument();
  });

  it("网络设备筛选展示资产字段并隐藏网络发现入口", async () => {
    server.use(
      http.get("/api/v1/devices", () =>
        HttpResponse.json({
          items: [
            { id: 140, name: "core-switch", hostname: "core-switch", os: "network", arch: "network", ip_address: "10.20.0.3", roles: ["network"], online: false },
          ],
          total: 1,
        }),
      ),
      http.get("/api/v1/edges", () => HttpResponse.json({ items: [], total: 0 })),
      http.get("/api/v1/k8s/edge-attachments", () =>
        HttpResponse.json({ items: [], total: 0 }),
      ),
      http.get("/api/v1/topology/nodes", () =>
        HttpResponse.json({ items: [], total: 0 }),
      ),
      http.get("/api/v1/topology/relations", () =>
        HttpResponse.json({ items: [], total: 0 }),
      ),
      http.get("/api/v1/devices/140/network", () =>
        HttpResponse.json({
          device_id: 140,
          device_kind: "switch",
          vendor: "Ongrid Labs",
          model: "VirtualSwitch 24",
          management_address: "10.20.0.3",
          bridge_base_mac: "b2:94:4a:34:5b:fb",
          interfaces: [
            { if_index: 1, name: "lo" },
            { if_index: 2, name: "eth0" },
            { if_index: 3, name: "eth1" },
          ],
          reachability_status: "reachable",
          scanner_host_name: "scanner-host",
          last_observed_at: "2026-06-29T10:00:00Z",
        }),
      ),
    );

    render(
      <MemoryRouter initialEntries={["/devices?roles=network"]}>
        <EdgesPage />
      </MemoryRouter>,
    );

    await screen.findAllByText("core-switch");
    const table = screen.getByRole("table");
    expect(table).toHaveClass("w-full", "min-w-[1200px]", "table-fixed");
    expect(table.querySelectorAll("col")).toHaveLength(10);
    expect(screen.queryByRole("button", { name: "网络发现" })).not.toBeInTheDocument();
    expect(screen.queryByText("WebSSH 会话")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "批量安装设备" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "新建设备" })).not.toBeInTheDocument();
    expect(await screen.findByText("Ongrid Labs · VirtualSwitch 24")).toBeInTheDocument();
    expect(screen.queryByText("b2:94:4a:34:5b:fb")).not.toBeInTheDocument();
    expect(screen.queryByText("设备类型")).not.toBeInTheDocument();
    expect(screen.getByText("接口")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();
    expect(screen.getByText("可达")).toBeInTheDocument();
    expect(screen.getByText("scanner-host")).toBeInTheDocument();
  });

  it("网络发现页只统计等待校验的候选并隐藏主机操作", async () => {
    server.use(
      http.get("/api/v1/network-discovery/candidates", () =>
        HttpResponse.json({
          items: [
            {
              id: 1,
              source: "arp",
              ip_address: "10.20.0.1",
              status: "candidate",
              observer_edge_id: 9,
              observer_host_name: "scanner-host",
              confidence: 20,
            },
            {
              id: 2,
              source: "snmp",
              ip_address: "10.20.0.2",
              status: "promoted",
              promoted_device_id: 140,
              observer_edge_id: 9,
              observer_host_name: "scanner-host",
              confidence: 100,
            },
          ],
          total: 2,
        }),
      ),
    );

    render(
      <MemoryRouter initialEntries={["/devices?view=network-discovery"]}>
        <EdgesPage />
      </MemoryRouter>,
    );

    expect(await screen.findByRole("heading", { name: "网络发现" })).toBeInTheDocument();
    expect(screen.getByText("1 个候选等待 SNMP 校验")).toBeInTheDocument();
    expect(screen.queryByText("WebSSH 会话")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "批量安装设备" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "新建设备" })).not.toBeInTheDocument();
  });
});

function LocationProbe() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname}</div>;
}
