import type { AnchorHTMLAttributes, ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, delay, http } from 'msw';
import { server } from '@/test/msw/server';
import { createTestQueryClient, renderWithProviders } from '@/test/render';
import type { Monitor, MonitorListResponse, Workspace } from '@/lib/api/types';
import { formatRelativeTime, formatUptime } from '@/lib/format';
import { queryKeys } from '@/lib/query/keys';
import { MonitorListScreen } from './MonitorListScreen';

const ORIGIN = 'http://localhost:5173';

const harness = vi.hoisted(() => ({
	navigate: vi.fn(),
}));

vi.mock('@tanstack/react-router', async (importOriginal) => {
	const actual =
		await importOriginal<typeof import('@tanstack/react-router')>();

	function buildHref(to: string, params?: Record<string, unknown>): string {
		let href = to;
		for (const [key, value] of Object.entries(params ?? {})) {
			href = href.replace(`$${key}`, String(value));
		}
		return href;
	}

	return {
		...actual,
		useNavigate: () => harness.navigate,
		useParams: () => ({ workspaceSlug: 'acme-status' }),
		Link: ({
			to,
			params,
			children,
			...props
		}: {
			to?: string;
			params?: Record<string, unknown>;
			children?: ReactNode;
		} & Omit<AnchorHTMLAttributes<HTMLAnchorElement>, 'href'>) => (
			<a href={to ? buildHref(to, params) : undefined} {...props}>
				{children}
			</a>
		),
	};
});

const workspace: Workspace = {
	id: 'w1',
	name: 'Acme Status',
	slug: 'acme-status',
	role: 'member',
};

const CHECKED_AT = new Date(Date.now() - 5 * 60_000).toISOString();

const summary = {
	window: '24h',
	uptime: 0.987,
	checks: 10,
	up: 9,
	down: 1,
	avg_latency_ms: 120,
	p95_latency_ms: 200,
	min_latency_ms: 80,
	max_latency_ms: 300,
};

function makeMonitor(overrides: Partial<Monitor> = {}): Monitor {
	return {
		id: 'm1',
		workspace_id: workspace.id,
		name: 'API',
		url: 'https://api.example.com/health?probe=1',
		method: 'GET',
		headers: {},
		has_body: false,
		content_type: null,
		expected_status: '2xx',
		interval_seconds: 60,
		timeout_seconds: 10,
		confirmation_threshold: 1,
		recovery_threshold: 1,
		paused: false,
		status: 'up',
		last_check: {
			checked_at: CHECKED_AT,
			status_code: 200,
			latency_ms: 120,
			error: null,
		},
		summary_24h: summary,
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-01T00:00:00Z',
		...overrides,
	};
}

const listPath = `${ORIGIN}/api/v1/workspaces/${workspace.id}/monitors`;
const monitorPath = (id = 'm1') => `${listPath}/${id}`;

function problem(code: string, status: number, title: string) {
	return HttpResponse.json(
		{
			type: 'about:blank',
			title,
			status,
			code,
		},
		{
			status,
			headers: { 'Content-Type': 'application/problem+json' },
		},
	);
}

function listHandler(
	first: MonitorListResponse,
	second?: MonitorListResponse,
	requests: (string | null)[] = [],
) {
	return http.get(listPath, ({ request }) => {
		const cursor = new URL(request.url).searchParams.get('cursor');
		requests.push(cursor);
		return HttpResponse.json(cursor ? second : first);
	});
}

describe('MonitorListScreen', () => {
	beforeEach(() => {
		harness.navigate.mockReset();
	});

	afterEach(() => {
		vi.restoreAllMocks();
	});

	test('renders name, API URL, status, last check, and uptime', async () => {
		const monitor = makeMonitor();
		server.use(listHandler({ monitors: [monitor] }));

		renderWithProviders(<MonitorListScreen workspace={workspace} />);

		expect(await screen.findByText('API')).toBeInTheDocument();
		expect(screen.getByText(monitor.url)).toBeInTheDocument();
		expect(screen.getByText('Up')).toBeInTheDocument();
		expect(
			screen.getByText(formatRelativeTime(CHECKED_AT)),
		).toBeInTheDocument();
		expect(
			screen.getByText(formatUptime(summary.uptime, summary.checks)),
		).toBeInTheDocument();
		expect(screen.getAllByRole('link', { name: 'New monitor' })).toHaveLength(
			1,
		);
	});

	test('renders No data for a null summary or zero checks', async () => {
		server.use(
			listHandler({
				monitors: [
					makeMonitor({ id: 'm1', name: 'No summary', summary_24h: null }),
					makeMonitor({
						id: 'm2',
						name: 'No checks',
						summary_24h: { ...summary, checks: 0, uptime: null },
					}),
				],
			}),
		);

		renderWithProviders(<MonitorListScreen workspace={workspace} />);

		expect(await screen.findByText('No summary')).toBeInTheDocument();
		expect(screen.getAllByText('No data')).toHaveLength(2);
	});

	test('Load more requests the cursor from the first page and appends it', async () => {
		const requests: (string | null)[] = [];
		const second = makeMonitor({ id: 'm2', name: 'Second monitor' });
		server.use(
			listHandler(
				{ monitors: [makeMonitor()], next_cursor: 'page-2' },
				{ monitors: [second] },
				requests,
			),
		);
		const user = userEvent.setup();

		renderWithProviders(<MonitorListScreen workspace={workspace} />);
		await screen.findByText('API');

		await user.click(screen.getByRole('button', { name: 'Load more' }));

		expect(await screen.findByText('Second monitor')).toBeInTheDocument();
		expect(requests).toEqual([null, 'page-2']);
		expect(
			screen.queryByRole('button', { name: 'Load more' }),
		).not.toBeInTheDocument();
	});

	test('pause optimistically shows Paused and calls POST /pause', async () => {
		let paused = false;
		const pauseCalls: string[] = [];
		server.use(
			http.get(listPath, () =>
				HttpResponse.json({
					monitors: [makeMonitor({ paused, status: paused ? 'paused' : 'up' })],
				}),
			),
			http.post(`${monitorPath()}/pause`, async () => {
				pauseCalls.push('pause');
				await delay(100);
				paused = true;
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<MonitorListScreen workspace={workspace} />);
		await screen.findByText('Up');

		await user.click(screen.getByRole('button', { name: 'Pause API' }));

		expect(await screen.findByText('Paused')).toBeInTheDocument();
		await waitFor(() => expect(pauseCalls).toHaveLength(1));
		expect(screen.getByText('Paused')).toBeInTheDocument();
	});

	test('pause and resume actions stay disabled while a pause is pending', async () => {
		let paused = false;
		server.use(
			http.get(listPath, () =>
				HttpResponse.json({
					monitors: [makeMonitor({ paused, status: paused ? 'paused' : 'up' })],
				}),
			),
			http.post(`${monitorPath()}/pause`, async () => {
				await delay(150);
				paused = true;
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<MonitorListScreen workspace={workspace} />);
		await screen.findByText('Up');

		await user.click(screen.getByRole('button', { name: 'Pause API' }));

		expect(
			await screen.findByRole('button', { name: 'Resume API' }),
		).toBeDisabled();
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'Resume API' })).toBeEnabled(),
		);
	});

	test('pause and resume actions stay disabled while a resume is pending', async () => {
		let paused = true;
		server.use(
			http.get(listPath, () =>
				HttpResponse.json({
					monitors: [makeMonitor({ paused, status: paused ? 'paused' : 'up' })],
				}),
			),
			http.post(`${monitorPath()}/resume`, async () => {
				await delay(150);
				paused = false;
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<MonitorListScreen workspace={workspace} />);
		await screen.findByText('Paused');

		await user.click(screen.getByRole('button', { name: 'Resume API' }));

		expect(
			await screen.findByRole('button', { name: 'Pause API' }),
		).toBeDisabled();
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'Pause API' })).toBeEnabled(),
		);
	});

	test('a failed pause restores the previous badge', async () => {
		server.use(
			listHandler({ monitors: [makeMonitor()] }),
			http.post(`${monitorPath()}/pause`, async () => {
				await delay(50);
				return problem('forbidden', 403, 'Forbidden');
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<MonitorListScreen workspace={workspace} />);
		await screen.findByText('Up');

		await user.click(screen.getByRole('button', { name: 'Pause API' }));

		expect(await screen.findByText('Paused')).toBeInTheDocument();
		expect(await screen.findByText('Up')).toBeInTheDocument();
		expect(screen.queryByText('Paused')).not.toBeInTheDocument();
	});

	test('resume optimistically clears paused, calls POST /resume, and refetches', async () => {
		let paused = true;
		const requests: (string | null)[] = [];
		const resumeCalls: string[] = [];
		server.use(
			http.get(listPath, ({ request }) => {
				requests.push(new URL(request.url).searchParams.get('cursor'));
				return HttpResponse.json({
					monitors: [makeMonitor({ paused, status: paused ? 'paused' : 'up' })],
				});
			}),
			http.post(`${monitorPath()}/resume`, async () => {
				resumeCalls.push('resume');
				await delay(100);
				paused = false;
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<MonitorListScreen workspace={workspace} />);
		await screen.findByText('Paused');

		await user.click(screen.getByRole('button', { name: 'Resume API' }));

		await waitFor(() =>
			expect(screen.queryByText('Paused')).not.toBeInTheDocument(),
		);
		await waitFor(() => expect(resumeCalls).toHaveLength(1));
		await waitFor(() => expect(requests.length).toBeGreaterThanOrEqual(2));
		expect(await screen.findByText('Up')).toBeInTheDocument();
	});

	test('a failed resume restores the paused badge', async () => {
		server.use(
			listHandler({
				monitors: [makeMonitor({ paused: true, status: 'paused' })],
			}),
			http.post(`${monitorPath()}/resume`, async () => {
				await delay(50);
				return problem('forbidden', 403, 'Forbidden');
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<MonitorListScreen workspace={workspace} />);
		await screen.findByText('Paused');

		await user.click(screen.getByRole('button', { name: 'Resume API' }));

		await waitFor(() =>
			expect(screen.queryByText('Paused')).not.toBeInTheDocument(),
		);
		expect(await screen.findByText('Paused')).toBeInTheDocument();
	});

	test('delete asks for confirmation and then calls DELETE', async () => {
		let deleted = false;
		const deleteCalls: string[] = [];
		server.use(
			http.get(listPath, () =>
				HttpResponse.json({
					monitors: deleted ? [] : [makeMonitor()],
				}),
			),
			http.delete(monitorPath(), () => {
				deleteCalls.push('delete');
				deleted = true;
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<MonitorListScreen workspace={workspace} />);
		await screen.findByText('API');

		await user.click(screen.getByRole('button', { name: 'Delete API' }));

		expect(
			await screen.findByRole('heading', { name: 'Delete API?' }),
		).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Delete monitor' }));

		await waitFor(() => expect(deleteCalls).toHaveLength(1));
		expect(await screen.findByText('No monitors yet')).toBeInTheDocument();
		expect(harness.navigate).toHaveBeenCalledWith({
			to: '/w/$workspaceSlug/monitors',
			params: { workspaceSlug: workspace.slug },
		});
	});

	test('a viewer sees no New monitor, pause, or delete controls', async () => {
		server.use(listHandler({ monitors: [makeMonitor()] }));

		renderWithProviders(
			<MonitorListScreen workspace={{ ...workspace, role: 'viewer' }} />,
		);

		expect(await screen.findByText('API')).toBeInTheDocument();
		expect(
			screen.queryByRole('link', { name: 'New monitor' }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole('button', { name: 'Pause API' }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByRole('button', { name: 'Delete API' }),
		).not.toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'View API' })).toBeInTheDocument();
	});

	test('the empty state shows No monitors yet', async () => {
		server.use(listHandler({ monitors: [] }));

		renderWithProviders(<MonitorListScreen workspace={workspace} />);

		expect(await screen.findByText('No monitors yet')).toBeInTheDocument();
		expect(screen.getAllByRole('link', { name: 'New monitor' })).toHaveLength(
			1,
		);
	});

	test('a failed background refetch keeps stale rows and shows a retry banner', async () => {
		let fail = false;
		const queryClient = createTestQueryClient();
		server.use(
			http.get(listPath, () =>
				fail
					? problem('internal_error', 500, 'Internal Server Error')
					: HttpResponse.json({ monitors: [makeMonitor()] }),
			),
		);
		const user = userEvent.setup();

		renderWithProviders(<MonitorListScreen workspace={workspace} />, {
			queryClient,
		});
		await screen.findByText('API');

		fail = true;
		await act(async () => {
			await queryClient.refetchQueries(
				{ queryKey: queryKeys.monitors(workspace.id) },
				{ cancelRefetch: false, throwOnError: false },
			);
		});

		expect(
			await screen.findByText('Could not refresh monitors'),
		).toBeInTheDocument();
		expect(screen.getByText('API')).toBeInTheDocument();
		expect(screen.getByText('Up')).toBeInTheDocument();

		fail = false;
		await user.click(screen.getByRole('button', { name: 'Retry' }));

		await waitFor(() =>
			expect(
				screen.queryByText('Could not refresh monitors'),
			).not.toBeInTheDocument(),
		);
		expect(screen.getByText('API')).toBeInTheDocument();
	});

	test('renders a table with row columns on desktop', async () => {
		vi.spyOn(window, 'matchMedia').mockImplementation(
			(query: string) =>
				({
					matches: query === '(min-width: 768px)',
					media: query,
					onchange: null,
					addListener: () => undefined,
					removeListener: () => undefined,
					addEventListener: () => undefined,
					removeEventListener: () => undefined,
					dispatchEvent: () => false,
				}) as MediaQueryList,
		);
		server.use(listHandler({ monitors: [makeMonitor()] }));

		renderWithProviders(<MonitorListScreen workspace={workspace} />);

		const table = await screen.findByRole('table');
		const row = within(table).getByRole('row', { name: /API/ });

		expect(within(row).getByText('API')).toBeInTheDocument();
		expect(within(row).getByText(makeMonitor().url)).toBeInTheDocument();
		expect(within(row).getByText('Up')).toBeInTheDocument();
		expect(
			within(row).getByText(formatRelativeTime(CHECKED_AT)),
		).toBeInTheDocument();
		expect(
			within(row).getByText(formatUptime(summary.uptime, summary.checks)),
		).toBeInTheDocument();
		expect(
			within(row).getByRole('button', { name: 'Pause API' }),
		).toBeInTheDocument();
	});

	test('search filters the loaded pages by name or URL', async () => {
		server.use(
			listHandler({
				monitors: [
					makeMonitor({ id: 'm1', name: 'Alpha API' }),
					makeMonitor({
						id: 'm2',
						name: 'Beta status',
						url: 'https://beta.example.com/health',
					}),
				],
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(<MonitorListScreen workspace={workspace} />);
		await screen.findByText('Alpha API');

		await user.type(
			screen.getByLabelText('Search monitors'),
			'beta.example.com',
		);

		expect(screen.queryByText('Alpha API')).not.toBeInTheDocument();
		expect(screen.getByText('Beta status')).toBeInTheDocument();
	});
});
