import { act } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { http, HttpResponse } from "msw";
import { beforeEach, describe, expect, it, vi } from "vitest";

import EdgesPage from "./Edges";
import { server } from "@/test/msw-server";

vi.mock("@/store/me", () => ({
  usePermissions: () => ({ isAdmin: true, canMutate: true, role: "admin" }),
}));

describe("EdgesPage", () => {
  beforeEach(() => {
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
              roles: ["server"],
              online: true,
              last_seen_at: "2026-06-29T10:00:00Z",
            },
          ],
          total: 3,
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

  it("隐藏 Controller Edge，并把 K8s Controller 标到所在 Node Edge", async () => {
    render(
      <MemoryRouter>
        <EdgesPage />
      </MemoryRouter>,
    );

    const k8sNameCells = await screen.findAllByText("ongrid-k8s-control-plane");
    expect(k8sNameCells).toHaveLength(2);
    expect(
      screen.queryByText("k8s:kind-local:ongrid-k8s-control-plane"),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("kind-controller")).not.toBeInTheDocument();
    expect(screen.getByText("K8s Node")).toBeInTheDocument();
    expect(screen.getByText("K8s Controller")).toBeInTheDocument();
    expect(screen.getByText("kind-local")).toBeInTheDocument();
    const k8sRow = k8sNameCells[0].closest("tr");
    expect(k8sRow).not.toBeNull();
    expect(
      within(k8sRow as HTMLTableRowElement).getByText("Kubernetes 管理"),
    ).toBeInTheDocument();
    expect(
      within(k8sRow as HTMLTableRowElement).queryByText("终端"),
    ).not.toBeInTheDocument();
    expect(
      within(k8sRow as HTMLTableRowElement).queryByLabelText(/选择/),
    ).not.toBeInTheDocument();
    expect(screen.getByText("bare-metal-1")).toBeInTheDocument();
    expect(screen.getByText("bm-1")).toBeInTheDocument();
    expect(screen.getByText("Host Edge")).toBeInTheDocument();
    expect(screen.getByRole("table")).toHaveClass("w-full", "min-w-[1260px]");
  });

  it("点击 K8s 托管设备行进入设备详情，Kubernetes 管理按钮仍进入集群页", async () => {
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

    await act(async () => {
      await user.click(k8sRow);
    });
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent("/devices/17"),
    );

    await act(async () => {
      await user.click(within(k8sRow).getByText("Kubernetes 管理"));
    });
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent("/kubernetes/1"),
    );
  });
});

function LocationProbe() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname}</div>;
}
