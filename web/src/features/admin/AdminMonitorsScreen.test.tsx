import { describe, expect, test } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type {
	AdminMonitor,
	AdminMonitorListResponse,
	AdminWorkspaceListResponse,
} from '@/lib/api/types';
import { formatRelativeTime } from '@/lib/format';
import { AdminMonitorsScreen } from './AdminMonitorsScreen';

const ORIGIN = 'http://localhost:5173';
const monitorsPath = `${ORIGIN}/api/v1/admin/monitors`;
const workspacesPath = `${ORIGIN}/api/v1/admin/workspaces`;

const redactedUrl = 'https://api.example.com/health';

const monitor: AdminMonitor = {
	id: 'm1',
	workspace_id: 'w1',
	workspace_name: 'Acme Status',
	workspace_slug: 'acme-status',
	name: 'API',
	url: redactedUrl,
	method: 'GET',
	expected_status: '2xx',
	interval_seconds: 60,
	timeout_seconds: 10,
	paused: false,
	status: 'up',
	last_checked_at: new Date(Date.now() - 5 * 60_000).toISOString(),
	last_status_code: 200,
	last_latency_ms: 123,
	last_error: null,
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z',
};

const workspace = {
	id: 'w1',
	name: 'Acme Status',
	slug: 'acme-status',
	created_by: 'u1',
	created_at: '2026-01-01T00:00:00Z',
	monitor_limit: 10,
	suspended_at: null,
	suspended_by: null,
	suspended_reason: null,
	member_count: 2,
	monitor_count: 3,
};

function setupHandlers(pages: AdminMonitorListResponse[]) {
	const urls: URL[] = [];
	server.use(
		http.get(monitorsPath, ({ request }) => {
			const url = new URL(request.url);
			urls.push(url);
			const cursor = url.searchParams.get('cursor');
			const page = cursor ? pages[1] : pages[0];
			return HttpResponse.json(page);
		}),
		http.get(workspacesPath, () => {
			const response: AdminWorkspaceListResponse = {
				workspaces: [workspace],
			};
			return HttpResponse.json(response);
		}),
	);
	return urls;
}

describe('AdminMonitorsScreen', () => {
	test('renders the redacted URL without reconstructing a query string', async () => {
		setupHandlers([{ monitors: [monitor] }]);

		renderWithProviders(<AdminMonitorsScreen />);

		expect(
			await screen.findByRole('heading', { name: 'Monitors' }),
		).toBeInTheDocument();
		expect(await screen.findByText('API')).toBeInTheDocument();
		expect(screen.getByText(redactedUrl)).toBeInTheDocument();
		expect(screen.getByText('Acme Status')).toBeInTheDocument();
		expect(screen.getByText('Up')).toBeInTheDocument();
		expect(screen.getByText('123 ms')).toBeInTheDocument();
		expect(
			screen.getByText(formatRelativeTime(monitor.last_checked_at!)),
		).toBeInTheDocument();
		expect(screen.queryByText(/secret=|token=/)).not.toBeInTheDocument();
	});

	test('the workspace combobox filters by workspace_id', async () => {
		const user = userEvent.setup();
		const urls = setupHandlers([{ monitors: [monitor] }]);

		renderWithProviders(<AdminMonitorsScreen />);
		await screen.findByText('API');

		await user.type(screen.getByLabelText('Workspace'), 'acme');
		const option = await screen.findByRole(
			'option',
			{ name: /Acme Status/ },
			{ timeout: 2000 },
		);
		await user.click(option);

		await waitFor(
			() => {
				expect(urls.at(-1)?.searchParams.get('workspace_id')).toBe('w1');
			},
			{ timeout: 2000 },
		);
	});

	test('search updates q and Load more uses next_cursor', async () => {
		const user = userEvent.setup();
		const urls = setupHandlers([
			{ monitors: [monitor], next_cursor: 'page-2' },
			{
				monitors: [
					{
						...monitor,
						id: 'm2',
						name: 'Status page',
						url: 'https://status.example.com/',
					},
				],
			},
		]);

		renderWithProviders(<AdminMonitorsScreen />);
		await screen.findByText('API');

		await user.type(screen.getByPlaceholderText('Search monitors'), 'api');
		await waitFor(
			() => {
				expect(urls.at(-1)?.searchParams.get('q')).toBe('api');
			},
			{ timeout: 2000 },
		);

		await user.click(await screen.findByRole('button', { name: 'Load more' }));

		expect(await screen.findByText('Status page')).toBeInTheDocument();
		expect(urls.at(-1)?.searchParams.get('cursor')).toBe('page-2');
	});

	test('renders one row per monitor', async () => {
		setupHandlers([{ monitors: [monitor] }]);

		renderWithProviders(<AdminMonitorsScreen />);

		const row = await screen.findByRole('row', { name: /API/ });
		expect(within(row).getByText(redactedUrl)).toBeInTheDocument();
	});
});
