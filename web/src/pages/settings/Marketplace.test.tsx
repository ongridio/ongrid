import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import SettingsMarketplace from './Marketplace';
import { SignatureBadge } from '@/components/marketplace/SignatureBadge';
import { server } from '@/test/msw-server';
import {
  etcdCapabilities,
  etcdPack,
  registries,
} from '@/test/fixtures/marketplace';

// useAuth is mocked module-wide so individual tests can flip the role
// (admin vs user) without touching zustand internals. The selector form
// `useAuth((s) => s.role)` means the mock just needs to honour the
// callback contract.
let mockRole: string = 'admin';
vi.mock('@/store/auth', () => ({
  useAuth: <T,>(selector: (s: { role: string }) => T): T =>
    selector({ role: mockRole }),
  getToken: () => null,
  getRefreshToken: () => null,
}));

const installedURL = '/api/v1/marketplace/installed';
const installURL = '/api/v1/marketplace/install';
const registriesURL = '/api/v1/marketplace/registries';
const installedItemURL = (id: string) =>
  `/api/v1/marketplace/installed/${encodeURIComponent(id)}`;

beforeEach(() => {
  mockRole = 'admin';
  localStorage.setItem('ongrid-locale', 'zh-CN');
});

afterEach(() => {
  vi.clearAllMocks();
});

describe('SettingsMarketplace', () => {
  const renderMarketplace = () =>
    render(
      <MemoryRouter>
        <SettingsMarketplace />
      </MemoryRouter>,
    );

  const selectLocalPath = async () => {
    await userEvent.click(screen.getByRole('button', { name: '本地路径' }));
    return screen.getByPlaceholderText(/var\/lib\/ongrid\/uploads/);
  };

  beforeEach(() => {
    server.use(
      http.get('/api/v1/secrets', () => HttpResponse.json({ items: [] })),
    );
  });

  it('renders empty state when no packs installed', async () => {
    server.use(
      http.get(installedURL, () => HttpResponse.json({ items: [] })),
      http.get(registriesURL, () => HttpResponse.json({ items: registries })),
    );

    renderMarketplace();

    expect(
      await screen.findByText(/还没有安装任何包/),
    ).toBeInTheDocument();
    expect(screen.getByText('已安装 (0)')).toBeInTheDocument();
  });

  it('renders installed pack list with capabilities expander', async () => {
    server.use(
      http.get(installedURL, () => HttpResponse.json({ items: [etcdPack] })),
      http.get(registriesURL, () => HttpResponse.json({ items: registries })),
    );

    renderMarketplace();

    expect(await screen.findByText('已安装 (1)')).toBeInTheDocument();
    expect(screen.getByText('etcd-troubleshoot')).toBeInTheDocument();

    // Click the [详情] button to expand and verify bins / config_keys appear.
    const detailsBtn = screen.getByRole('button', { name: '详情' });
    await userEvent.click(detailsBtn);

    expect(await screen.findByText(/binaries/i)).toBeInTheDocument();
    expect(screen.getByText(/etcdctl/)).toBeInTheDocument();
    expect(screen.getByText(/config keys/i)).toBeInTheDocument();
    expect(screen.getByText(/ETCD_ENDPOINTS/)).toBeInTheDocument();
  });

  it('local install happy path', async () => {
    let installedItems: typeof etcdPack[] = [];
    server.use(
      http.get(installedURL, () =>
        HttpResponse.json({ items: installedItems }),
      ),
      http.get(registriesURL, () => HttpResponse.json({ items: registries })),
      http.post(installURL, async () => {
        installedItems = [etcdPack];
        return HttpResponse.json({
          pack: etcdPack,
          capabilities: etcdCapabilities,
          warnings: [],
        });
      }),
    );

    renderMarketplace();

    // Wait for the empty state to settle, then drive the install form.
    await screen.findByText(/还没有安装任何包/);

    const pathInput = await selectLocalPath();
    await userEvent.type(pathInput, '/var/lib/ongrid/uploads/etcd-troubleshoot');

    const installBtn = screen.getByRole('button', { name: /^安装$/ });
    await userEvent.click(installBtn);

    // Confirm modal lands with capabilities snapshot.
    expect(
      await screen.findByText(/已安装: etcd-troubleshoot v0\.1\.0/),
    ).toBeInTheDocument();
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('能力声明')).toBeInTheDocument();

    // Click [完成] to keep the install. List should now show the pack.
    await userEvent.click(within(dialog).getByRole('button', { name: '完成' }));

    expect(await screen.findByText('已安装 (1)')).toBeInTheDocument();
    expect(screen.getByText('etcd-troubleshoot')).toBeInTheDocument();
  });

  it('local install rollback button', async () => {
    let installedItems: typeof etcdPack[] = [];
    let deleteCalled = false;
    server.use(
      http.get(installedURL, () =>
        HttpResponse.json({ items: installedItems }),
      ),
      http.get(registriesURL, () => HttpResponse.json({ items: registries })),
      http.post(installURL, async () => {
        installedItems = [etcdPack];
        return HttpResponse.json({
          pack: etcdPack,
          capabilities: etcdCapabilities,
          warnings: [],
        });
      }),
      http.delete(installedItemURL(etcdPack.pack_id), () => {
        deleteCalled = true;
        installedItems = [];
        return new HttpResponse(null, { status: 204 });
      }),
    );

    renderMarketplace();

    await screen.findByText(/还没有安装任何包/);

    await userEvent.type(
      await selectLocalPath(),
      '/var/lib/ongrid/uploads/etcd-troubleshoot',
    );
    await userEvent.click(screen.getByRole('button', { name: /^安装$/ }));

    const dialog = await screen.findByRole('dialog');
    await userEvent.click(
      within(dialog).getByRole('button', { name: /回滚卸载/ }),
    );

    await waitFor(() => expect(deleteCalled).toBe(true));

    // Modal closed and installed list back to empty state.
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    );
    expect(await screen.findByText(/还没有安装任何包/)).toBeInTheDocument();
    expect(screen.queryByText('etcd-troubleshoot')).not.toBeInTheDocument();
  });

  it('install rejects duplicate sha (409)', async () => {
    server.use(
      http.get(installedURL, () => HttpResponse.json({ items: [] })),
      http.get(registriesURL, () => HttpResponse.json({ items: registries })),
      http.post(installURL, () =>
        HttpResponse.json(
          { error: 'manifest sha already installed' },
          { status: 409 },
        ),
      ),
    );

    renderMarketplace();

    await screen.findByText(/还没有安装任何包/);
    await userEvent.type(
      await selectLocalPath(),
      '/var/lib/ongrid/uploads/etcd-troubleshoot',
    );
    await userEvent.click(screen.getByRole('button', { name: /^安装$/ }));

    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent(/已经安装过/);
  });

  it('install rejects non-allowed source (400)', async () => {
    server.use(
      http.get(installedURL, () => HttpResponse.json({ items: [] })),
      http.get(registriesURL, () => HttpResponse.json({ items: registries })),
      http.post(installURL, () =>
        HttpResponse.json(
          { error: 'source not in allow list' },
          { status: 400 },
        ),
      ),
    );

    renderMarketplace();

    await screen.findByText(/还没有安装任何包/);
    await userEvent.type(
      await selectLocalPath(),
      '/etc/passwd',
    );
    await userEvent.click(screen.getByRole('button', { name: /^安装$/ }));

    const toast = await screen.findByRole('status');
    expect(toast).toHaveTextContent(/未在允许列表/);
  });

  it('non-admin sees install button disabled', async () => {
    mockRole = 'user';
    server.use(
      http.get(installedURL, () => HttpResponse.json({ items: [] })),
      http.get(registriesURL, () => HttpResponse.json({ items: registries })),
    );

    renderMarketplace();

    await screen.findByText(/还没有安装任何包/);

    // Even if the user types something, the submit button stays disabled
    // because !isAdmin gates submit independently of canSubmit.
    const installBtn = screen.getByRole('button', { name: /^安装$/ });
    expect(installBtn).toBeDisabled();
    expect(installBtn).toHaveAttribute('title', '需要 admin 权限');

    // The local-path input is also disabled for non-admin viewers.
    const pathInput = await selectLocalPath();
    expect(pathInput).toBeDisabled();

    // And the helper line nudges them toward admin login.
    expect(screen.getByText(/仅 admin 可执行安装/)).toBeInTheDocument();
  });

  it('signature_state badge variants', () => {
    const { rerender, container } = render(<SignatureBadge state="verified" />);
    expect(container).toHaveTextContent('verified');

    rerender(<SignatureBadge state="unsigned" />);
    expect(container).toHaveTextContent('unsigned');

    rerender(<SignatureBadge state="failed" />);
    expect(container).toHaveTextContent('signature failed');
  });
});
