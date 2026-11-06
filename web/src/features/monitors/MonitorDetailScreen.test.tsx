import type { AnchorHTMLAttributes, ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, delay, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type {
	Incident,
	Monitor,
	MonitorAlertState,
	ProbeResult,
	ResultsWindow,
	Summary,
	Workspace,
} from '@/lib/api/types';
import { formatRelativeTime } from '@/lib/format';
import { MonitorDetailScreen } from './MonitorDetailScreen';

const ORIGIN = 'http://localhost:5173';

const harness = vi.hoisted(() => ({
	navigate: vi.fn(),
	search: { window: '24h' } as Record<string, unknown>,
	listeners: new Set<() => void>(),
}));

vi.mock('@tanstack/react-router', async (importOriginal) => {
	const actual =
		await importOriginal<typeof import('@tanstack/react-router')>();
	const React = await import('react');

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
		useSearch: () =>
			React.useSyncExternalStore(
				(onStoreChange) => {
					harness.listeners.add(onStoreChange);
					return () => harness.listeners.delete(onStoreChange);
				},
				() => harness.search,
			),
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

const summary: Summary = {
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

const probeResult: ProbeResult = {
	id: 'r1',
	checked_at: CHECKED_AT,
	scheduled_for: CHECKED_AT,
	status: 'up',
	status_code: 200,
	latency_ms: 120,
	error: null,
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
const monitorPath = `${listPath}/m1`;
const summaryPath = `${monitorPath}/summary`;
const resultsPath = `${monitorPath}/results`;
const alertChannelPath = `${monitorPath}/alert-channel`;
const incidentsPath = `${ORIGIN}/api/v1/workspaces/${workspace.id}/incidents`;

const defaultAlertState: MonitorAlertState = {
	available: true,
	channel_enabled: true,
	enabled: true,
};

function makeIncident(overrides: Partial<Incident> = {}): Incident {
	return {
		id: 'i1',
		workspace_id: workspace.id,
		monitor_id: 'm1',
		monitor_name: 'API',
		status: 'open',
		started_at: new Date(Date.now() - 60 * 60_000).toISOString(),
		acknowledged_at: null,
		acknowledged_by: null,
		resolved_at: null,
		last_notified_at: null,
		reminder_count: 0,
		...overrides,
	};
}

function problem(code: string, status: number, title: string) {
	return HttpResponse.json(
		{ type: 'about:blank', title, status, code },
		{ status, headers: { 'Content-Type': 'application/problem+json' } },
	);
}

interface WindowRequests {
	summary: (string | null)[];
	results: (string | null)[];
}

function useMonitorHandlers(
	monitor: Monitor = makeMonitor(),
	requests: WindowRequests = { summary: [], results: [] },
	results: ProbeResult[] = [],
	alertState: MonitorAlertState = defaultAlertState,
) {
	server.use(
		http.get(monitorPath, () => HttpResponse.json(monitor)),
		http.get(summaryPath, ({ request }) => {
			requests.summary.push(new URL(request.url).searchParams.get('window'));
			return HttpResponse.json(summary);
		}),
		http.get(resultsPath, ({ request }) => {
			requests.results.push(new URL(request.url).searchParams.get('window'));
			return HttpResponse.json({ results });
		}),
		http.get(alertChannelPath, () => HttpResponse.json(alertState)),
		http.get(incidentsPath, () => HttpResponse.json({ incidents: [] })),
	);

	return requests;
}

function useIncidentHandlers(
	incidents: Incident[],
	requests: URLSearchParams[] = [],
) {
	server.use(
		http.get(incidentsPath, ({ request }) => {
			requests.push(new URL(request.url).searchParams);
			return HttpResponse.json({ incidents });
		}),
	);

	return requests;
}

describe('MonitorDetailScreen', () => {
	beforeEach(() => {
		harness.listeners.clear();
		harness.navigate.mockReset();
		harness.navigate.mockImplementation(
			(options: { search?: Record<string, unknown> } = {}) => {
				if (options.search) {
					harness.search = options.search;
					for (const listener of harness.listeners) listener();
				}
			},
		);
		harness.search = { window: '24h' };
	});

	afterEach(() => {
		vi.restoreAllMocks();
	});

	test('window selector refetches summary and results for the chosen window', async () => {
		const requests = useMonitorHandlers();
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(
			await screen.findByRole('heading', { name: 'API' }),
		).toBeInTheDocument();
		await waitFor(() => expect(requests.summary).toContain('24h'));
		expect(requests.results).toContain('24h');

		for (const timeWindow of ['1h', '6h', '7d'] as ResultsWindow[]) {
			await user.click(screen.getByRole('button', { name: timeWindow }));

			await waitFor(() => expect(requests.summary.at(-1)).toBe(timeWindow));
			await waitFor(() => expect(requests.results.at(-1)).toBe(timeWindow));
			expect(harness.navigate).toHaveBeenLastCalledWith(
				expect.objectContaining({ search: { window: timeWindow } }),
			);
		}

		await user.click(screen.getByRole('button', { name: '24h' }));

		expect(screen.getByRole('button', { name: '24h' })).toHaveAttribute(
			'aria-pressed',
			'true',
		);
		expect(harness.navigate).toHaveBeenLastCalledWith(
			expect.objectContaining({ search: { window: '24h' } }),
		);
	});

	test('an invalid window search falls back to 24h', async () => {
		harness.search = { window: 'bogus' };
		const requests = useMonitorHandlers();

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		await screen.findByRole('heading', { name: 'API' });
		await waitFor(() => expect(requests.summary).toContain('24h'));
		expect(requests.summary).not.toContain('bogus');
		expect(requests.results).toContain('24h');
	});

	test('a paused monitor shows Paused and a Resume action', async () => {
		useMonitorHandlers(makeMonitor({ paused: true, status: 'paused' }));

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(await screen.findByText('Paused')).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: 'Resume API' }),
		).toBeInTheDocument();
		expect(
			screen.queryByRole('button', { name: 'Pause API' }),
		).not.toBeInTheDocument();
		expect(
			screen.queryByText('Down', { selector: '[data-slot="badge"]' }),
		).not.toBeInTheDocument();
	});

	test('a null last_check shows Never checked', async () => {
		useMonitorHandlers(makeMonitor({ last_check: null }));

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(await screen.findByText('Never checked')).toBeInTheDocument();
	});

	test('an unknown status is never labeled Down', async () => {
		useMonitorHandlers(makeMonitor({ status: 'unknown', last_check: null }));

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(await screen.findByText('Unknown')).toBeInTheDocument();
		expect(
			screen.queryByText('Down', { selector: '[data-slot="badge"]' }),
		).not.toBeInTheDocument();
	});

	test('renders the monitor URL and last check', async () => {
		useMonitorHandlers();

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(await screen.findByText(makeMonitor().url)).toBeInTheDocument();
		expect(
			screen.getByText(formatRelativeTime(CHECKED_AT)),
		).toBeInTheDocument();
	});

	test('delete asks for confirmation and then calls DELETE', async () => {
		const deleteCalls: string[] = [];
		useMonitorHandlers();
		server.use(
			http.delete(monitorPath, () => {
				deleteCalls.push('delete');
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);
		await screen.findByRole('heading', { name: 'API' });

		await user.click(screen.getByRole('button', { name: 'Delete' }));

		expect(
			await screen.findByRole('heading', { name: 'Delete API?' }),
		).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: 'Delete monitor' }));

		await waitFor(() => expect(deleteCalls).toHaveLength(1));
		expect(harness.navigate).toHaveBeenCalledWith({
			to: '/w/$workspaceSlug/monitors',
			params: { workspaceSlug: workspace.slug },
		});
	});

	test('a failed delete keeps the dialog open and shows the error', async () => {
		useMonitorHandlers();
		server.use(
			http.delete(monitorPath, () =>
				problem('internal_error', 500, 'Internal Server Error'),
			),
		);
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);
		await screen.findByRole('heading', { name: 'API' });

		await user.click(screen.getByRole('button', { name: 'Delete' }));
		await user.click(screen.getByRole('button', { name: 'Delete monitor' }));

		const dialog = await screen.findByRole('dialog');
		expect(
			within(dialog).getByRole('heading', { name: 'Delete API?' }),
		).toBeInTheDocument();
		expect(
			within(dialog).getByText('Code: internal_error'),
		).toBeInTheDocument();
		expect(harness.navigate).not.toHaveBeenCalled();
	});

	test('a 5xx monitor error offers a retry', async () => {
		let fail = true;
		useMonitorHandlers();
		server.use(
			http.get(monitorPath, () =>
				fail
					? problem('internal_error', 500, 'Internal Server Error')
					: HttpResponse.json(makeMonitor()),
			),
		);
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(
			await screen.findByText('Could not load monitor'),
		).toBeInTheDocument();

		fail = false;
		await user.click(screen.getByRole('button', { name: 'Try again' }));

		expect(
			await screen.findByRole('heading', { name: 'API' }),
		).toBeInTheDocument();
	});

	test('a 5xx summary error offers a retry', async () => {
		let fail = true;
		useMonitorHandlers();
		server.use(
			http.get(summaryPath, () =>
				fail
					? problem('internal_error', 500, 'Internal Server Error')
					: HttpResponse.json(summary),
			),
		);
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(
			await screen.findByText('Could not load summary'),
		).toBeInTheDocument();

		fail = false;
		await user.click(screen.getByRole('button', { name: 'Try again' }));

		await waitFor(() =>
			expect(
				screen.queryByText('Could not load summary'),
			).not.toBeInTheDocument(),
		);
		expect(screen.getByText('Checks').parentElement).toHaveTextContent('10');
	});

	test('loads the next results page from the cursor', async () => {
		const cursors: (string | null)[] = [];
		useMonitorHandlers();
		server.use(
			http.get(resultsPath, ({ request }) => {
				const cursor = new URL(request.url).searchParams.get('cursor');
				cursors.push(cursor);
				return cursor
					? HttpResponse.json({
							results: [
								{ ...probeResult, id: 'r2', status: 'down', status_code: 500 },
							],
						})
					: HttpResponse.json({
							results: [probeResult],
							next_cursor: 'page-2',
						});
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);
		await screen.findByRole('heading', { name: 'API' });
		await screen.findByText('200');

		await user.click(screen.getByRole('button', { name: 'Load more' }));

		expect(await screen.findByText('500')).toBeInTheDocument();
		expect(cursors).toEqual([null, 'page-2']);
		expect(
			screen.queryByRole('button', { name: 'Load more' }),
		).not.toBeInTheDocument();
	});

	test('an open incident banner links to the incident', async () => {
		const incident = makeIncident();
		useMonitorHandlers();
		useIncidentHandlers([incident]);

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(await screen.findByText('Active incident')).toBeInTheDocument();
		expect(screen.getByText(/Open incident/)).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'View incident' })).toHaveAttribute(
			'href',
			'/w/acme-status/incidents/i1',
		);
	});

	test('an acknowledged incident banner links to the incident', async () => {
		const incident = makeIncident({ status: 'acknowledged' });
		useMonitorHandlers();
		useIncidentHandlers([incident]);

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(await screen.findByText('Active incident')).toBeInTheDocument();
		expect(screen.getByText(/Acknowledged incident/)).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'View incident' })).toHaveAttribute(
			'href',
			'/w/acme-status/incidents/i1',
		);
	});

	test('renders the five most recent incidents for the monitor with a single request', async () => {
		const requests: URLSearchParams[] = [];
		const recent = [
			makeIncident({
				id: 'i2',
				status: 'resolved',
				resolved_at: new Date().toISOString(),
			}),
			makeIncident({ id: 'i3', status: 'acknowledged' }),
		];
		useMonitorHandlers();
		useIncidentHandlers(recent, requests);

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(
			await screen.findByRole('heading', { name: 'Recent incidents' }),
		).toBeInTheDocument();
		expect(
			await screen.findByRole('link', { name: 'Resolved' }),
		).toHaveAttribute('href', '/w/acme-status/incidents/i2');
		expect(screen.getByRole('link', { name: 'Acknowledged' })).toHaveAttribute(
			'href',
			'/w/acme-status/incidents/i3',
		);
		await waitFor(() => expect(requests).toHaveLength(1));
		expect(requests[0].get('status')).toBe('all');
		expect(requests[0].get('monitor_id')).toBe('m1');
		expect(requests[0].get('limit')).toBe('5');
	});

	test('shows a loading skeleton while recent incidents load', async () => {
		useMonitorHandlers();
		server.use(
			http.get(incidentsPath, async () => {
				await delay(100);
				return HttpResponse.json({ incidents: [] });
			}),
		);

		const { container } = renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(
			await screen.findByRole('status', { name: 'Loading incidents' }),
		).toBeInTheDocument();
		expect(
			container.querySelector('[data-slot="skeleton"]'),
		).toBeInTheDocument();
	});

	test('shows an inline incident error with retry', async () => {
		let fail = true;
		useMonitorHandlers();
		server.use(
			http.get(incidentsPath, () =>
				fail
					? problem('internal_error', 500, 'Internal Server Error')
					: HttpResponse.json({ incidents: [] }),
			),
		);
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(
			await screen.findByText('Could not load incidents'),
		).toBeInTheDocument();
		expect(screen.queryByText('No incidents yet')).not.toBeInTheDocument();

		fail = false;
		await user.click(screen.getByRole('button', { name: 'Try again' }));

		expect(await screen.findByText('No incidents yet')).toBeInTheDocument();
		expect(
			screen.queryByText('Could not load incidents'),
		).not.toBeInTheDocument();
	});

	test('shows no incident banner when only resolved incidents exist', async () => {
		const resolved = makeIncident({
			status: 'resolved',
			resolved_at: new Date().toISOString(),
		});
		useMonitorHandlers();
		useIncidentHandlers([resolved]);

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(
			await screen.findByRole('link', { name: 'Resolved' }),
		).toHaveAttribute('href', '/w/acme-status/incidents/i1');
		expect(screen.queryByText('Active incident')).not.toBeInTheDocument();
	});

	test('shows no incident banner and an empty state when there are no incidents', async () => {
		useMonitorHandlers();
		useIncidentHandlers([]);

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(await screen.findByText('No incidents yet')).toBeInTheDocument();
		expect(screen.queryByText('Active incident')).not.toBeInTheDocument();
	});

	test('a member sees and toggles the email alerts switch', async () => {
		const patches: unknown[] = [];
		const state: MonitorAlertState = {
			available: true,
			channel_enabled: true,
			enabled: true,
		};
		useMonitorHandlers(makeMonitor(), undefined, undefined, state);
		server.use(
			http.patch(alertChannelPath, async ({ request }) => {
				const body = (await request.json()) as { enabled: boolean };
				patches.push(body);
				state.enabled = body.enabled;
				return new HttpResponse(null, { status: 204 });
			}),
		);
		const user = userEvent.setup();

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		const toggle = await screen.findByRole('switch', { name: 'Email alerts' });
		await waitFor(() => expect(toggle).toBeChecked());

		await user.click(toggle);

		await waitFor(() => expect(patches).toEqual([{ enabled: false }]));
		await waitFor(() => expect(toggle).not.toBeChecked());
	});

	test('a viewer does not see the email alerts switch', async () => {
		useMonitorHandlers();

		renderWithProviders(
			<MonitorDetailScreen
				workspace={{ ...workspace, role: 'viewer' }}
				monitorId="m1"
			/>,
		);

		await screen.findByRole('heading', { name: 'API' });
		expect(
			screen.queryByRole('switch', { name: 'Email alerts' }),
		).not.toBeInTheDocument();
	});

	test('a disabled workspace channel shows a note and disables the switch', async () => {
		useMonitorHandlers(makeMonitor(), undefined, undefined, {
			available: true,
			channel_enabled: false,
			enabled: false,
		});

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(
			await screen.findByText('Email alerts are disabled for this workspace.'),
		).toBeInTheDocument();
		expect(screen.getByRole('switch', { name: 'Email alerts' })).toBeDisabled();
	});

	test('a missing workspace channel shows a note without an admin link for members', async () => {
		useMonitorHandlers(makeMonitor(), undefined, undefined, {
			available: false,
			channel_enabled: false,
			enabled: false,
		});

		renderWithProviders(
			<MonitorDetailScreen workspace={workspace} monitorId="m1" />,
		);

		expect(
			await screen.findByText(
				'Email alerts are not configured for this workspace.',
			),
		).toBeInTheDocument();
		expect(screen.getByRole('switch', { name: 'Email alerts' })).toBeDisabled();
		expect(
			screen.queryByRole('link', { name: 'Configure email alerts' }),
		).not.toBeInTheDocument();
	});

	test('a missing workspace channel links admins to alert settings', async () => {
		useMonitorHandlers(makeMonitor(), undefined, undefined, {
			available: false,
			channel_enabled: false,
			enabled: false,
		});

		renderWithProviders(
			<MonitorDetailScreen
				workspace={{ ...workspace, role: 'admin' }}
				monitorId="m1"
			/>,
		);

		expect(
			await screen.findByRole('link', { name: 'Configure email alerts' }),
		).toHaveAttribute('href', '/w/acme-status/settings/alerts');
	});
});
